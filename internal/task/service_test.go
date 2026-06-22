// Tests for the OnUpdate retry hook (Slice 16a) — specifically the captcha
// guarantee from Slice 16b: a captcha_blocked terminal state must NEVER
// schedule a retry, even if the spider's retry_on list contains every
// other allowed class.
package task

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/yourteam/crawler-lite/internal/spider"
)

// stubTaskRepo implements task.Repository with enough fidelity for the
// hook tests. Create calls are recorded so we can assert "no retry was
// scheduled".
type stubTaskRepo struct {
	parent  *Task
	creates []CreateInput
}

func (s *stubTaskRepo) Create(_ context.Context, in CreateInput) (*Task, error) {
	s.creates = append(s.creates, in)
	t := &Task{ID: int64(1000 + len(s.creates)), SpiderID: in.SpiderID, Attempt: in.Attempt}
	return t, nil
}
func (s *stubTaskRepo) Get(_ context.Context, id int64) (*Task, error) {
	if s.parent != nil && s.parent.ID == id {
		return s.parent, nil
	}
	return nil, ErrInvalidInput
}
func (s *stubTaskRepo) List(_ context.Context, _, _ int) ([]*Task, error)  { return nil, nil }
func (s *stubTaskRepo) ListQueued(_ context.Context) ([]*Task, error)      { return nil, nil }
func (s *stubTaskRepo) SetStatus(_ context.Context, _ int64, _ Status, _, _, _ string) error {
	return nil
}

// stubSpiders implements task.SpiderLookup with a single canned spider.
type stubSpiders struct{ s *spider.Spider }

func (l *stubSpiders) Get(_ context.Context, id int64) (*spider.Spider, error) {
	if l.s != nil && l.s.ID == id {
		return l.s, nil
	}
	return nil, ErrInvalidInput
}

func newServiceWith(repo Repository, spiders SpiderLookup) *Service {
	return NewService(Deps{
		Repo:    repo,
		Spiders: spiders,
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// TestOnUpdateCaptchaSkipsRetry: even when the spider has aggressively
// opted into every retryable class AND a generous max_attempts, a
// captcha_blocked terminal status must not produce a child task.
//
// This is the cross-slice guarantee. Slice 16a's Decide() blocks captcha
// from retry; Slice 16b's classifyOutcome ensures captcha terminal states
// arrive as `captcha_blocked` (not `failed`), so OnUpdate's early return
// at the status check is what does the work here. Locking it in with a
// test means a later refactor can't quietly remove that guard.
func TestOnUpdateCaptchaSkipsRetry(t *testing.T) {
	parent := &Task{ID: 7, SpiderID: 42, SpiderVersion: 3, Attempt: 1}
	sp := &spider.Spider{
		ID: 42,
		Config: map[string]any{
			"retry": map[string]any{
				"max_attempts": float64(5),
				"backoff":      "linear",
				"base_delay_s": float64(1),
				// Author asked for every class — captcha must STILL be
				// filtered out by retry parsing AND by OnUpdate's status
				// check.
				"retry_on": []any{"timeout", "exit", "deps", "build_assign"},
			},
		},
	}
	repo := &stubTaskRepo{parent: parent}
	svc := newServiceWith(repo, &stubSpiders{s: sp})

	if err := svc.OnUpdate(context.Background(), parent.ID,
		StatusCaptchaBlocked, "hit hCaptcha", "captcha", "worker-1"); err != nil {
		t.Fatalf("OnUpdate: %v", err)
	}
	if len(repo.creates) != 0 {
		t.Errorf("expected no child task on captcha_blocked, got %d: %+v",
			len(repo.creates), repo.creates)
	}
}

// TestOnUpdateFailureSchedulesRetry is the happy path: a `failed`
// terminal with err_class="exit" against an opted-in retry_on list does
// schedule a child task. Keeps the negative test honest — if a refactor
// breaks all retries, both tests fail loudly together.
func TestOnUpdateFailureSchedulesRetry(t *testing.T) {
	parent := &Task{ID: 11, SpiderID: 99, SpiderVersion: 1, Attempt: 1}
	sp := &spider.Spider{
		ID: 99,
		Config: map[string]any{
			"retry": map[string]any{
				"max_attempts": float64(3),
				"backoff":      "linear",
				"base_delay_s": float64(1),
				"retry_on":     []any{"exit"},
			},
		},
	}
	repo := &stubTaskRepo{parent: parent}
	svc := newServiceWith(repo, &stubSpiders{s: sp})

	if err := svc.OnUpdate(context.Background(), parent.ID,
		StatusFailed, "boom", "exit", "worker-1"); err != nil {
		t.Fatalf("OnUpdate: %v", err)
	}
	if len(repo.creates) != 1 {
		t.Fatalf("expected exactly one child task, got %d", len(repo.creates))
	}
	child := repo.creates[0]
	if child.Trigger != TriggerRetry {
		t.Errorf("child trigger = %q, want retry", child.Trigger)
	}
	if child.ParentTaskID != parent.ID {
		t.Errorf("child parent = %d, want %d", child.ParentTaskID, parent.ID)
	}
	if child.Attempt != 2 {
		t.Errorf("child attempt = %d, want 2", child.Attempt)
	}
	if child.NotBefore == nil {
		t.Errorf("child not_before must be set when delay > 0")
	}
}

// TestOnUpdateSucceededNeverRetries — sanity check on the early return.
func TestOnUpdateSucceededNeverRetries(t *testing.T) {
	parent := &Task{ID: 1, SpiderID: 1, Attempt: 1}
	sp := &spider.Spider{ID: 1, Config: map[string]any{
		"retry": map[string]any{
			"max_attempts": float64(5),
			"retry_on":     []any{"timeout", "exit"},
		},
	}}
	repo := &stubTaskRepo{parent: parent}
	svc := newServiceWith(repo, &stubSpiders{s: sp})
	if err := svc.OnUpdate(context.Background(), parent.ID,
		StatusSucceeded, "", "", "w"); err != nil {
		t.Fatalf("OnUpdate: %v", err)
	}
	if len(repo.creates) != 0 {
		t.Errorf("succeeded terminal must not retry, got %d creates", len(repo.creates))
	}
}
