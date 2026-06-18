// Package spiders registers HTTP handlers for /api/spiders.
//
// Style: free function + closures. All handlers are stateless and delegate to
// spider.Service, which owns the database and storage clients.
package spiders

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/yourteam/crawler-lite/internal/api/render"
	"github.com/yourteam/crawler-lite/internal/spider"
)

type createReq struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	EntryModule string         `json:"entry_module"`
	Config      map[string]any `json:"config,omitempty"`
	GitURL      string         `json:"git_url,omitempty"`
	GitBranch   string         `json:"git_branch,omitempty"`
}

type updateReq struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	EntryModule string         `json:"entry_module"`
	Status      spider.Status  `json:"status"`
	Config      map[string]any `json:"config"`
	GitURL      string         `json:"git_url"`
	GitBranch   string         `json:"git_branch"`
}

// RegisterRoutes mounts spider CRUD on a group already wrapped in authMiddleware.
func RegisterRoutes(g gin.IRoutes, svc *spider.Service, log *slog.Logger) {
	g.GET("/spiders", func(c *gin.Context) {
		out, err := svc.List(c.Request.Context())
		if err != nil {
			log.Error("list spiders", "err", err)
			render.Error(c, http.StatusInternalServerError, "failed to list")
			return
		}
		render.JSON(c, http.StatusOK, gin.H{"items": out})
	})

	g.GET("/spiders/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		s, err := svc.Get(c.Request.Context(), id)
		if err != nil {
			render.Error(c, http.StatusNotFound, "spider not found")
			return
		}
		render.JSON(c, http.StatusOK, s)
	})

	g.POST("/spiders", func(c *gin.Context) {
		var req createReq
		if !render.Decode(c, &req) {
			return
		}
		s, err := svc.Create(c.Request.Context(), spider.CreateInput{
			Name:        req.Name,
			Description: req.Description,
			EntryModule: req.EntryModule,
			Config:      req.Config,
			GitURL:      req.GitURL,
			GitBranch:   req.GitBranch,
		})
		if err != nil {
			if errors.Is(err, spider.ErrInvalidInput) {
				render.Error(c, http.StatusBadRequest, "name and entry_module are required")
				return
			}
			log.Error("create spider", "err", err)
			render.Error(c, http.StatusInternalServerError, "failed to create")
			return
		}
		render.JSON(c, http.StatusCreated, s)
	})

	g.PATCH("/spiders/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var req updateReq
		if !render.Decode(c, &req) {
			return
		}
		s, err := svc.Update(c.Request.Context(), id, spider.UpdateInput{
			Name:        req.Name,
			Description: req.Description,
			EntryModule: req.EntryModule,
			Status:      req.Status,
			Config:      req.Config,
			GitURL:      req.GitURL,
			GitBranch:   req.GitBranch,
		})
		if err != nil {
			render.Error(c, http.StatusNotFound, "spider not found")
			return
		}
		render.JSON(c, http.StatusOK, s)
	})

	g.DELETE("/spiders/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		if err := svc.Delete(c.Request.Context(), id); err != nil {
			render.Error(c, http.StatusInternalServerError, "delete failed")
			return
		}
		c.Status(http.StatusNoContent)
	})

	// Sync triggers a git clone of the spider's source. Returns the updated
	// spider so the UI can show the new version + commit. Errors from the
	// clone surface as 422 — they're caused by the user's git_url/branch,
	// not a server bug.
	g.POST("/spiders/:id/sync", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		s, err := svc.Sync(c.Request.Context(), id)
		if err != nil {
			switch {
			case errors.Is(err, spider.ErrNoGitURL):
				render.Error(c, http.StatusBadRequest, "spider has no git_url")
			default:
				log.Warn("spider sync failed", "spider_id", id, "err", err)
				render.Error(c, http.StatusUnprocessableEntity, err.Error())
			}
			return
		}
		render.JSON(c, http.StatusOK, s)
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
