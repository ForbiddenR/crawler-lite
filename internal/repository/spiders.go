package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourteam/crawler-lite/internal/spider"
)

type SpiderRepo struct{ pool *pgxpool.Pool }

func NewSpiderRepo(pool *pgxpool.Pool) *SpiderRepo { return &SpiderRepo{pool: pool} }

// ErrNotFound is returned when a SELECT by primary key returns 0 rows. Defined
// here once so every repo agrees on the sentinel.
var ErrNotFound = errors.New("not found")

const spiderColumns = `id, project_id, name, description, status, entry_module,
                       source_key, source_version, config, created_by,
                       created_at, updated_at, deleted_at`

func (r *SpiderRepo) scanOne(row pgx.Row) (*spider.Spider, error) {
	var (
		s            spider.Spider
		description  *string
		sourceKey    *string
		configBytes  []byte
		createdBy    *int64
		createdAt    time.Time
		updatedAt    time.Time
		deletedAt    *time.Time
	)
	err := row.Scan(
		&s.ID, &s.ProjectID, &s.Name, &description, &s.Status, &s.EntryModule,
		&sourceKey, &s.SourceVersion, &configBytes, &createdBy,
		&createdAt, &updatedAt, &deletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if description != nil {
		s.Description = *description
	}
	if sourceKey != nil {
		s.SourceKey = *sourceKey
	}
	if len(configBytes) > 0 {
		_ = json.Unmarshal(configBytes, &s.Config)
	}
	s.CreatedAt = createdAt
	s.UpdatedAt = updatedAt
	return &s, nil
}

func (r *SpiderRepo) List(ctx context.Context) ([]*spider.Spider, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+spiderColumns+`
		FROM spiders
		WHERE deleted_at IS NULL
		ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*spider.Spider
	for rows.Next() {
		s, err := r.scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *SpiderRepo) Get(ctx context.Context, id int64) (*spider.Spider, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+spiderColumns+`
		FROM spiders
		WHERE id = $1 AND deleted_at IS NULL
	`, id)
	return r.scanOne(row)
}

func (r *SpiderRepo) Create(ctx context.Context, in spider.CreateInput) (*spider.Spider, error) {
	configJSON, err := json.Marshal(in.Config)
	if err != nil {
		return nil, err
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO spiders (project_id, name, description, entry_module, config, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+spiderColumns,
		in.ProjectID, in.Name, nullableStr(in.Description), in.EntryModule,
		configJSON, nullableInt64(in.CreatedBy),
	)
	return r.scanOne(row)
}

func (r *SpiderRepo) Update(ctx context.Context, id int64, in spider.UpdateInput) (*spider.Spider, error) {
	configJSON, err := json.Marshal(in.Config)
	if err != nil {
		return nil, err
	}
	row := r.pool.QueryRow(ctx, `
		UPDATE spiders
		SET name = $2, description = $3, entry_module = $4,
		    config = $5, status = $6, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING `+spiderColumns,
		id, in.Name, nullableStr(in.Description), in.EntryModule,
		configJSON, in.Status,
	)
	return r.scanOne(row)
}

func (r *SpiderRepo) SoftDelete(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE spiders SET deleted_at = now() WHERE id = $1`, id)
	return err
}

func nullableStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullableInt64(i int64) *int64 {
	if i == 0 {
		return nil
	}
	return &i
}
