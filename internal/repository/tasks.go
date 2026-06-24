package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourteam/crawler-lite/internal/task"
)

type TaskRepo struct{ pool *pgxpool.Pool }

func NewTaskRepo(pool *pgxpool.Pool) *TaskRepo { return &TaskRepo{pool: pool} }

const taskColumns = `id, spider_id, parent_task_id, trigger, status,
                     spider_version, worker_id, queued_at, started_at,
                     finished_at, exit_code, error, error_class, stats,
                     created_by, triggered_args, attempt, not_before`

func (r *TaskRepo) scanOne(row pgx.Row) (*task.Task, error) {
	var (
		t              task.Task
		parentID       *int64
		workerID       *string
		startedAt      *time.Time
		finishedAt     *time.Time
		exitCode       *int32
		errStr         *string
		errClass       *string
		statsBytes     []byte
		createdBy      *int64
		triggeredBytes []byte
		notBefore      *time.Time
	)
	err := row.Scan(
		&t.ID, &t.SpiderID, &parentID, &t.Trigger, &t.Status,
		&t.SpiderVersion, &workerID, &t.QueuedAt, &startedAt,
		&finishedAt, &exitCode, &errStr, &errClass, &statsBytes,
		&createdBy, &triggeredBytes, &t.Attempt, &notBefore,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if parentID != nil {
		t.ParentTaskID = *parentID
	}
	if workerID != nil {
		t.WorkerID = *workerID
	}
	t.StartedAt = startedAt
	t.FinishedAt = finishedAt
	if errStr != nil {
		t.Error = *errStr
	}
	if errClass != nil {
		t.ErrorClass = *errClass
	}
	if len(statsBytes) > 0 {
		_ = json.Unmarshal(statsBytes, &t.Stats)
	}
	if len(triggeredBytes) > 0 {
		_ = json.Unmarshal(triggeredBytes, &t.TriggeredArgs)
	}
	t.NotBefore = notBefore
	return &t, nil
}

func (r *TaskRepo) Create(ctx context.Context, in task.CreateInput) (*task.Task, error) {
	argsJSON, err := json.Marshal(in.TriggeredArgs)
	if err != nil {
		return nil, err
	}
	attempt := max(in.Attempt, 1)
	row := r.pool.QueryRow(ctx, `
		INSERT INTO tasks (spider_id, parent_task_id, trigger, spider_version,
		                   triggered_args, created_by, attempt, not_before)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+taskColumns,
		in.SpiderID, nullableInt64(in.ParentTaskID), in.Trigger, in.SpiderVersion, argsJSON,
		nullableInt64(in.CreatedBy), attempt, in.NotBefore,
	)
	return r.scanOne(row)
}

func (r *TaskRepo) Get(ctx context.Context, id int64) (*task.Task, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+taskColumns+`
		FROM tasks WHERE id = $1
	`, id)
	return r.scanOne(row)
}

func (r *TaskRepo) List(ctx context.Context, limit, offset int) ([]*task.Task, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+taskColumns+`
		FROM tasks
		ORDER BY queued_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*task.Task
	for rows.Next() {
		t, err := r.scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *TaskRepo) ListQueued(ctx context.Context) ([]*task.Task, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+taskColumns+`
		FROM tasks
		WHERE status = 'queued'
		  AND (not_before IS NULL OR not_before <= now())
		ORDER BY queued_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*task.Task
	for rows.Next() {
		t, err := r.scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetStatus transitions a task to the given status. The CASE in SQL sets
// started_at / finished_at automatically based on the transition.
func (r *TaskRepo) SetStatus(ctx context.Context, id int64, status task.Status, errMsg, errClass string, workerID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE tasks
		SET status = $2::task_status,
		    error = NULLIF($3, ''),
		    error_class = NULLIF($4, ''),
		    worker_id = COALESCE(NULLIF($5, ''), worker_id),
		    started_at = CASE WHEN $2::task_status = 'running' AND started_at IS NULL THEN now() ELSE started_at END,
		    finished_at = CASE WHEN $2::task_status IN ('succeeded','failed','cancelled','timeout','captcha_blocked')
		                      THEN now() ELSE finished_at END
		WHERE id = $1
	`, id, status, errMsg, errClass, workerID)
	return err
}
