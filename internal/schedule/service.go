// Package schedule owns cron-driven spider runs.
//
// Service is the persistence-facing layer (CRUD + validation). Runner
// (runner.go) is the long-lived goroutine that holds the in-process cron
// daemon and creates tasks when a schedule fires.
package schedule

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Schedule is the domain object. Args is stored as JSONB and forwarded to
// task.CreateInput.TriggeredArgs so spider authors can parameterise scheduled
// runs (e.g. {"region": "us-west"}).
type Schedule struct {
	ID          int64          `json:"id"`
	SpiderID    int64          `json:"spider_id"`
	Name        string         `json:"name"`
	CronExpr    string         `json:"cron_expr"`
	Args        map[string]any `json:"args"`
	Enabled     bool           `json:"enabled"`
	LastRunAt   *time.Time     `json:"last_run_at,omitempty"`
	LastTaskID  int64          `json:"last_task_id,omitempty"`
	NextRunAt   *time.Time     `json:"next_run_at,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type CreateInput struct {
	SpiderID  int64
	Name      string
	CronExpr  string
	Args      map[string]any
	Enabled   bool
	CreatedBy int64
}

type UpdateInput struct {
	Name     string
	CronExpr string
	Args     map[string]any
	Enabled  bool
}

// Repository is the slice of repository.ScheduleRepo we need.
type Repository interface {
	Insert(ctx context.Context, in CreateInput) (*Schedule, error)
	Get(ctx context.Context, id int64) (*Schedule, error)
	List(ctx context.Context) ([]*Schedule, error)
	ListEnabled(ctx context.Context) ([]*Schedule, error)
	Update(ctx context.Context, id int64, in UpdateInput) (*Schedule, error)
	Delete(ctx context.Context, id int64) error
	MarkRun(ctx context.Context, id int64, lastRunAt time.Time, lastTaskID int64, nextRunAt *time.Time) error
	UpdateNextRun(ctx context.Context, id int64, nextRunAt *time.Time) error
}

type Service struct {
	repo Repository
	log  *slog.Logger
}

func NewService(repo Repository, log *slog.Logger) *Service {
	return &Service{repo: repo, log: log}
}

var (
	ErrInvalidInput = errors.New("invalid input")
	ErrInvalidCron  = errors.New("invalid cron expression")
)

// parser is the standard 5-field cron parser, shared by Service (for input
// validation) and Runner (for next-run computation). Module-level because
// cron.Parser is safe for concurrent use.
var parser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
)

// ValidateCron reports whether the given expression parses. Surfaced for
// HTTP handlers that want to 400 before ever touching the DB.
func ValidateCron(expr string) error {
	if strings.TrimSpace(expr) == "" {
		return ErrInvalidCron
	}
	if _, err := parser.Parse(expr); err != nil {
		return ErrInvalidCron
	}
	return nil
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Schedule, error) {
	if in.SpiderID == 0 || strings.TrimSpace(in.Name) == "" {
		return nil, ErrInvalidInput
	}
	if err := ValidateCron(in.CronExpr); err != nil {
		return nil, err
	}
	return s.repo.Insert(ctx, in)
}

func (s *Service) Get(ctx context.Context, id int64) (*Schedule, error) {
	return s.repo.Get(ctx, id)
}

func (s *Service) List(ctx context.Context) ([]*Schedule, error) {
	return s.repo.List(ctx)
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (*Schedule, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, ErrInvalidInput
	}
	if err := ValidateCron(in.CronExpr); err != nil {
		return nil, err
	}
	return s.repo.Update(ctx, id, in)
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.repo.Delete(ctx, id)
}

// MarkRun is called by the runner after a successful Queue. The next-run
// time is recomputed by the runner (it already holds the parsed schedule)
// and passed in, so the service stays simple.
func (s *Service) MarkRun(ctx context.Context, id int64, lastRunAt time.Time, lastTaskID int64, nextRunAt *time.Time) error {
	return s.repo.MarkRun(ctx, id, lastRunAt, lastTaskID, nextRunAt)
}
