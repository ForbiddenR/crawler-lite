// Package workerapp is the worker process composition root. The worker is
// just one long-lived gRPC client that opens a stream to the master, sends
// Hello, and processes assignments via TaskExecutor (which spawns Python).
package workerapp

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/yourteam/crawler-lite/internal/runner"
	"github.com/yourteam/crawler-lite/internal/storage"
)

type App struct {
	cfg    Config
	log    *slog.Logger
	worker *runner.Worker
}

func Build(_ context.Context, cfg Config, log *slog.Logger) (*App, error) {
	if cfg.WorkerID == "" {
		// Fall back to the container hostname so the production compose
		// can scale workers (docker compose up --scale worker=N) without
		// setting a distinct WORKER_ID per replica. Docker assigns
		// "<project>-worker-<n>" hostnames that are stable across
		// restarts and unique across replicas. An explicit WORKER_ID
		// always wins.
		hostname, err := os.Hostname()
		if err != nil || hostname == "" {
			return nil, fmt.Errorf("WORKER_ID is required and hostname lookup failed: %w", err)
		}
		cfg.WorkerID = hostname
		log.Info("WORKER_ID unset, falling back to hostname", "worker_id", cfg.WorkerID)
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}

	mc, err := minio.New(cfg.MinIOEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinIOAccessKey, cfg.MinIOSecretKey, ""),
		Secure: cfg.MinIOSecure,
	})
	if err != nil {
		return nil, fmt.Errorf("minio: %w", err)
	}
	store := storage.NewMinIOClient(mc, cfg.MinIOBucket)

	exec := runner.NewTaskExecutor(store, cfg.PythonPath, cfg.WorkDir, cfg.VenvDir, cfg.UVPath, log)

	w := runner.NewWorker(runner.Config{
		MasterAddr:   cfg.MasterGRPCAddr,
		WorkerID:     cfg.WorkerID,
		Concurrency:  cfg.Concurrency,
		Capabilities: cfg.Capabilities(),
		SharedSecret: cfg.WorkerSharedSecret,
	}, exec, log)

	return &App{cfg: cfg, log: log, worker: w}, nil
}

func (a *App) Run(ctx context.Context) error {
	a.log.Info("worker starting",
		"worker_id", a.cfg.WorkerID,
		"master", a.cfg.MasterGRPCAddr,
		"concurrency", a.cfg.Concurrency,
		"python", a.cfg.PythonPath,
		"venv_dir", a.cfg.VenvDir,
	)
	return a.worker.Run(ctx)
}
