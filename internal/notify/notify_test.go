// Tests for notify.Service. We inject a fake Sender that records calls
// so we can assert event filtering, disabled-channel skipping, and the
// "never propagate sender errors" contract from Notify().
package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSender records every Validate / Send call. By default Validate
// accepts anything containing "://" and Send returns nil; tests
// override fields when they want different behaviour.
type fakeSender struct {
	mu          sync.Mutex
	sends       []sendCall
	validateErr error
	sendErr     error
}

type sendCall struct {
	urls  []string
	title string
	body  string
}

func (f *fakeSender) Validate(url string) error {
	if f.validateErr != nil {
		return f.validateErr
	}
	if !strings.Contains(url, "://") {
		return errors.New("bad url")
	}
	return nil
}
func (f *fakeSender) Send(_ context.Context, urls []string, title, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, sendCall{urls: urls, title: title, body: body})
	return f.sendErr
}
func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

// stubRepo implements notify.Repository in memory.
type stubRepo struct {
	channels []*Channel
}

func (r *stubRepo) Insert(_ context.Context, in CreateInput) (*Channel, error) {
	c := &Channel{
		ID: int64(len(r.channels) + 1), Name: in.Name, Kind: in.Kind,
		URL: in.URL, Events: in.Events, Enabled: in.Enabled,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	r.channels = append(r.channels, c)
	return c, nil
}
func (r *stubRepo) Get(_ context.Context, id int64) (*Channel, error) {
	for _, c := range r.channels {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, errors.New("not found")
}
func (r *stubRepo) List(_ context.Context) ([]*Channel, error) { return r.channels, nil }
func (r *stubRepo) ListEnabled(_ context.Context) ([]*Channel, error) {
	var out []*Channel
	for _, c := range r.channels {
		if c.Enabled {
			out = append(out, c)
		}
	}
	return out, nil
}
func (r *stubRepo) Update(_ context.Context, _ int64, _ UpdateInput) (*Channel, error) {
	return nil, errors.New("unused")
}
func (r *stubRepo) Delete(_ context.Context, _ int64) error { return nil }

func newServiceWith(repo Repository, sender Sender) *Service {
	return NewService(Deps{
		Repo:   repo,
		Sender: sender,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// waitForSends spins for up to 1s waiting for the fake to record at
// least `want` calls. Notify dispatches in a goroutine per channel.
func waitForSends(t *testing.T, f *fakeSender, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if f.count() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestServiceNotifyEventFilter: enabled channel subscribed to
// `failed` receives a `failed` event; same channel does NOT receive a
// `succeeded` event. Critical to the slice — the filter is the whole
// point of the events column.
func TestServiceNotifyEventFilter(t *testing.T) {
	repo := &stubRepo{channels: []*Channel{
		{ID: 1, Name: "ops", Kind: "slack", URL: "slack://x/y/z",
			Events: []string{"failed"}, Enabled: true},
	}}
	sender := &fakeSender{}
	svc := newServiceWith(repo, sender)

	svc.Notify(context.Background(), Event{Kind: "failed", TaskID: 7})
	waitForSends(t, sender, 1)
	if sender.count() != 1 {
		t.Fatalf("expected 1 send for matching kind, got %d", sender.count())
	}

	svc.Notify(context.Background(), Event{Kind: "succeeded", TaskID: 8})
	// Give a goroutine a chance to (incorrectly) fire.
	time.Sleep(50 * time.Millisecond)
	if sender.count() != 1 {
		t.Errorf("expected no extra send for non-matching kind, got %d total", sender.count())
	}
}

// TestServiceNotifyDisabledChannelSkipped: a disabled channel must be
// invisible to Notify regardless of its event filter.
func TestServiceNotifyDisabledChannelSkipped(t *testing.T) {
	repo := &stubRepo{channels: []*Channel{
		{ID: 1, Name: "ops", Kind: "slack", URL: "slack://x/y/z",
			Events: []string{"failed"}, Enabled: false},
	}}
	sender := &fakeSender{}
	svc := newServiceWith(repo, sender)

	svc.Notify(context.Background(), Event{Kind: "failed", TaskID: 7})
	time.Sleep(50 * time.Millisecond)
	if sender.count() != 0 {
		t.Errorf("disabled channel must not send, got %d", sender.count())
	}
}

// TestServiceNotifySenderErrorDoesNotPanic: sender returns err. Notify
// must still return (it returns nothing anyway, but goroutines must not
// panic and the next call should still work).
func TestServiceNotifySenderErrorDoesNotPanic(t *testing.T) {
	repo := &stubRepo{channels: []*Channel{
		{ID: 1, Name: "ops", Kind: "slack", URL: "slack://x/y/z",
			Events: []string{"failed"}, Enabled: true},
	}}
	sender := &fakeSender{sendErr: errors.New("network boom")}
	svc := newServiceWith(repo, sender)

	svc.Notify(context.Background(), Event{Kind: "failed", TaskID: 7})
	waitForSends(t, sender, 1)
	if sender.count() != 1 {
		t.Errorf("send still recorded despite error, got %d", sender.count())
	}
}

// TestServiceCreateInvalidURLRejected: Sender.Validate says no, Service
// returns ErrInvalidURL — the handler will translate that to 400.
func TestServiceCreateInvalidURLRejected(t *testing.T) {
	repo := &stubRepo{}
	sender := &fakeSender{validateErr: errors.New("parse fail")}
	svc := newServiceWith(repo, sender)

	_, err := svc.Create(context.Background(), CreateInput{
		Name: "x", Kind: "slack", URL: "garbage", Events: []string{"failed"},
	})
	if !errors.Is(err, ErrInvalidURL) {
		t.Errorf("expected ErrInvalidURL, got %v", err)
	}
}

// TestServiceCreateInvalidEventsRejected: unknown event token →
// ErrInvalidInput. Prevents typos like "fail" from silently never
// firing.
func TestServiceCreateInvalidEventsRejected(t *testing.T) {
	repo := &stubRepo{}
	sender := &fakeSender{}
	svc := newServiceWith(repo, sender)

	_, err := svc.Create(context.Background(), CreateInput{
		Name: "x", Kind: "slack", URL: "slack://a/b/c",
		Events: []string{"fail" /* typo */},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

// TestFormat: smoke-check the body lines. Locks in the strings shown
// in Slack/Discord so a refactor of format() can't silently empty the
// payload.
func TestFormat(t *testing.T) {
	title, body := format(Event{
		Kind: "failed", TaskID: 42, SpiderID: 7, SpiderName: "amazon",
		ErrorMessage: "boom", Attempt: 2, MaxAttempts: 3, WillRetry: true,
	})
	if !strings.Contains(title, "#42") || !strings.Contains(title, "failed") {
		t.Errorf("title missing key bits: %q", title)
	}
	if !strings.Contains(body, "amazon") || !strings.Contains(body, "boom") ||
		!strings.Contains(body, "2/3") || !strings.Contains(body, "retrying") {
		t.Errorf("body missing key bits: %q", body)
	}
}
