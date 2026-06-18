package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourteam/crawler-lite/internal/artifacts"
)

// ArtifactsRepo bundles the screenshot, HAR, and log-index tables. They share
// no joins but they're conceptually the same group of "task output artifacts",
// so we keep one repo so the composition root has fewer dependencies to wire.
type ArtifactsRepo struct{ pool *pgxpool.Pool }

func NewArtifactsRepo(pool *pgxpool.Pool) *ArtifactsRepo { return &ArtifactsRepo{pool: pool} }

// ---- screenshots ----------------------------------------------------------

func (r *ArtifactsRepo) InsertScreenshot(ctx context.Context, in artifacts.ScreenshotInsert) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO task_screenshots (task_id, name, url, storage_key, width, height, bytes)
		VALUES ($1, $2, NULLIF($3, ''), $4, NULLIF($5, 0), NULLIF($6, 0), $7)
	`, in.TaskID, in.Name, in.URL, in.StorageKey, in.Width, in.Height, in.Bytes)
	return err
}

func (r *ArtifactsRepo) ListScreenshots(ctx context.Context, taskID int64) ([]*artifacts.Screenshot, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, task_id, taken_at, name, COALESCE(url, ''), storage_key,
		       COALESCE(width, 0), COALESCE(height, 0), bytes
		FROM task_screenshots
		WHERE task_id = $1
		ORDER BY taken_at
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*artifacts.Screenshot
	for rows.Next() {
		var s artifacts.Screenshot
		if err := rows.Scan(
			&s.ID, &s.TaskID, &s.TakenAt, &s.Name, &s.URL, &s.StorageKey,
			&s.Width, &s.Height, &s.Bytes,
		); err != nil {
			return nil, err
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// ---- HAR ------------------------------------------------------------------

func (r *ArtifactsRepo) UpsertHAR(ctx context.Context, in artifacts.HARInsert) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO task_har (task_id, storage_key, request_count, total_bytes)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (task_id) DO UPDATE
		SET storage_key = EXCLUDED.storage_key,
		    request_count = EXCLUDED.request_count,
		    total_bytes = EXCLUDED.total_bytes
	`, in.TaskID, in.StorageKey, in.RequestCount, in.TotalBytes)
	return err
}

// ---- log index ------------------------------------------------------------

// UpsertLogIndex bumps the line count and per-level totals. Called from the
// LogSink as lines stream in. Keeping the upsert atomic lets the WS handler
// poll for "is there anything yet?" without races.
func (r *ArtifactsRepo) UpsertLogIndex(ctx context.Context, in artifacts.LogIndexUpsert) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO task_log_index (task_id, log_key, bytes, line_count, level_counts, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (task_id) DO UPDATE
		SET log_key = EXCLUDED.log_key,
		    bytes = task_log_index.bytes + EXCLUDED.bytes,
		    line_count = task_log_index.line_count + EXCLUDED.line_count,
		    level_counts = task_log_index.level_counts || EXCLUDED.level_counts,
		    updated_at = EXCLUDED.updated_at
	`, in.TaskID, in.LogKey, in.AddBytes, in.AddLines, in.LevelCountsJSON, time.Now())
	return err
}

func (r *ArtifactsRepo) GetLogIndex(ctx context.Context, taskID int64) (*artifacts.LogIndex, error) {
	var li artifacts.LogIndex
	var lc []byte
	err := r.pool.QueryRow(ctx, `
		SELECT task_id, log_key, bytes, line_count, level_counts, updated_at
		FROM task_log_index
		WHERE task_id = $1
	`, taskID).Scan(&li.TaskID, &li.LogKey, &li.Bytes, &li.LineCount, &lc, &li.UpdatedAt)
	if err != nil {
		return nil, err
	}
	li.LevelCountsJSON = lc
	return &li, nil
}
