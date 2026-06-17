// Package tasks contains the HTTP handlers for /api/tasks.
package tasks

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

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

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	out, err := h.svc.List(r.Context(), limit, offset)
	if err != nil {
		h.log.Error("list tasks", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	t, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if !decode(w, r, &req) {
		return
	}
	if req.SpiderID == 0 {
		writeError(w, http.StatusBadRequest, "spider_id required")
		return
	}
	t, err := h.svc.Queue(r.Context(), task.CreateInput{
		SpiderID:      req.SpiderID,
		Trigger:       task.TriggerManual,
		SpiderVersion: 1,
		TriggeredArgs: req.Args,
	})
	if err != nil {
		h.log.Error("queue task", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to queue")
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Cancel(r.Context(), id); err != nil {
		h.log.Error("cancel task", "err", err)
		writeError(w, http.StatusInternalServerError, "cancel failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pathInt64(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	raw := chi.URLParam(r, key)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid "+key)
		return 0, false
	}
	return id, true
}
