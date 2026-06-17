// Package workers serves /api/workers — a snapshot of the in-memory hub
// registry. Useful for the "is anything connected?" UI.
package workers

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/yourteam/crawler-lite/internal/api/render"
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

func (h *Handler) List(c *gin.Context) {
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
	render.JSON(c, http.StatusOK, gin.H{"items": out})
}
