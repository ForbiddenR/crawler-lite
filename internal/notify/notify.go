// Package notify owns notification channels and the fan-out hook that
// task.Service calls when a task reaches a terminal state.
//
// Service is the persistence-facing layer (CRUD + validation). It also
// implements the task.Notifier interface so task.Service can call
// Notify() without importing the notify package's concrete types.
//
// The ShoutrrrSender adapter wraps github.com/containrrr/shoutrrr and
// is the only external HTTP dependency in this package.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Channel is a saved notification destination.
type Channel struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	Enabled   bool      `json:"enabled"`
	CreatedBy *int64    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateInput is the public shape for POST /api/notifications.
type CreateInput struct {
	Name      string
	Kind      string
	URL       string
	Events    []string
	Enabled   bool
	CreatedBy int64
}

// UpdateInput is the public shape for PATCH /api/notifications/:id.
type UpdateInput struct {
	Name    string
	Kind    string
	URL     string
	Events  []string
	Enabled bool
}

// Event is what task.Service passes to Notify after a terminal state
// change. The Kind field is a task.Status string (failed, timeout,
// captcha_blocked, succeeded) — the same tokens used in the
// channel.events JSONB column.
type Event struct {
	Kind         string
	TaskID       int64
	SpiderID     int64
	SpiderName   string
	ErrorClass   string
	ErrorMessage string
	Attempt      int32
	MaxAttempts  int32
	WillRetry    bool
	FinishedAt   time.Time
}

// Repository is what the service needs from the persistence layer.
type Repository interface {
	Insert(ctx context.Context, in CreateInput) (*Channel, error)
	Get(ctx context.Context, id int64) (*Channel, error)
	List(ctx context.Context) ([]*Channel, error)
	ListEnabled(ctx context.Context) ([]*Channel, error)
	Update(ctx context.Context, id int64, in UpdateInput) (*Channel, error)
	Delete(ctx context.Context, id int64) error
}

// Sender abstracts the shoutrrr round-trip. The service uses it for
// Test() and Notify(); tests inject a fake to record calls.
//
// Validate is a no-network parse check used at write time so we 400
// the request before persisting a garbage URL.
type Sender interface {
	Validate(url string) error
	Send(ctx context.Context, urls []string, title, body string) error
}

// Deps groups the constructor arguments.
type Deps struct {
	Repo   Repository
	Sender Sender
	Log    *slog.Logger
}

// Service is the persistence + fan-out layer.
type Service struct {
	deps Deps

	// cache is a short-lived memo for ListEnabled(). The hot path is
	// task.Service.OnUpdate on the gRPC read-loop; we don't want to
	// re-query Postgres for every terminal event.
	mu         sync.Mutex
	cacheUntil time.Time
	cached     []*Channel
}

// NewService constructs a notify.Service.
func NewService(d Deps) *Service {
	return &Service{deps: d}
}

var (
	ErrInvalidInput = errors.New("invalid input")
	ErrInvalidURL   = errors.New("invalid notification URL")
)

// validEvents is the set of event kinds a channel may subscribe to.
// Mirrors the task.Status terminal tokens.
var validEvents = map[string]struct{}{
	"failed":          {},
	"timeout":         {},
	"captcha_blocked": {},
	"succeeded":       {},
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Channel, error) {
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.URL) == "" {
		return nil, ErrInvalidInput
	}
	if in.Kind == "" {
		return nil, ErrInvalidInput
	}
	if err := s.deps.Sender.Validate(in.URL); err != nil {
		return nil, ErrInvalidURL
	}
	if in.Events == nil {
		in.Events = []string{"failed", "timeout", "captcha_blocked"}
	}
	for _, e := range in.Events {
		if _, ok := validEvents[e]; !ok {
			return nil, ErrInvalidInput
		}
	}
	s.invalidate()
	return s.deps.Repo.Insert(ctx, in)
}

func (s *Service) Get(ctx context.Context, id int64) (*Channel, error) {
	return s.deps.Repo.Get(ctx, id)
}

func (s *Service) List(ctx context.Context) ([]*Channel, error) {
	return s.deps.Repo.List(ctx)
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (*Channel, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, ErrInvalidInput
	}
	if in.URL != "" {
		if err := s.deps.Sender.Validate(in.URL); err != nil {
			return nil, ErrInvalidURL
		}
	}
	for _, e := range in.Events {
		if _, ok := validEvents[e]; !ok {
			return nil, ErrInvalidInput
		}
	}
	s.invalidate()
	return s.deps.Repo.Update(ctx, id, in)
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	s.invalidate()
	return s.deps.Repo.Delete(ctx, id)
}

// Test sends a canned message through the channel. The caller surfaces
// the error to the user as a 502.
func (s *Service) Test(ctx context.Context, id int64) error {
	ch, err := s.deps.Repo.Get(ctx, id)
	if err != nil {
		return err
	}
	return s.deps.Sender.Send(ctx, []string{ch.URL},
		"crawler-lite", "Test notification — your channel is configured correctly.")
}

// Notify is called by task.Service.OnUpdate. It loads enabled channels
// (with a short cache), filters by event kind, and fans out
// concurrently. Errors are logged but never returned — notifications
// are a side channel and must never block status persistence.
func (s *Service) Notify(ctx context.Context, ev Event) {
	channels, err := s.enabled(ctx)
	if err != nil {
		s.deps.Log.Error("notify: load channels", "err", err)
		return
	}
	for _, ch := range channels {
		if !contains(ch.Events, ev.Kind) {
			continue
		}
		title, body := format(ev)
		ch := ch
		go func() {
			sendCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := s.deps.Sender.Send(sendCtx, []string{ch.URL}, title, body); err != nil {
				s.deps.Log.Warn("notify: send",
					"channel", ch.Name, "kind", ch.Kind,
					"task", ev.TaskID, "event", ev.Kind, "err", err)
			}
		}()
	}
}

// enabled returns the cached enabled-channel list, refreshing it every
// 5 seconds.
func (s *Service) enabled(ctx context.Context) ([]*Channel, error) {
	s.mu.Lock()
	if time.Now().Before(s.cacheUntil) {
		cached := s.cached
		s.mu.Unlock()
		return cached, nil
	}
	s.mu.Unlock()

	channels, err := s.deps.Repo.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cached = channels
	s.cacheUntil = time.Now().Add(5 * time.Second)
	s.mu.Unlock()
	return channels, nil
}

// invalidate drops the enabled-channel cache so the next Notify reloads.
func (s *Service) invalidate() {
	s.mu.Lock()
	s.cacheUntil = time.Time{}
	s.cached = nil
	s.mu.Unlock()
}

// format builds (title, body) for a notification event. Kept as a
// package-level function so tests can exercise it directly.
func format(ev Event) (string, string) {
	verb := ev.Kind
	switch ev.Kind {
	case "captcha_blocked":
		verb = "blocked by captcha"
	case "failed":
		verb = "failed"
	case "timeout":
		verb = "timed out"
	case "succeeded":
		verb = "succeeded"
	}
	spider := ev.SpiderName
	if spider == "" {
		spider = fmt.Sprintf("id=%d", ev.SpiderID)
	}
	title := fmt.Sprintf("Task #%d %s — %s", ev.TaskID, verb, spider)

	var b strings.Builder
	fmt.Fprintf(&b, "Task #%d %s\n", ev.TaskID, verb)
	fmt.Fprintf(&b, "Spider: %s (id=%d)\n", spider, ev.SpiderID)
	if ev.ErrorMessage != "" {
		fmt.Fprintf(&b, "Error: %s\n", ev.ErrorMessage)
	}
	if ev.Attempt > 0 && ev.MaxAttempts > 0 {
		if ev.WillRetry {
			fmt.Fprintf(&b, "Attempt %d/%d — retrying\n", ev.Attempt, ev.MaxAttempts)
		} else if ev.MaxAttempts > 1 {
			fmt.Fprintf(&b, "Attempt %d/%d — giving up\n", ev.Attempt, ev.MaxAttempts)
		}
	}
	if !ev.FinishedAt.IsZero() {
		fmt.Fprintf(&b, "Time: %s\n", ev.FinishedAt.Format(time.RFC3339))
	}
	return title, b.String()
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
