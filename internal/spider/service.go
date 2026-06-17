// Package spider exposes the SpiderService used by HTTP handlers.
//
// Domain types live here and are returned from the service. The repository
// layer scans rows into these types so HTTP handlers and tests don't need to
// know about pgx scan tags.
package spider

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Status maps to the `spider_status` enum in Postgres.
type Status string

const (
	StatusActive   Status = "active"
	StatusPaused   Status = "paused"
	StatusArchived Status = "archived"
)

// Spider is the domain object. Config is stored as JSONB; for week 1 we keep
// it as a free-form map. A typed Config struct lands in week 2.
type Spider struct {
	ID            int64                  `json:"id"`
	ProjectID     int64                  `json:"project_id"`
	Name          string                 `json:"name"`
	Description   string                 `json:"description,omitempty"`
	Status        Status                 `json:"status"`
	EntryModule   string                 `json:"entry_module"`
	SourceKey     string                 `json:"source_key,omitempty"`
	SourceVersion int32                  `json:"source_version"`
	Config        map[string]any         `json:"config"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
}

type CreateInput struct {
	ProjectID   int64
	Name        string
	Description string
	EntryModule string
	Config      map[string]any
	CreatedBy   int64
}

type UpdateInput struct {
	Name        string
	Description string
	EntryModule string
	Config      map[string]any
	Status      Status
}

// Repository is what the service needs from the persistence layer. Defined
// here because the consumer should declare its needs.
type Repository interface {
	List(ctx context.Context) ([]*Spider, error)
	Get(ctx context.Context, id int64) (*Spider, error)
	Create(ctx context.Context, in CreateInput) (*Spider, error)
	Update(ctx context.Context, id int64, in UpdateInput) (*Spider, error)
	SoftDelete(ctx context.Context, id int64) error
}

// StorageClient is the slice of storage.MinIOClient we need.
type StorageClient interface {
	Bucket() string
}

type Service struct {
	repo  Repository
	store StorageClient
	log   *slog.Logger
}

func NewService(repo Repository, store StorageClient, log *slog.Logger) *Service {
	return &Service{repo: repo, store: store, log: log}
}

var ErrInvalidInput = errors.New("invalid input")

func (s *Service) List(ctx context.Context) ([]*Spider, error) {
	return s.repo.List(ctx)
}

func (s *Service) Get(ctx context.Context, id int64) (*Spider, error) {
	return s.repo.Get(ctx, id)
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Spider, error) {
	if in.Name == "" || in.EntryModule == "" {
		return nil, ErrInvalidInput
	}
	if in.ProjectID == 0 {
		in.ProjectID = 1 // default project, seeded by migration 00002
	}
	if in.Config == nil {
		in.Config = map[string]any{}
	}
	return s.repo.Create(ctx, in)
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (*Spider, error) {
	if in.Status == "" {
		in.Status = StatusActive
	}
	if in.Config == nil {
		in.Config = map[string]any{}
	}
	return s.repo.Update(ctx, id, in)
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.repo.SoftDelete(ctx, id)
}
