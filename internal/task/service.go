// Package task owns the TaskService: queueing, listing, status transitions,
// cancel, and the dispatch loop that hands queued tasks to workers via the
// WorkerHub.
package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	pb "github.com/yourteam/crawler-lite/internal/pb/worker/v1"
	"github.com/yourteam/crawler-lite/internal/spider"
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
	Attempt       int32          `json:"attempt"`
	NotBefore     *time.Time     `json:"not_before,omitempty"`
}

type CreateInput struct {
	SpiderID      int64
	Trigger       Trigger
	SpiderVersion int32
	TriggeredArgs map[string]any
	CreatedBy     int64
	ParentTaskID  int64
	Attempt       int32
	NotBefore     *time.Time
}

// Repository is what the service needs from the persistence layer.
type Repository interface {
	Create(ctx context.Context, in CreateInput) (*Task, error)
	Get(ctx context.Context, id int64) (*Task, error)
	List(ctx context.Context, limit, offset int) ([]*Task, error)
	ListQueued(ctx context.Context) ([]*Task, error)
	SetStatus(ctx context.Context, id int64, status Status, errMsg, errClass string, workerID string) error
}

// SpiderLookup is the slice of spider.Repository the dispatch loop needs to
// build a complete pb.AssignTask. It's defined here, in the consumer.
type SpiderLookup interface {
	Get(ctx context.Context, id int64) (*spider.Spider, error)
}

// Hub is the slice of hub.WorkerHub we need to dispatch tasks. Defined as an
// interface to avoid an import cycle and to make the dispatch loop testable.
//
// Assign takes a fully-built pb.AssignTask: the task package owns the
// translation from domain Task + Spider into the wire message.
type Hub interface {
	Assign(ctx context.Context, a *pb.AssignTask) (bool, error)
	CancelRunning(ctx context.Context, taskID int64) error
}

// Deps groups the dispatch-loop dependencies. A struct keeps the constructor
// readable as the list grows.
type Deps struct {
	Repo    Repository
	Spiders SpiderLookup
	Hub     Hub
	Log     *slog.Logger

	// Default per-task timeout when the spider config doesn't specify one.
	DefaultTimeoutSeconds int32
}

type Service struct {
	deps Deps

	// wakeup is signalled by Queue() and ticked by RunDispatcher() so newly
	// queued tasks dispatch immediately instead of waiting for the next poll.
	wakeup chan struct{}
}

func NewService(d Deps) *Service {
	if d.DefaultTimeoutSeconds == 0 {
		d.DefaultTimeoutSeconds = 600
	}
	return &Service{
		deps:   d,
		wakeup: make(chan struct{}, 1),
	}
}

var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNoSource     = errors.New("spider has no synced source")
)

// Queue creates a task in `queued` state and pokes the dispatch loop.
func (s *Service) Queue(ctx context.Context, in CreateInput) (*Task, error) {
	if in.SpiderID == 0 {
		return nil, ErrInvalidInput
	}
	if in.Trigger == "" {
		in.Trigger = TriggerManual
	}
	// SpiderVersion defaults to whatever the spider currently has.
	if in.SpiderVersion == 0 {
		sp, err := s.deps.Spiders.Get(ctx, in.SpiderID)
		if err != nil {
			return nil, fmt.Errorf("lookup spider: %w", err)
		}
		if sp.SourceVersion == 0 {
			return nil, ErrNoSource
		}
		in.SpiderVersion = sp.SourceVersion
	}
	t, err := s.deps.Repo.Create(ctx, in)
	if err != nil {
		return nil, err
	}
	s.notify()
	return t, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Task, error) {
	return s.deps.Repo.Get(ctx, id)
}

func (s *Service) List(ctx context.Context, limit, offset int) ([]*Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return s.deps.Repo.List(ctx, limit, offset)
}

// Cancel sets status to cancelled in the DB and signals the worker (if any).
func (s *Service) Cancel(ctx context.Context, id int64) error {
	if err := s.deps.Repo.SetStatus(ctx, id, StatusCancelled, "cancelled by user", "", ""); err != nil {
		return err
	}
	return s.deps.Hub.CancelRunning(ctx, id)
}

// OnUpdate is called by the WorkerHub when it receives a TaskUpdate frame.
//
// After persisting the status, if the task landed in a retryable terminal
// state (failed / timeout) and the spider's policy allows another attempt,
// schedule a child task. The dispatcher's existing wakeup + 5s tick handles
// firing it once not_before has passed.
func (s *Service) OnUpdate(ctx context.Context, taskID int64, status Status, errMsg, errClass, workerID string) error {
	if err := s.deps.Repo.SetStatus(ctx, taskID, status, errMsg, errClass, workerID); err != nil {
		return err
	}
	if status != StatusFailed && status != StatusTimeout {
		return nil
	}
	s.maybeScheduleRetry(ctx, taskID, errClass)
	return nil
}

// maybeScheduleRetry reads the parent task + spider policy, decides, and on
// "yes" inserts a child task. Errors are logged but never propagated — a
// failure to schedule a retry must not roll back the parent's terminal
// status update.
func (s *Service) maybeScheduleRetry(ctx context.Context, parentID int64, errClass string) {
	parent, err := s.deps.Repo.Get(ctx, parentID)
	if err != nil {
		s.deps.Log.Warn("retry: lookup parent", "task", parentID, "err", err)
		return
	}
	sp, err := s.deps.Spiders.Get(ctx, parent.SpiderID)
	if err != nil {
		s.deps.Log.Warn("retry: lookup spider", "task", parentID, "err", err)
		return
	}
	policy := PolicyFromSpiderConfig(sp.Config)
	attempt := int(parent.Attempt)
	if attempt < 1 {
		attempt = 1
	}
	retry, delay := policy.Decide(attempt, errClass)
	if !retry {
		return
	}
	notBefore := time.Now().Add(delay)
	child, err := s.deps.Repo.Create(ctx, CreateInput{
		SpiderID:      parent.SpiderID,
		Trigger:       TriggerRetry,
		SpiderVersion: parent.SpiderVersion,
		TriggeredArgs: parent.TriggeredArgs,
		ParentTaskID:  parent.ID,
		Attempt:       int32(attempt + 1),
		NotBefore:     &notBefore,
	})
	if err != nil {
		s.deps.Log.Error("retry: create child", "parent", parentID, "err", err)
		return
	}
	s.deps.Log.Info("retry: scheduled",
		"parent", parentID, "child", child.ID,
		"attempt", child.Attempt, "not_before", notBefore.Format(time.RFC3339),
		"err_class", errClass)
	s.notify()
}

// notify wakes the dispatch loop. Non-blocking: if a wakeup is already
// queued, this is a no-op.
func (s *Service) notify() {
	select {
	case s.wakeup <- struct{}{}:
	default:
	}
}

// RunDispatcher is the long-lived goroutine that consumes queued tasks and
// asks the hub to assign them. It wakes on:
//   - explicit notify() from Queue
//   - a 5s safety tick (catches updates that didn't go through Queue, e.g.
//     a worker disconnect that re-queued tasks)
//
// Returns when ctx is cancelled.
func (s *Service) RunDispatcher(ctx context.Context) error {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		s.dispatchOnce(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-s.wakeup:
		case <-tick.C:
		}
	}
}

func (s *Service) dispatchOnce(ctx context.Context) {
	queued, err := s.deps.Repo.ListQueued(ctx)
	if err != nil {
		s.deps.Log.Error("dispatch: list queued", "err", err)
		return
	}
	for _, t := range queued {
		assign, err := s.buildAssign(ctx, t)
		if err != nil {
			s.deps.Log.Warn("dispatch: build assign",
				"task", t.ID, "err", err)
			// Surface the error on the task so the UI shows it.
			_ = s.deps.Repo.SetStatus(ctx, t.ID, StatusFailed, err.Error(), "build_assign", "")
			// Treat this the same as a worker-side failure for retry purposes:
			// the spider author may have opted "build_assign" into retry_on,
			// in which case a sync between attempts could resolve it.
			s.maybeScheduleRetry(ctx, t.ID, "build_assign")
			continue
		}
		ok, err := s.deps.Hub.Assign(ctx, assign)
		if err != nil {
			s.deps.Log.Error("dispatch: hub assign", "task", t.ID, "err", err)
			continue
		}
		if !ok {
			// All workers busy; leave the task queued and try next tick.
			break
		}
		// The hub already pushed the message; the worker will report ACCEPTED →
		// RUNNING which is what flips the row to running.
	}
}

// buildAssign turns a queued Task + its Spider into the pb.AssignTask the hub
// pushes to a worker.
func (s *Service) buildAssign(ctx context.Context, t *Task) (*pb.AssignTask, error) {
	sp, err := s.deps.Spiders.Get(ctx, t.SpiderID)
	if err != nil {
		return nil, fmt.Errorf("lookup spider: %w", err)
	}
	if sp.SourceKey == "" {
		return nil, ErrNoSource
	}

	configJSON, err := json.Marshal(map[string]any{
		"entry_module": sp.EntryModule,
		"config":       sp.Config,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}

	argsJSON := []byte(`{}`)
	if t.TriggeredArgs != nil {
		argsJSON, err = json.Marshal(t.TriggeredArgs)
		if err != nil {
			return nil, fmt.Errorf("marshal args: %w", err)
		}
	}

	timeout := s.deps.DefaultTimeoutSeconds
	if v, ok := sp.Config["timeout_s"].(float64); ok && v > 0 {
		timeout = int32(v)
	}

	return &pb.AssignTask{
		TaskId:        t.ID,
		SpiderId:      t.SpiderID,
		SpiderVersion: t.SpiderVersion,
		SourceKey:     sp.SourceKey,
		ConfigJson:    configJSON,
		ArgsJson:      argsJSON,
		ProxyUrl:      "", // proxies in week 3
		TimeoutS:      timeout,
	}, nil
}
