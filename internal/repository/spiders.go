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

// ErrNotFound is returned when a SELECT by primary key returns 0 rows.
var ErrNotFound = errors.New("not found")

const spiderColumns = `id, project_id, name, description, status, entry_module,
                       source_key, source_version, config, created_by,
                       created_at, updated_at, deleted_at,
                       git_url, git_branch, last_synced_at, last_sync_commit, last_sync_error`

func (r *SpiderRepo) scanOne(row pgx.Row) (*spider.Spider, error) {
	var (
		s              spider.Spider
		description    *string
		sourceKey      *string
		configBytes    []byte
		createdBy      *int64
		createdAt      time.Time
		updatedAt      time.Time
		deletedAt      *time.Time
		gitURL         *string
		gitBranch      string
		lastSyncedAt   *time.Time
		lastSyncCommit *string
		lastSyncError  *string
	)
	err := row.Scan(
		&s.ID, &s.ProjectID, &s.Name, &description, &s.Status, &s.EntryModule,
		&sourceKey, &s.SourceVersion, &configBytes, &createdBy,
		&createdAt, &updatedAt, &deletedAt,
		&gitURL, &gitBranch, &lastSyncedAt, &lastSyncCommit, &lastSyncError,
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
	if gitURL != nil {
		s.GitURL = *gitURL
	}
	s.GitBranch = gitBranch
	s.LastSyncedAt = lastSyncedAt
	if lastSyncCommit != nil {
		s.LastSyncCommit = *lastSyncCommit
	}
	if lastSyncError != nil {
		s.LastSyncError = *lastSyncError
	}
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
	branch := in.GitBranch
	if branch == "" {
		branch = "main"
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO spiders (project_id, name, description, entry_module, config,
		                    created_by, git_url, git_branch)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8)
		RETURNING `+spiderColumns,
		in.ProjectID, in.Name, nullableStr(in.Description), in.EntryModule,
		configJSON, nullableInt64(in.CreatedBy), in.GitURL, branch,
	)
	return r.scanOne(row)
}

func (r *SpiderRepo) Update(ctx context.Context, id int64, in spider.UpdateInput) (*spider.Spider, error) {
	configJSON, err := json.Marshal(in.Config)
	if err != nil {
		return nil, err
	}
	branch := in.GitBranch
	if branch == "" {
		branch = "main"
	}
	row := r.pool.QueryRow(ctx, `
		UPDATE spiders
		SET name = $2, description = $3, entry_module = $4,
		    config = $5, status = $6,
		    git_url = NULLIF($7, ''), git_branch = $8,
		    updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING `+spiderColumns,
		id, in.Name, nullableStr(in.Description), in.EntryModule,
		configJSON, in.Status, in.GitURL, branch,
	)
	return r.scanOne(row)
}

func (r *SpiderRepo) SoftDelete(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE spiders SET deleted_at = now() WHERE id = $1`, id)
	return err
}

// MarkSynced updates source_key + bumps source_version to the new version,
// records the commit hash, and clears last_sync_error. Called from the
// SourceManager after a successful clone+upload.
func (r *SpiderRepo) MarkSynced(ctx context.Context, id int64, sourceKey, commit string, version int32) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE spiders
		SET source_key = $2,
		    source_version = $3,
		    last_synced_at = now(),
		    last_sync_commit = NULLIF($4, ''),
		    last_sync_error = NULL,
		    updated_at = now()
		WHERE id = $1
	`, id, sourceKey, version, commit)
	return err
}

// MarkSyncFailed records a sync failure without changing source_key, so the
// last good source remains usable.
func (r *SpiderRepo) MarkSyncFailed(ctx context.Context, id int64, errMsg string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE spiders
		SET last_sync_error = $2,
		    updated_at = now()
		WHERE id = $1
	`, id, errMsg)
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
