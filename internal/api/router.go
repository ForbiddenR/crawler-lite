// Package api wires HTTP handlers behind a chi router. The router is the
// only HTTP surface; handlers are organized by domain.
//
// All handlers receive their dependencies via the Handlers struct, which is
// constructed once in app.Build. No global state.
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

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
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(slogRecoverer(log))
	r.Use(slogLogger(log))
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(corsMiddleware) // dev-friendly; tighten for prod

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	authH := auth.NewHandler(h.Auth, log)
	spidersH := spiders.NewHandler(h.Spiders, log)
	tasksH := tasks.NewHandler(h.Tasks, log)
	workersH := workers.NewHandler(h.Hub, log)

	r.Route("/api", func(r chi.Router) {
		// public
		r.Post("/auth/login", authH.Login)

		// authed
		r.Group(func(r chi.Router) {
			r.Use(authMiddleware(h.Auth, log))

			r.Get("/auth/me", authH.Me)

			r.Get("/spiders", spidersH.List)
			r.Post("/spiders", spidersH.Create)
			r.Get("/spiders/{id}", spidersH.Get)
			r.Patch("/spiders/{id}", spidersH.Update)
			r.Delete("/spiders/{id}", spidersH.Delete)

			r.Get("/tasks", tasksH.List)
			r.Post("/tasks", tasksH.Create)
			r.Get("/tasks/{id}", tasksH.Get)
			r.Post("/tasks/{id}/cancel", tasksH.Cancel)

			r.Get("/workers", workersH.List)
		})
	})

	return r
}
