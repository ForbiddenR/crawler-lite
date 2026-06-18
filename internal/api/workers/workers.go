// Package workers serves /api/workers — a snapshot of the in-memory hub
// registry. Useful for the "is anything connected?" UI.
//
// Style: free function + closures. The handler reads from *hub.WorkerHub
// (which itself owns the in-memory state); no Handler struct here.
package workers

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/yourteam/crawler-lite/internal/api/render"
	"github.com/yourteam/crawler-lite/internal/hub"
)

type workerView struct {
	WorkerID     string   `json:"worker_id"`
	SessionID    string   `json:"session_id"`
	Capabilities []string `json:"capabilities"`
	Concurrency  int32    `json:"concurrency"`
	FreeSlots    int32    `json:"free_slots"`
	Running      int32    `json:"running"`
}

// RegisterRoutes mounts GET /workers on a group already wrapped in
// authMiddleware. log is unused today but kept in the signature for future
// error logging if hub.Sessions ever grows a failure mode.
func RegisterRoutes(g gin.IRoutes, h *hub.WorkerHub, log *slog.Logger) {
	_ = log
	g.GET("/workers", func(c *gin.Context) {
		sessions := h.Sessions()
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
	})
}
