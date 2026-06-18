// Package api wires HTTP handlers behind a Gin engine. The router itself is
// stateless: each domain registers routes via free functions that capture
// their dependencies as closure params. No *Handler structs unless a route
// genuinely owns mutable in-memory state.
package api

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/yourteam/crawler-lite/internal/api/auth"
	"github.com/yourteam/crawler-lite/internal/api/spiders"
	"github.com/yourteam/crawler-lite/internal/api/tasks"
	"github.com/yourteam/crawler-lite/internal/api/workers"
	authsvc "github.com/yourteam/crawler-lite/internal/auth"
	"github.com/yourteam/crawler-lite/internal/cache"
	"github.com/yourteam/crawler-lite/internal/hub"
	"github.com/yourteam/crawler-lite/internal/repository"
	spidersvc "github.com/yourteam/crawler-lite/internal/spider"
	"github.com/yourteam/crawler-lite/internal/storage"
	tasksvc "github.com/yourteam/crawler-lite/internal/task"
)

// Deps is what NewRouter needs. Everything is a long-lived service or client
// owned by app.App; the router does not construct anything itself.
type Deps struct {
	Auth    *authsvc.Service
	Spiders *spidersvc.Service
	Tasks   *tasksvc.Service
	Hub     *hub.WorkerHub

	// Tasks read endpoints + log WS need direct access to cache/storage/repos
	// so they don't have to round-trip through services.
	Cache *cache.Client
	Store *storage.MinIOClient
	Repos *repository.Repos
}

func NewRouter(d Deps, log *slog.Logger) http.Handler {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(slogRecoverer(log))
	r.Use(slogLogger(log))
	r.Use(corsMiddleware()) // dev-friendly; tighten for prod

	r.GET("/healthz", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	api := r.Group("/api")
	authed := api.Group("")
	authed.Use(authMiddleware(d.Auth, log))

	// Each domain exposes one or two free functions taking the group + the
	// deps it needs. No Handler struct, no NewHandler.
	auth.RegisterPublicRoutes(api, d.Auth, log)
	auth.RegisterProtectedRoutes(authed, d.Auth, log)

	spiders.RegisterRoutes(authed, d.Spiders, log)

	taskDeps := tasks.Deps{
		Service:   d.Tasks,
		Cache:     d.Cache,
		Store:     d.Store,
		Items:     d.Repos.Items,
		Artifacts: d.Repos.Artifacts,
		Log:       log,
	}
	tasks.RegisterRoutes(authed, taskDeps)
	// WebSocket: token comes from ?token=, so it lives on the public api
	// group and authenticates internally.
	tasks.RegisterLogStream(api, taskDeps, d.Auth)

	workers.RegisterRoutes(authed, d.Hub, log)

	return r
}
