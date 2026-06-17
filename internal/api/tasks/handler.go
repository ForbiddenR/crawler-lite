// Package tasks contains the HTTP handlers for /api/tasks.
package tasks

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/yourteam/crawler-lite/internal/api/render"
	"github.com/yourteam/crawler-lite/internal/task"
)

type Handler struct {
	svc *task.Service
	log *slog.Logger
}

func NewHandler(svc *task.Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

type createReq struct {
	SpiderID int64          `json:"spider_id"`
	Args     map[string]any `json:"args,omitempty"`
}

func (h *Handler) List(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	out, err := h.svc.List(c.Request.Context(), limit, offset)
	if err != nil {
		h.log.Error("list tasks", "err", err)
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
	t, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		render.Error(c, http.StatusNotFound, "task not found")
		return
	}
	render.JSON(c, http.StatusOK, t)
}

func (h *Handler) Create(c *gin.Context) {
	var req createReq
	if !render.Decode(c, &req) {
		return
	}
	if req.SpiderID == 0 {
		render.Error(c, http.StatusBadRequest, "spider_id required")
		return
	}
	t, err := h.svc.Queue(c.Request.Context(), task.CreateInput{
		SpiderID:      req.SpiderID,
		Trigger:       task.TriggerManual,
		SpiderVersion: 1,
		TriggeredArgs: req.Args,
	})
	if err != nil {
		h.log.Error("queue task", "err", err)
		render.Error(c, http.StatusInternalServerError, "failed to queue")
		return
	}
	render.JSON(c, http.StatusCreated, t)
}

func (h *Handler) Cancel(c *gin.Context) {
	id, ok := pathInt64(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Cancel(c.Request.Context(), id); err != nil {
		h.log.Error("cancel task", "err", err)
		render.Error(c, http.StatusInternalServerError, "cancel failed")
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
