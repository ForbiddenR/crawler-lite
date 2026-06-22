// Package notifications contains HTTP handlers for /api/notifications.
//
// Style mirrors internal/api/schedules: free functions over a Deps
// struct. No Handler structs; closures capture deps.
package notifications

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/yourteam/crawler-lite/internal/api/render"
	"github.com/yourteam/crawler-lite/internal/notify"
)

// Deps groups the constructor arguments.
type Deps struct {
	Service *notify.Service
	Log     *slog.Logger
}

type createReq struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	URL     string   `json:"url"`
	Events  []string `json:"events"`
	Enabled *bool    `json:"enabled,omitempty"`
}

type updateReq struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	URL     string   `json:"url"`
	Events  []string `json:"events"`
	Enabled *bool    `json:"enabled,omitempty"`
}

// RegisterRoutes mounts /api/notifications on a group already wrapped
// in authMiddleware.
func RegisterRoutes(g gin.IRoutes, d Deps) {
	g.GET("/notifications", func(c *gin.Context) {
		out, err := d.Service.List(c.Request.Context())
		if err != nil {
			d.Log.Error("list notifications", "err", err)
			render.Error(c, http.StatusInternalServerError, "failed to list")
			return
		}
		render.JSON(c, http.StatusOK, gin.H{"items": out})
	})

	g.GET("/notifications/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		ch, err := d.Service.Get(c.Request.Context(), id)
		if err != nil {
			render.Error(c, http.StatusNotFound, "channel not found")
			return
		}
		render.JSON(c, http.StatusOK, ch)
	})

	g.POST("/notifications", func(c *gin.Context) {
		var req createReq
		if !render.Decode(c, &req) {
			return
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		ch, err := d.Service.Create(c.Request.Context(), notify.CreateInput{
			Name:    req.Name,
			Kind:    req.Kind,
			URL:     req.URL,
			Events:  req.Events,
			Enabled: enabled,
		})
		if err != nil {
			if errors.Is(err, notify.ErrInvalidInput) {
				render.Error(c, http.StatusBadRequest, "name, kind, url, events all required")
				return
			}
			if errors.Is(err, notify.ErrInvalidURL) {
				render.Error(c, http.StatusBadRequest, "invalid notification url")
				return
			}
			d.Log.Error("create notification", "err", err)
			render.Error(c, http.StatusInternalServerError, "failed to create")
			return
		}
		render.JSON(c, http.StatusCreated, ch)
	})

	g.PATCH("/notifications/:id", func(c *gin.Context) {
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
		ch, err := d.Service.Update(c.Request.Context(), id, notify.UpdateInput{
			Name:    req.Name,
			Kind:    req.Kind,
			URL:     req.URL,
			Events:  req.Events,
			Enabled: enabled,
		})
		if err != nil {
			if errors.Is(err, notify.ErrInvalidInput) {
				render.Error(c, http.StatusBadRequest, "name is required")
				return
			}
			if errors.Is(err, notify.ErrInvalidURL) {
				render.Error(c, http.StatusBadRequest, "invalid notification url")
				return
			}
			d.Log.Error("update notification", "id", id, "err", err)
			render.Error(c, http.StatusInternalServerError, "failed to update")
			return
		}
		render.JSON(c, http.StatusOK, ch)
	})

	g.DELETE("/notifications/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		if err := d.Service.Delete(c.Request.Context(), id); err != nil {
			d.Log.Error("delete notification", "id", id, "err", err)
			render.Error(c, http.StatusInternalServerError, "failed to delete")
			return
		}
		c.Status(http.StatusNoContent)
	})

	// POST /:id/test — fires a canned message to verify the channel.
	// Surface sender errors as 502 with the underlying message so the
	// operator can see e.g. "401 from Slack" without grepping logs.
	g.POST("/notifications/:id/test", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		if err := d.Service.Test(c.Request.Context(), id); err != nil {
			d.Log.Warn("test notification", "id", id, "err", err)
			render.Error(c, http.StatusBadGateway, err.Error())
			return
		}
		render.JSON(c, http.StatusOK, gin.H{"ok": true})
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
