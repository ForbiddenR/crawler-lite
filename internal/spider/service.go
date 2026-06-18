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

// Spider is the domain object. Config is stored as JSONB; for now we keep it
// as a free-form map. A typed Config struct can land later when more of the
// fields are stable.
type Spider struct {
	ID            int64          `json:"id"`
	ProjectID     int64          `json:"project_id"`
	Name          string         `json:"name"`
	Description   string         `json:"description,omitempty"`
	Status        Status         `json:"status"`
	EntryModule   string         `json:"entry_module"`
	SourceKey     string         `json:"source_key,omitempty"`
	SourceVersion int32          `json:"source_version"`
	Config        map[string]any `json:"config"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`

	// Git source distribution (week 2). Master clones GitURL@GitBranch on
	// Sync, zips the working copy, and uploads to MinIO.
	GitURL         string     `json:"git_url,omitempty"`
	GitBranch      string     `json:"git_branch,omitempty"`
	LastSyncedAt   *time.Time `json:"last_synced_at,omitempty"`
	LastSyncCommit string     `json:"last_sync_commit,omitempty"`
	LastSyncError  string     `json:"last_sync_error,omitempty"`
}

type CreateInput struct {
	ProjectID   int64
	Name        string
	Description string
	EntryModule string
	Config      map[string]any
	CreatedBy   int64
	GitURL      string
	GitBranch   string
}

type UpdateInput struct {
	Name        string
	Description string
	EntryModule string
	Config      map[string]any
	Status      Status
	GitURL      string
	GitBranch   string
}

// Repository is what the service needs from the persistence layer. Defined
// here because the consumer should declare its needs.
type Repository interface {
	List(ctx context.Context) ([]*Spider, error)
	Get(ctx context.Context, id int64) (*Spider, error)
	Create(ctx context.Context, in CreateInput) (*Spider, error)
	Update(ctx context.Context, id int64, in UpdateInput) (*Spider, error)
	SoftDelete(ctx context.Context, id int64) error

	// Sync bookkeeping
	MarkSynced(ctx context.Context, id int64, sourceKey, commit string, version int32) error
	MarkSyncFailed(ctx context.Context, id int64, errMsg string) error
}

// StorageClient is the slice of storage.MinIOClient we need.
type StorageClient interface {
	Bucket() string
	Upload(ctx context.Context, key string, data []byte, contentType string) error
	Download(ctx context.Context, key string) ([]byte, error)
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// SourceSyncer pulls a spider's source from git and stores a versioned zip in
// MinIO. We define the interface here, in the consumer, so the gitsource
// implementation can be swapped (e.g. for tests) without touching this file.
type SourceSyncer interface {
	Sync(ctx context.Context, sp *Spider) (sourceKey, commit string, newVersion int32, err error)
}

type Service struct {
	repo   Repository
	store  StorageClient
	syncer SourceSyncer
	log    *slog.Logger
}

func NewService(repo Repository, store StorageClient, syncer SourceSyncer, log *slog.Logger) *Service {
	return &Service{repo: repo, store: store, syncer: syncer, log: log}
}

var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNoGitURL     = errors.New("spider has no git_url configured")
)

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
	if in.GitBranch == "" {
		in.GitBranch = "main"
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
	if in.GitBranch == "" {
		in.GitBranch = "main"
	}
	return s.repo.Update(ctx, id, in)
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.repo.SoftDelete(ctx, id)
}

// Sync clones the spider's git URL, zips the working tree, and uploads the
// result to MinIO. On success the spider's source_key/source_version/
// last_sync_commit are bumped; on failure last_sync_error is recorded.
//
// Returns the updated *Spider so the API can echo the new version.
func (s *Service) Sync(ctx context.Context, id int64) (*Spider, error) {
	sp, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if sp.GitURL == "" {
		return nil, ErrNoGitURL
	}
	key, commit, newVer, err := s.syncer.Sync(ctx, sp)
	if err != nil {
		_ = s.repo.MarkSyncFailed(ctx, id, err.Error())
		return nil, err
	}
	if err := s.repo.MarkSynced(ctx, id, key, commit, newVer); err != nil {
		return nil, err
	}
	s.log.Info("spider synced", "spider_id", id, "version", newVer, "commit", commit)
	return s.repo.Get(ctx, id)
}
