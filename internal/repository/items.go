package repository

import (
	"context"
	"crypto/sha256"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourteam/crawler-lite/internal/items"
)

// ItemRepo persists items emitted by spider runs.
//
// Items are append-only and their `payload` is opaque JSON. We compute a
// sha256 of the canonical JSON on insert so spiders can opt into dedup with a
// `UNIQUE` index on (spider_id, payload_hash) without exposing the hash to
// users.
type ItemRepo struct{ pool *pgxpool.Pool }

func NewItemRepo(pool *pgxpool.Pool) *ItemRepo { return &ItemRepo{pool: pool} }

func (r *ItemRepo) Insert(ctx context.Context, in items.Insert) (int64, error) {
	hash := sha256.Sum256(in.PayloadJSON)
	var id int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO items (task_id, spider_id, payload, payload_hash)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, in.TaskID, in.SpiderID, in.PayloadJSON, hash[:]).Scan(&id)
	return id, err
}

func (r *ItemRepo) ListByTask(ctx context.Context, taskID int64, limit, offset int) ([]*items.Item, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, task_id, spider_id, payload, created_at
		FROM items
		WHERE task_id = $1
		ORDER BY id
		LIMIT $2 OFFSET $3
	`, taskID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*items.Item
	for rows.Next() {
		var it items.Item
		var raw []byte
		if err := rows.Scan(&it.ID, &it.TaskID, &it.SpiderID, &raw, &it.CreatedAt); err != nil {
			return nil, err
		}
		// Round-trip through json.RawMessage so the API serializes the payload
		// without re-marshaling.
		it.Payload = json.RawMessage(raw)
		out = append(out, &it)
	}
	return out, rows.Err()
}

func (r *ItemRepo) CountByTask(ctx context.Context, taskID int64) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM items WHERE task_id = $1`, taskID,
	).Scan(&n)
	return n, err
}
