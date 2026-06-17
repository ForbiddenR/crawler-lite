// Package spiders contains the HTTP handlers for /api/spiders.
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

type Handler struct {
	svc *spider.Service
	log *slog.Logger
}

func NewHandler(svc *spider.Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

type createReq struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	EntryModule string         `json:"entry_module"`
	Config      map[string]any `json:"config,omitempty"`
}

type updateReq struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	EntryModule string         `json:"entry_module"`
	Status      spider.Status  `json:"status"`
	Config      map[string]any `json:"config"`
}

func (h *Handler) List(c *gin.Context) {
	out, err := h.svc.List(c.Request.Context())
	if err != nil {
		h.log.Error("list spiders", "err", err)
		render.Error(c, http.StatusInternalServerError, "failed to list")
		return
	}
	render.JSON(c, http.StatusOK, gin.H{"items": out})
}

func (h *Handler) Get(c *gin.Context) {
	id, ok := pathInt64(c, "id")
	if !ok {
		return
	}
	s, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		render.Error(c, http.StatusNotFound, "spider not found")
		return
	}
	render.JSON(c, http.StatusOK, s)
}

func (h *Handler) Create(c *gin.Context) {
	var req createReq
	if !render.Decode(c, &req) {
		return
	}
	s, err := h.svc.Create(c.Request.Context(), spider.CreateInput{
		Name:        req.Name,
		Description: req.Description,
		EntryModule: req.EntryModule,
		Config:      req.Config,
	})
	if err != nil {
		if errors.Is(err, spider.ErrInvalidInput) {
			render.Error(c, http.StatusBadRequest, "name and entry_module are required")
			return
		}
		h.log.Error("create spider", "err", err)
		render.Error(c, http.StatusInternalServerError, "failed to create")
		return
	}
	render.JSON(c, http.StatusCreated, s)
}

func (h *Handler) Update(c *gin.Context) {
	id, ok := pathInt64(c, "id")
	if !ok {
		return
	}
	var req updateReq
	if !render.Decode(c, &req) {
		return
	}
	s, err := h.svc.Update(c.Request.Context(), id, spider.UpdateInput{
		Name:        req.Name,
		Description: req.Description,
		EntryModule: req.EntryModule,
		Status:      req.Status,
		Config:      req.Config,
	})
	if err != nil {
		render.Error(c, http.StatusNotFound, "spider not found")
		return
	}
	render.JSON(c, http.StatusOK, s)
}

func (h *Handler) Delete(c *gin.Context) {
	id, ok := pathInt64(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		render.Error(c, http.StatusInternalServerError, "delete failed")
		return
	}
	c.Status(http.StatusNoContent)
}

func pathInt64(c *gin.Context, key string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(key), 10, 64)
	if err != nil || id <= 0 {
		render.Error(c, http.StatusBadRequest, "invalid "+key)
		return 0, false
	}
	return id, true
}
