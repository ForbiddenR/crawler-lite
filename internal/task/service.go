// Package task owns the TaskService — task creation, listing, status
// transitions, and (in future weeks) the dispatch loop that hands tasks to
// workers via the WorkerHub.
package task

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Status mirrors the `task_status` enum in Postgres.
type Status string

const (
	StatusQueued         Status = "queued"
	StatusRunning        Status = "running"
	StatusSucceeded      Status = "succeeded"
	StatusFailed         Status = "failed"
	StatusCancelled      Status = "cancelled"
	StatusTimeout        Status = "timeout"
	StatusCaptchaBlocked Status = "captcha_blocked"
)

// Trigger mirrors the `task_trigger` enum.
type Trigger string

const (
	TriggerManual   Trigger = "manual"
	TriggerSchedule Trigger = "schedule"
	TriggerRetry    Trigger = "retry"
	TriggerAPI      Trigger = "api"
)

type Task struct {
	ID            int64          `json:"id"`
	SpiderID      int64          `json:"spider_id"`
	ParentTaskID  int64          `json:"parent_task_id,omitempty"`
	Trigger       Trigger        `json:"trigger"`
	Status        Status         `json:"status"`
	SpiderVersion int32          `json:"spider_version"`
	WorkerID      string         `json:"worker_id,omitempty"`
	QueuedAt      time.Time      `json:"queued_at"`
	StartedAt     *time.Time     `json:"started_at,omitempty"`
	FinishedAt    *time.Time     `json:"finished_at,omitempty"`
	Error         string         `json:"error,omitempty"`
	ErrorClass    string         `json:"error_class,omitempty"`
	Stats         map[string]any `json:"stats"`
	TriggeredArgs map[string]any `json:"triggered_args,omitempty"`
}

type CreateInput struct {
	SpiderID      int64
	Trigger       Trigger
	SpiderVersion int32
	TriggeredArgs map[string]any
	CreatedBy     int64
}

// Repository is what the service needs from the persistence layer.
type Repository interface {
	Create(ctx context.Context, in CreateInput) (*Task, error)
	Get(ctx context.Context, id int64) (*Task, error)
	List(ctx context.Context, limit, offset int) ([]*Task, error)
	ListQueued(ctx context.Context) ([]*Task, error)
	SetStatus(ctx context.Context, id int64, status Status, errMsg, errClass string, workerID string) error
}

// Hub is the slice of hub.WorkerHub we need to dispatch tasks. Defined as an
// interface to avoid an import cycle and to make the dispatch loop testable.
type Hub interface {
	// Assign attempts to dispatch a task to an available worker. Returns true
	// if accepted, false if no worker has free capacity (caller should keep
	// the task queued).
	Assign(ctx context.Context, t *Task) (bool, error)

	// CancelRunning best-effort signals a running worker to abort. Safe to call
	// for tasks that aren't running anywhere.
	CancelRunning(ctx context.Context, taskID int64) error
}

type Service struct {
	repo Repository
	hub  Hub
	log  *slog.Logger
}

// NewService takes the things it needs. The dispatch loop and spider lookup
// arrive in week 2; the placeholder _spiders parameter from the earlier draft
// has been removed.
func NewService(repo Repository, _ any, hub Hub, log *slog.Logger) *Service {
	return &Service{repo: repo, hub: hub, log: log}
}

var ErrInvalidInput = errors.New("invalid input")

// Queue creates a task in `queued` state. The dispatch loop (week 2) picks it
// up and asks the hub to assign.
func (s *Service) Queue(ctx context.Context, in CreateInput) (*Task, error) {
	if in.SpiderID == 0 {
		return nil, ErrInvalidInput
	}
	if in.Trigger == "" {
		in.Trigger = TriggerManual
	}
	if in.SpiderVersion == 0 {
		in.SpiderVersion = 1
	}
	return s.repo.Create(ctx, in)
}

func (s *Service) Get(ctx context.Context, id int64) (*Task, error) {
	return s.repo.Get(ctx, id)
}

func (s *Service) List(ctx context.Context, limit, offset int) ([]*Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.List(ctx, limit, offset)
}

// Cancel sets status to cancelled in the DB and signals the worker (if any).
// Best-effort — the worker may have already finished by the time the signal
// reaches it.
func (s *Service) Cancel(ctx context.Context, id int64) error {
	if err := s.repo.SetStatus(ctx, id, StatusCancelled, "cancelled by user", "", ""); err != nil {
		return err
	}
	return s.hub.CancelRunning(ctx, id)
}

// OnUpdate is called by the WorkerHub when it receives a TaskUpdate frame
// from a worker. It just persists the new status; richer routing arrives later.
func (s *Service) OnUpdate(ctx context.Context, taskID int64, status Status, errMsg, errClass, workerID string) error {
	return s.repo.SetStatus(ctx, taskID, status, errMsg, errClass, workerID)
}
