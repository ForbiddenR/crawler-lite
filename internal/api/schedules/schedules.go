// Package schedules contains the HTTP handlers for /api/schedules.
//
// Style mirrors internal/api/tasks: free functions over a Deps struct.
// Mutations call Runner.Reload after the database write so the in-process
// cron picks up the new state immediately.
package schedules

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/yourteam/crawler-lite/internal/api/render"
	"github.com/yourteam/crawler-lite/internal/schedule"
)

// Runner is the slice of schedule.Runner this package needs. We accept an
// interface so handler tests can pass a no-op implementation.
type Runner interface {
	Reload(ctx context.Context) error
}

// Deps groups the constructor arguments. Service does the persistence /
// validation, Runner is the live cron daemon.
type Deps struct {
	Service *schedule.Service
	Runner  Runner
	Log     *slog.Logger
}

type createReq struct {
	SpiderID int64          `json:"spider_id"`
	Name     string         `json:"name"`
	CronExpr string         `json:"cron_expr"`
	Args     map[string]any `json:"args,omitempty"`
	Enabled  *bool          `json:"enabled,omitempty"`
}

type updateReq struct {
	Name     string         `json:"name"`
	CronExpr string         `json:"cron_expr"`
	Args     map[string]any `json:"args,omitempty"`
	Enabled  *bool          `json:"enabled,omitempty"`
}

// RegisterRoutes mounts /api/schedules on a group already wrapped in
// authMiddleware.
func RegisterRoutes(g gin.IRoutes, d Deps) {
	g.GET("/schedules", func(c *gin.Context) {
		out, err := d.Service.List(c.Request.Context())
		if err != nil {
			d.Log.Error("list schedules", "err", err)
			render.Error(c, http.StatusInternalServerError, "failed to list")
			return
		}
		render.JSON(c, http.StatusOK, gin.H{"items": out})
	})

	g.GET("/schedules/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		s, err := d.Service.Get(c.Request.Context(), id)
		if err != nil {
			render.Error(c, http.StatusNotFound, "schedule not found")
			return
		}
		render.JSON(c, http.StatusOK, s)
	})

	g.POST("/schedules", func(c *gin.Context) {
		var req createReq
		if !render.Decode(c, &req) {
			return
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		// CreatedBy is left at 0 — the repo coerces 0 → NULL. Other domains
		// (tasks, spiders) don't track creator on the API path either; revisit
		// if/when the schedules table grows audit columns the UI exposes.
		s, err := d.Service.Create(c.Request.Context(), schedule.CreateInput{
			SpiderID: req.SpiderID,
			Name:     req.Name,
			CronExpr: req.CronExpr,
			Args:     req.Args,
			Enabled:  enabled,
		})
		if err != nil {
			if errors.Is(err, schedule.ErrInvalidInput) {
				render.Error(c, http.StatusBadRequest, "spider_id and name are required")
				return
			}
			if errors.Is(err, schedule.ErrInvalidCron) {
				render.Error(c, http.StatusBadRequest, "invalid cron expression")
				return
			}
			d.Log.Error("create schedule", "err", err)
			render.Error(c, http.StatusInternalServerError, "failed to create")
			return
		}
		if err := d.Runner.Reload(c.Request.Context()); err != nil {
			d.Log.Warn("reload after create", "err", err)
		}
		render.JSON(c, http.StatusCreated, s)
	})

	g.PATCH("/schedules/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var req updateReq
		if !render.Decode(c, &req) {
			return
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		s, err := d.Service.Update(c.Request.Context(), id, schedule.UpdateInput{
			Name:     req.Name,
			CronExpr: req.CronExpr,
			Args:     req.Args,
			Enabled:  enabled,
		})
		if err != nil {
			if errors.Is(err, schedule.ErrInvalidInput) {
				render.Error(c, http.StatusBadRequest, "name is required")
				return
			}
			if errors.Is(err, schedule.ErrInvalidCron) {
				render.Error(c, http.StatusBadRequest, "invalid cron expression")
				return
			}
			d.Log.Error("update schedule", "id", id, "err", err)
			render.Error(c, http.StatusInternalServerError, "failed to update")
			return
		}
		if err := d.Runner.Reload(c.Request.Context()); err != nil {
			d.Log.Warn("reload after update", "err", err)
		}
		render.JSON(c, http.StatusOK, s)
	})

	g.DELETE("/schedules/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		if err := d.Service.Delete(c.Request.Context(), id); err != nil {
			d.Log.Error("delete schedule", "id", id, "err", err)
			render.Error(c, http.StatusInternalServerError, "failed to delete")
			return
		}
		if err := d.Runner.Reload(c.Request.Context()); err != nil {
			d.Log.Warn("reload after delete", "err", err)
		}
		c.Status(http.StatusNoContent)
	})
}

func pathInt64(c *gin.Context, key string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(key), 10, 64)
	if err != nil || id <= 0 {
		render.Error(c, http.StatusBadRequest, "invalid "+key)
		return 0, false
	}
	return id, true
}
