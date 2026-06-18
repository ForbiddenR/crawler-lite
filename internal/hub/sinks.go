// Package hub — sinks for streamed worker output.
//
// LogSink: every LogLine is published to a Redis pubsub channel so the
// browser WS can subscribe live. We also batch-flush log lines to MinIO
// every few seconds for the historical / "scrub through old logs" view.
//
// ItemSink: writes items to Postgres.
//
// ArtifactSink: writes screenshot / HAR metadata to Postgres. The bytes
// themselves are uploaded to MinIO by the worker (or the SDK) before the
// ArtifactRef frame is sent — the master never handles the binary.
package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/yourteam/crawler-lite/internal/artifacts"
	"github.com/yourteam/crawler-lite/internal/cache"
	"github.com/yourteam/crawler-lite/internal/items"
	pb "github.com/yourteam/crawler-lite/internal/pb/worker/v1"
	"github.com/yourteam/crawler-lite/internal/storage"
)

// ---------------------------------------------------------------------------
// LogSink
// ---------------------------------------------------------------------------

// LogSinkPubsub publishes log lines to Redis (live tail) and buffers them
// per-task to MinIO (historical). The MinIO flush is best-effort.
type LogSinkPubsub struct {
	cache *cache.Client
	store *storage.MinIOClient
	repo  ArtifactsLogIndex

	mu       sync.Mutex
	buffers  map[int64]*logBuffer
	flushDur time.Duration
}

type logBuffer struct {
	lines       [][]byte
	lvlCounts   map[string]int
	lastFlushed time.Time
}

// ArtifactsLogIndex is the slice of repository.ArtifactsRepo we need.
type ArtifactsLogIndex interface {
	UpsertLogIndex(ctx context.Context, in artifacts.LogIndexUpsert) error
}

func NewLogSink(cache *cache.Client, store *storage.MinIOClient, repo ArtifactsLogIndex) *LogSinkPubsub {
	s := &LogSinkPubsub{
		cache:    cache,
		store:    store,
		repo:     repo,
		buffers:  make(map[int64]*logBuffer),
		flushDur: 3 * time.Second,
	}
	return s
}

// Run is a background goroutine that periodically flushes buffered log lines
// to MinIO. Started by the master composition root alongside the dispatcher.
func (s *LogSinkPubsub) Run(ctx context.Context) error {
	tick := time.NewTicker(s.flushDur)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			s.flushAll(context.Background())
			return nil
		case <-tick.C:
			s.flushAll(ctx)
		}
	}
}

// Write implements LogSink.
func (s *LogSinkPubsub) Write(ctx context.Context, line *pb.LogLine) error {
	// Encode once for both pubsub and disk buffering. The on-wire format is
	// the same JSON object so the WS handler can pass through bytes.
	encoded, err := json.Marshal(map[string]any{
		"task_id": line.TaskId,
		"ts_ns":   line.TsNs,
		"level":   line.Level,
		"message": line.Message,
	})
	if err != nil {
		return err
	}

	// Live fan-out (best-effort; pubsub publish is fire-and-forget).
	if err := s.cache.Publish(ctx, cache.LogChannel(line.TaskId), encoded); err != nil {
		// Don't fail the sink on pubsub errors — we still want to buffer to disk.
		// The worker keeps streaming; subscribers will reconnect.
	}

	// Buffer for the periodic MinIO flush.
	s.mu.Lock()
	buf := s.buffers[line.TaskId]
	if buf == nil {
		buf = &logBuffer{lvlCounts: map[string]int{}}
		s.buffers[line.TaskId] = buf
	}
	buf.lines = append(buf.lines, encoded)
	if line.Level != "" {
		buf.lvlCounts[line.Level]++
	}
	s.mu.Unlock()
	return nil
}

func (s *LogSinkPubsub) flushAll(ctx context.Context) {
	s.mu.Lock()
	tasks := make([]int64, 0, len(s.buffers))
	for tid := range s.buffers {
		tasks = append(tasks, tid)
	}
	s.mu.Unlock()
	for _, tid := range tasks {
		s.flushTask(ctx, tid)
	}
}

func (s *LogSinkPubsub) flushTask(ctx context.Context, taskID int64) {
	s.mu.Lock()
	buf := s.buffers[taskID]
	if buf == nil || len(buf.lines) == 0 {
		s.mu.Unlock()
		return
	}
	lines := buf.lines
	lvlCounts := buf.lvlCounts
	buf.lines = nil
	buf.lvlCounts = map[string]int{}
	s.mu.Unlock()

	key := logKeyForTask(taskID)
	if err := s.store.AppendJSONL(ctx, key, lines); err != nil {
		// Put the lines back so we retry next tick. Unbounded growth is bounded
		// by the worker timeout — at v1 scale we won't accumulate >1MB before
		// MinIO comes back.
		s.mu.Lock()
		buf := s.buffers[taskID]
		if buf == nil {
			buf = &logBuffer{lvlCounts: lvlCounts}
			s.buffers[taskID] = buf
		} else {
			for k, v := range lvlCounts {
				buf.lvlCounts[k] += v
			}
		}
		buf.lines = append(lines, buf.lines...)
		s.mu.Unlock()
		return
	}

	// Update the index in Postgres so the UI can show counts even before the
	// log object is fully written.
	addBytes := int64(0)
	for _, l := range lines {
		addBytes += int64(len(l)) + 1
	}
	lvlJSON, _ := json.Marshal(lvlCounts)
	_ = s.repo.UpsertLogIndex(ctx, artifacts.LogIndexUpsert{
		TaskID:          taskID,
		LogKey:          key,
		AddBytes:        addBytes,
		AddLines:        len(lines),
		LevelCountsJSON: lvlJSON,
	})
}

func logKeyForTask(taskID int64) string {
	return fmt.Sprintf("tasks/%d/log.jsonl", taskID)
}

// ---------------------------------------------------------------------------
// ItemSink
// ---------------------------------------------------------------------------

type ItemRepo interface {
	Insert(ctx context.Context, in items.Insert) (int64, error)
}

type ItemSinkDB struct{ repo ItemRepo }

func NewItemSink(repo ItemRepo) *ItemSinkDB { return &ItemSinkDB{repo: repo} }

func (s *ItemSinkDB) Write(ctx context.Context, taskID, spiderID int64, payload []byte) error {
	_, err := s.repo.Insert(ctx, items.Insert{
		TaskID:      taskID,
		SpiderID:    spiderID,
		PayloadJSON: payload,
	})
	return err
}

// ---------------------------------------------------------------------------
// ArtifactSink
// ---------------------------------------------------------------------------

type ArtifactsRepo interface {
	InsertScreenshot(ctx context.Context, in artifacts.ScreenshotInsert) error
	UpsertHAR(ctx context.Context, in artifacts.HARInsert) error
}

type ArtifactSinkDB struct{ repo ArtifactsRepo }

func NewArtifactSink(repo ArtifactsRepo) *ArtifactSinkDB { return &ArtifactSinkDB{repo: repo} }

func (s *ArtifactSinkDB) Write(ctx context.Context, ref *pb.ArtifactRef) error {
	switch ref.Kind {
	case "screenshot":
		return s.repo.InsertScreenshot(ctx, artifacts.ScreenshotInsert{
			TaskID:     ref.TaskId,
			Name:       ref.Name,
			URL:        ref.Url,
			StorageKey: ref.StorageKey,
			Width:      int(ref.Width),
			Height:     int(ref.Height),
			Bytes:      int(ref.Bytes),
		})
	case "har":
		return s.repo.UpsertHAR(ctx, artifacts.HARInsert{
			TaskID:     ref.TaskId,
			StorageKey: ref.StorageKey,
			TotalBytes: int64(ref.Bytes),
		})
	default:
		return fmt.Errorf("unknown artifact kind: %s", ref.Kind)
	}
}
