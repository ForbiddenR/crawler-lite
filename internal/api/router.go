// Package api wires HTTP handlers behind a Gin engine. The engine is the only
// HTTP surface; handlers are organized by domain.
//
// All handlers receive their dependencies via the Handlers struct, which is
// constructed once in app.Build. No global state.
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
	"github.com/yourteam/crawler-lite/internal/hub"
	spidersvc "github.com/yourteam/crawler-lite/internal/spider"
	tasksvc "github.com/yourteam/crawler-lite/internal/task"
)

// Handlers is the bag of dependencies passed to every API handler.
type Handlers struct {
	Auth    *authsvc.Service
	Spiders *spidersvc.Service
	Tasks   *tasksvc.Service
	Hub     *hub.WorkerHub
}

func NewRouter(h Handlers, log *slog.Logger) http.Handler {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(slogRecoverer(log))
	r.Use(slogLogger(log))
	r.Use(corsMiddleware()) // dev-friendly; tighten for prod

	r.GET("/healthz", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	authH := auth.NewHandler(h.Auth, log)
	spidersH := spiders.NewHandler(h.Spiders, log)
	tasksH := tasks.NewHandler(h.Tasks, log)
	workersH := workers.NewHandler(h.Hub, log)

	api := r.Group("/api")
	{
		// public
		api.POST("/auth/login", authH.Login)

		// authed
		authed := api.Group("")
		authed.Use(authMiddleware(h.Auth, log))
		{
			authed.GET("/auth/me", authH.Me)

			authed.GET("/spiders", spidersH.List)
			authed.POST("/spiders", spidersH.Create)
			authed.GET("/spiders/:id", spidersH.Get)
			authed.PATCH("/spiders/:id", spidersH.Update)
			authed.DELETE("/spiders/:id", spidersH.Delete)

			authed.GET("/tasks", tasksH.List)
			authed.POST("/tasks", tasksH.Create)
			authed.GET("/tasks/:id", tasksH.Get)
			authed.POST("/tasks/:id/cancel", tasksH.Cancel)

			authed.GET("/workers", workersH.List)
		}
	}

	return r
}
