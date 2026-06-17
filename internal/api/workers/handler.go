// Package workers serves /api/workers — a snapshot of the in-memory hub
// registry. Useful for the "is anything connected?" UI.
package workers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/yourteam/crawler-lite/internal/hub"
)

type Handler struct {
	hub *hub.WorkerHub
	log *slog.Logger
}

func NewHandler(h *hub.WorkerHub, log *slog.Logger) *Handler {
	return &Handler{hub: h, log: log}
}

type workerView struct {
	WorkerID     string   `json:"worker_id"`
	SessionID    string   `json:"session_id"`
	Capabilities []string `json:"capabilities"`
	Concurrency  int32    `json:"concurrency"`
	FreeSlots    int32    `json:"free_slots"`
	Running      int32    `json:"running"`
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	sessions := h.hub.Sessions()
	out := make([]workerView, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, workerView{
			WorkerID:     s.WorkerID,
			SessionID:    s.SessionID,
			Capabilities: s.Capabilities,
			Concurrency:  s.Concurrency,
			FreeSlots:    s.FreeSlots,
			Running:      s.RunningTasks,
		})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"items": out})
}
