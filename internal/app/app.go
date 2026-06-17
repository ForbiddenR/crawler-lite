// Package app is the master composition root.
//
// Read app.go top-to-bottom to understand how every long-lived dependency is
// constructed and wired. There is no DI container; each constructor takes
// exactly the dependencies it needs as arguments.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	"github.com/yourteam/crawler-lite/internal/api"
	"github.com/yourteam/crawler-lite/internal/auth"
	"github.com/yourteam/crawler-lite/internal/cache"
	"github.com/yourteam/crawler-lite/internal/hub"
	pb "github.com/yourteam/crawler-lite/internal/pb/worker/v1"
	"github.com/yourteam/crawler-lite/internal/repository"
	"github.com/yourteam/crawler-lite/internal/spider"
	"github.com/yourteam/crawler-lite/internal/storage"
	"github.com/yourteam/crawler-lite/internal/task"
)

// App holds every long-lived dependency for the master process.
type App struct {
	cfg Config
	log *slog.Logger

	// Infrastructure
	db    *pgxpool.Pool
	rdb   *redis.Client
	mc    *minio.Client
	store *storage.MinIOClient

	// Repositories
	repos *repository.Repos

	// Services
	auth   *auth.Service
	spider *spider.Service
	task   *task.Service

	// Network surface
	hub        *hub.WorkerHub
	httpServer *http.Server
	grpcServer *grpc.Server
}

// Build is the composition root. Read top-to-bottom.
func Build(ctx context.Context, cfg Config, log *slog.Logger) (*App, error) {
	// --- 1. Infrastructure -------------------------------------------------
	dbCfg, err := pgxpool.ParseConfig(cfg.DatabaseDSN)
	if err != nil {
		return nil, fmt.Errorf("parse db dsn: %w", err)
	}
	dbCfg.MaxConns = cfg.DBPoolSize
	db, err := pgxpool.NewWithConfig(ctx, dbCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	if err := db.Ping(ctx); err != nil {
		return nil, fmt.Errorf("postgres ping: %w", err)
	}

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}
	// cache.Client wraps rdb; constructed in week 2 when first consumer arrives.
	_ = cache.NewClient(rdb)

	mc, err := minio.New(cfg.MinIOEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinIOAccessKey, cfg.MinIOSecretKey, ""),
		Secure: cfg.MinIOSecure,
	})
	if err != nil {
		return nil, fmt.Errorf("minio: %w", err)
	}
	store := storage.NewMinIOClient(mc, cfg.MinIOBucket)

	// --- 2. Repositories ---------------------------------------------------
	repos := repository.New(db)

	// --- 3. Domain services ------------------------------------------------
	hasher := auth.NewBcryptHasher(cfg.BcryptCost)
	jwt := auth.NewJWTIssuer(cfg.JWTSecret, cfg.JWTTTL)
	authSvc := auth.NewService(repos.Users, hasher, jwt, log)

	spiderSvc := spider.NewService(repos.Spiders, store, log)

	// hub and task service have a controlled cycle; resolve via a setter.
	workerHub := hub.New(log)
	taskSvc := task.NewService(repos.Tasks, repos.Spiders, workerHub, log)
	workerHub.BindTaskService(taskSvc)

	// --- 4. Network surface ------------------------------------------------
	router := api.NewRouter(api.Handlers{
		Auth:    authSvc,
		Spiders: spiderSvc,
		Tasks:   taskSvc,
		Hub:     workerHub,
	}, log)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(8 * 1024 * 1024),
	)
	pb.RegisterWorkerHubServer(grpcServer, workerHub)

	return &App{
		cfg: cfg, log: log,
		db: db, rdb: rdb, mc: mc, store: store,
		repos:  repos,
		auth:   authSvc,
		spider: spiderSvc,
		task:   taskSvc,
		hub:    workerHub,
		httpServer: httpServer,
		grpcServer: grpcServer,
	}, nil
}

// Run blocks until ctx is cancelled or any long-lived component exits.
func (a *App) Run(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		ln, err := net.Listen("tcp", a.cfg.GRPCAddr)
		if err != nil {
			return fmt.Errorf("grpc listen: %w", err)
		}
		a.log.Info("grpc listening", "addr", a.cfg.GRPCAddr)
		return a.grpcServer.Serve(ln)
	})

	g.Go(func() error {
		a.log.Info("http listening", "addr", a.cfg.HTTPAddr)
		err := a.httpServer.ListenAndServe()
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	})

	g.Go(func() error {
		<-ctx.Done()
		a.log.Info("shutting down")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		stopped := make(chan struct{})
		go func() {
			a.grpcServer.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-shutdownCtx.Done():
			a.grpcServer.Stop()
		}

		_ = a.httpServer.Shutdown(shutdownCtx)
		a.db.Close()
		_ = a.rdb.Close()
		return nil
	})

	return g.Wait()
}
