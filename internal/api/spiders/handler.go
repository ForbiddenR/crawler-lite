// Package spiders contains the HTTP handlers for /api/spiders.
package spiders

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

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

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.List(r.Context())
	if err != nil {
		h.log.Error("list spiders", "err", err)
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
	s, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "spider not found")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if !decode(w, r, &req) {
		return
	}
	s, err := h.svc.Create(r.Context(), spider.CreateInput{
		Name:        req.Name,
		Description: req.Description,
		EntryModule: req.EntryModule,
		Config:      req.Config,
	})
	if err != nil {
		if errors.Is(err, spider.ErrInvalidInput) {
			writeError(w, http.StatusBadRequest, "name and entry_module are required")
			return
		}
		h.log.Error("create spider", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create")
		return
	}
	writeJSON(w, http.StatusCreated, s)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	var req updateReq
	if !decode(w, r, &req) {
		return
	}
	s, err := h.svc.Update(r.Context(), id, spider.UpdateInput{
		Name:        req.Name,
		Description: req.Description,
		EntryModule: req.EntryModule,
		Status:      req.Status,
		Config:      req.Config,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "spider not found")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
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
