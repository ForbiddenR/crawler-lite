// Package tasks contains the HTTP handlers for /api/tasks.
//
// Style: free functions + closures over a Deps struct. Tasks needs more deps
// than other domains (Service, Cache, Store, two repo slices, Log), so they
// are bundled into a Deps value rather than threaded through long parameter
// lists.
package tasks

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/yourteam/crawler-lite/internal/api/render"
	"github.com/yourteam/crawler-lite/internal/artifacts"
	authsvc "github.com/yourteam/crawler-lite/internal/auth"
	"github.com/yourteam/crawler-lite/internal/cache"
	"github.com/yourteam/crawler-lite/internal/items"
	"github.com/yourteam/crawler-lite/internal/storage"
	"github.com/yourteam/crawler-lite/internal/task"
)

// Deps groups the constructor arguments for the tasks routes. Several
// endpoints (items, log history, log stream) need direct access to repos and
// infrastructure, not just task.Service — bundling avoids a sprawling
// register signature.
type Deps struct {
	Service   *task.Service
	Cache     *cache.Client
	Store     *storage.MinIOClient
	Items     ItemRepo
	Artifacts ArtifactsRepo
	Log       *slog.Logger
}

// ItemRepo is the slice of repository.ItemRepo this package needs.
type ItemRepo interface {
	ListByTask(ctx context.Context, taskID int64, limit, offset int) ([]*items.Item, error)
}

// ArtifactsRepo is the slice we need for screenshots + log index.
type ArtifactsRepo interface {
	ListScreenshots(ctx context.Context, taskID int64) ([]*artifacts.Screenshot, error)
	GetLogIndex(ctx context.Context, taskID int64) (*artifacts.LogIndex, error)
}

type createReq struct {
	SpiderID int64          `json:"spider_id"`
	Args     map[string]any `json:"args,omitempty"`
}

// RegisterRoutes mounts the JSON task endpoints on a group already wrapped in
// authMiddleware. The WebSocket log-stream endpoint is registered separately
// via RegisterLogStream because it authenticates via a query-param token.
func RegisterRoutes(g gin.IRoutes, d Deps) {
	g.GET("/tasks", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.Query("limit"))
		offset, _ := strconv.Atoi(c.Query("offset"))
		out, err := d.Service.List(c.Request.Context(), limit, offset)
		if err != nil {
			d.Log.Error("list tasks", "err", err)
			render.Error(c, http.StatusInternalServerError, "failed to list")
			return
		}
		render.JSON(c, http.StatusOK, gin.H{"items": out})
	})

	g.GET("/tasks/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		t, err := d.Service.Get(c.Request.Context(), id)
		if err != nil {
			render.Error(c, http.StatusNotFound, "task not found")
			return
		}
		render.JSON(c, http.StatusOK, t)
	})

	g.POST("/tasks", func(c *gin.Context) {
		var req createReq
		if !render.Decode(c, &req) {
			return
		}
		if req.SpiderID == 0 {
			render.Error(c, http.StatusBadRequest, "spider_id required")
			return
		}
		t, err := d.Service.Queue(c.Request.Context(), task.CreateInput{
			SpiderID:      req.SpiderID,
			Trigger:       task.TriggerManual,
			TriggeredArgs: req.Args,
		})
		if err != nil {
			if errors.Is(err, task.ErrNoSource) {
				render.Error(c, http.StatusBadRequest, "spider has no synced source — run /sync first")
				return
			}
			d.Log.Error("queue task", "err", err)
			render.Error(c, http.StatusInternalServerError, "failed to queue")
			return
		}
		render.JSON(c, http.StatusCreated, t)
	})

	g.POST("/tasks/:id/cancel", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		if err := d.Service.Cancel(c.Request.Context(), id); err != nil {
			d.Log.Error("cancel task", "err", err)
			render.Error(c, http.StatusInternalServerError, "cancel failed")
			return
		}
		c.Status(http.StatusNoContent)
	})

	// Items: paginated list of items emitted by the task.
	g.GET("/tasks/:id/items", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		limit, _ := strconv.Atoi(c.Query("limit"))
		if limit <= 0 || limit > 500 {
			limit = 100
		}
		offset, _ := strconv.Atoi(c.Query("offset"))
		if offset < 0 {
			offset = 0
		}
		out, err := d.Items.ListByTask(c.Request.Context(), id, limit, offset)
		if err != nil {
			d.Log.Error("list items", "err", err)
			render.Error(c, http.StatusInternalServerError, "failed to list items")
			return
		}
		render.JSON(c, http.StatusOK, gin.H{"items": out})
	})

	// Screenshots: each entry includes a short-lived presigned URL so the UI
	// can <img src=...> directly.
	g.GET("/tasks/:id/screenshots", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		rows, err := d.Artifacts.ListScreenshots(c.Request.Context(), id)
		if err != nil {
			render.Error(c, http.StatusInternalServerError, "failed to list screenshots")
			return
		}
		out := make([]gin.H, 0, len(rows))
		for _, s := range rows {
			url, err := d.Store.PresignGet(c.Request.Context(), s.StorageKey, 30*time.Minute)
			if err != nil {
				d.Log.Warn("presign", "key", s.StorageKey, "err", err)
			}
			out = append(out, gin.H{
				"id":       s.ID,
				"task_id":  s.TaskID,
				"taken_at": s.TakenAt,
				"name":     s.Name,
				"url":      url,
				"page_url": s.URL,
				"width":    s.Width,
				"height":   s.Height,
				"bytes":    s.Bytes,
			})
		}
		render.JSON(c, http.StatusOK, gin.H{"items": out})
	})

	// LogHistory streams the persisted log JSONL straight from MinIO. Useful
	// for "scrub through old logs" after a task finishes; for live tail, use
	// the WS endpoint.
	g.GET("/tasks/:id/log", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		idx, err := d.Artifacts.GetLogIndex(c.Request.Context(), id)
		if err != nil || idx == nil {
			c.Status(http.StatusNoContent)
			return
		}
		body, err := d.Store.Download(c.Request.Context(), idx.LogKey)
		if err != nil {
			render.Error(c, http.StatusInternalServerError, "failed to fetch log")
			return
		}
		c.Header("Content-Type", "application/x-ndjson")
		_, _ = c.Writer.Write(body)
	})
}

// ----------------------------------------------------------------------------
// WebSocket: live log tail
// ----------------------------------------------------------------------------

// upgrader is module-level — gorilla/websocket reuses buffers internally.
// CheckOrigin returns true here since auth is enforced via ?token=… below.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// RegisterLogStream mounts GET /tasks/:id/log/stream on the public api group.
// The WS upgrade can't read an Authorization header from a browser, so the
// JWT comes in as a query param and is verified inside the handler.
func RegisterLogStream(g gin.IRoutes, d Deps, authSvc *authsvc.Service) {
	g.GET("/tasks/:id/log/stream", func(c *gin.Context) {
		token := c.Query("token")
		if token == "" {
			render.Error(c, http.StatusUnauthorized, "missing token")
			return
		}
		if _, err := authSvc.VerifyToken(token); err != nil {
			render.Error(c, http.StatusUnauthorized, "invalid token")
			return
		}
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}

		ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			d.Log.Warn("ws upgrade", "err", err)
			return
		}
		defer ws.Close()

		// Subscribe before sending historical catch-up so we don't miss lines
		// emitted during the catch-up.
		sub := d.Cache.Subscribe(c.Request.Context(), cache.LogChannel(id))
		defer sub.Close()

		// Catch-up: send whatever's already on disk so the user doesn't see a
		// blank screen on a running task.
		if hist, err := fetchHistorical(c.Request.Context(), d, id); err == nil && len(hist) > 0 {
			_ = ws.WriteMessage(websocket.TextMessage, hist)
		}

		// Reader goroutine: detect client disconnect or close frames.
		clientGone := make(chan struct{})
		go func() {
			defer close(clientGone)
			for {
				if _, _, err := ws.NextReader(); err != nil {
					return
				}
			}
		}()

		// Forward pubsub → ws.
		ch := sub.Channel()
		ping := time.NewTicker(30 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-c.Request.Context().Done():
				return
			case <-clientGone:
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := ws.WriteMessage(websocket.TextMessage, []byte(msg.Payload)); err != nil {
					return
				}
			case <-ping.C:
				_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := ws.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	})
}

func fetchHistorical(ctx context.Context, d Deps, taskID int64) ([]byte, error) {
	idx, err := d.Artifacts.GetLogIndex(ctx, taskID)
	if err != nil || idx == nil {
		return nil, err
	}
	body, err := d.Store.Download(ctx, idx.LogKey)
	if err != nil {
		return nil, err
	}
	// JSONL — caller forwards as one WS frame; the browser splits on newlines.
	if len(body) > 0 && body[len(body)-1] == '\n' {
		body = body[:len(body)-1]
	}
	return body, nil
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

func pathInt64(c *gin.Context, key string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(key), 10, 64)
	if err != nil || id <= 0 {
		render.Error(c, http.StatusBadRequest, "invalid "+key)
		return 0, false
	}
	return id, true
}
