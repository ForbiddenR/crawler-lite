package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourteam/crawler-lite/internal/schedule"
)

type ScheduleRepo struct{ pool *pgxpool.Pool }

func NewScheduleRepo(pool *pgxpool.Pool) *ScheduleRepo { return &ScheduleRepo{pool: pool} }

const scheduleColumns = `id, spider_id, name, cron_expr, args, enabled,
                         last_run_at, last_task_id, next_run_at,
                         created_at, updated_at`

func (r *ScheduleRepo) scanOne(row pgx.Row) (*schedule.Schedule, error) {
	var (
		s          schedule.Schedule
		argsBytes  []byte
		lastRunAt  *time.Time
		lastTaskID *int64
		nextRunAt  *time.Time
	)
	err := row.Scan(
		&s.ID, &s.SpiderID, &s.Name, &s.CronExpr, &argsBytes, &s.Enabled,
		&lastRunAt, &lastTaskID, &nextRunAt,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(argsBytes) > 0 {
		_ = json.Unmarshal(argsBytes, &s.Args)
	}
	s.LastRunAt = lastRunAt
	if lastTaskID != nil {
		s.LastTaskID = *lastTaskID
	}
	s.NextRunAt = nextRunAt
	return &s, nil
}

func (r *ScheduleRepo) Insert(ctx context.Context, in schedule.CreateInput) (*schedule.Schedule, error) {
	argsJSON, err := json.Marshal(in.Args)
	if err != nil {
		return nil, err
	}
	if len(argsJSON) == 0 || string(argsJSON) == "null" {
		argsJSON = []byte("{}")
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO schedules (spider_id, name, cron_expr, args, enabled, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+scheduleColumns,
		in.SpiderID, in.Name, in.CronExpr, argsJSON, in.Enabled,
		nullableInt64(in.CreatedBy),
	)
	return r.scanOne(row)
}

func (r *ScheduleRepo) Get(ctx context.Context, id int64) (*schedule.Schedule, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+scheduleColumns+`
		FROM schedules WHERE id = $1
	`, id)
	return r.scanOne(row)
}

func (r *ScheduleRepo) List(ctx context.Context) ([]*schedule.Schedule, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+scheduleColumns+`
		FROM schedules
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*schedule.Schedule
	for rows.Next() {
		s, err := r.scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListEnabled is the runner's hot path. The partial index
// (idx_schedules_enabled WHERE enabled) makes this trivial.
func (r *ScheduleRepo) ListEnabled(ctx context.Context) ([]*schedule.Schedule, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+scheduleColumns+`
		FROM schedules
		WHERE enabled
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*schedule.Schedule
	for rows.Next() {
		s, err := r.scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *ScheduleRepo) Update(ctx context.Context, id int64, in schedule.UpdateInput) (*schedule.Schedule, error) {
	argsJSON, err := json.Marshal(in.Args)
	if err != nil {
		return nil, err
	}
	if len(argsJSON) == 0 || string(argsJSON) == "null" {
		argsJSON = []byte("{}")
	}
	row := r.pool.QueryRow(ctx, `
		UPDATE schedules
		SET name = $2, cron_expr = $3, args = $4, enabled = $5,
		    updated_at = now()
		WHERE id = $1
		RETURNING `+scheduleColumns,
		id, in.Name, in.CronExpr, argsJSON, in.Enabled,
	)
	return r.scanOne(row)
}

func (r *ScheduleRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM schedules WHERE id = $1`, id)
	return err
}

// MarkRun is called by the runner after a successful Queue. nextRunAt is
// computed by the runner (it already holds the parsed cron schedule).
func (r *ScheduleRepo) MarkRun(ctx context.Context, id int64, lastRunAt time.Time, lastTaskID int64, nextRunAt *time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE schedules
		SET last_run_at = $2,
		    last_task_id = $3,
		    next_run_at = $4,
		    updated_at = now()
		WHERE id = $1
	`, id, lastRunAt, lastTaskID, nextRunAt)
	return err
}

// UpdateNextRun is called by the runner during Reload to refresh the
// projected next-run timestamp without touching last_run_at / last_task_id.
func (r *ScheduleRepo) UpdateNextRun(ctx context.Context, id int64, nextRunAt *time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE schedules
		SET next_run_at = $2
		WHERE id = $1
	`, id, nextRunAt)
	return err
}
