// Tests for the per-spider venv resolver.
//
// We don't shell out to a real `uv` (slow, network-bound, optional). Each
// test writes a tiny shell stub at a known path and points the executor at
// it via `uvPath`. The stub:
//
//  1. Bumps a counter file so tests can assert how many times uv was
//     invoked across a sequence of calls.
//  2. Emulates `uv venv <dir> --python ...`: mkdir -p <dir>/bin && touch
//     <dir>/bin/python.
//  3. Emulates `uv pip install ...`: prints a line so we can verify the
//     "[deps] ..." log forwarding, and (optionally) exits non-zero when
//     the requirements file contains a magic FAIL marker.
//
// That's enough to drive every branch of resolveInterpreter without needing
// Python, pip, or a network.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/yourteam/crawler-lite/internal/pb/worker/v1"
)

// writeFakeUV writes a shell script that emulates the uv subset we use and
// returns its absolute path plus the path to a counter file the script
// increments on every invocation.
func writeFakeUV(t *testing.T, dir string) (uvPath, counterPath string) {
	t.Helper()
	counterPath = filepath.Join(dir, "uv-invocations")
	uvPath = filepath.Join(dir, "fake-uv")
	script := `#!/bin/sh
# Bump invocation counter atomically enough for tests.
counter=` + shellEscape(counterPath) + `
n=$(cat "$counter" 2>/dev/null || echo 0)
echo $((n + 1)) > "$counter"

cmd="$1"; shift
case "$cmd" in
venv)
    target="$1"; shift
    mkdir -p "$target/bin"
    : > "$target/bin/python"
    echo "fake-uv: created venv at $target"
    ;;
pip)
    sub="$1"; shift
    if [ "$sub" != "install" ]; then
        echo "fake-uv: unsupported pip subcommand: $sub" >&2
        exit 2
    fi
    # Walk args looking for -r REQS so we can fail on the magic marker.
    while [ $# -gt 0 ]; do
        case "$1" in
        -r) shift; reqs="$1" ;;
        esac
        shift
    done
    if [ -n "$reqs" ] && grep -q '^FAIL$' "$reqs" 2>/dev/null; then
        echo "fake-uv: refusing FAIL requirement" >&2
        exit 1
    fi
    echo "fake-uv: installed ok"
    ;;
*)
    echo "fake-uv: unknown command: $cmd" >&2
    exit 2
    ;;
esac
`
	if err := os.WriteFile(uvPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake uv: %v", err)
	}
	return uvPath, counterPath
}

func shellEscape(s string) string {
	// Single-quote and double up any embedded single-quotes.
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func readCounter(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read counter: %v", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("parse counter %q: %v", string(b), err)
	}
	return n
}

// drain pulls messages off outbox until it's idle for `quiet`. Returns all
// the log message texts it saw, in order. Useful for asserting we DID NOT
// emit `[deps]` lines on the cache-hit path.
func drainLogs(outbox <-chan *pb.WorkerMsg, quiet time.Duration) []string {
	var out []string
	timer := time.NewTimer(quiet)
	defer timer.Stop()
	for {
		select {
		case m, ok := <-outbox:
			if !ok {
				return out
			}
			if ll := m.GetLogLine(); ll != nil {
				out = append(out, ll.Message)
			}
			// reset the quiet timer
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(quiet)
		case <-timer.C:
			return out
		}
	}
}

func newTestExecutor(t *testing.T, uvPath string) (*TaskExecutor, string) {
	t.Helper()
	venvDir := t.TempDir()
	e := &TaskExecutor{
		pyPath:    "/usr/bin/python3-not-actually-invoked",
		workDir:   t.TempDir(),
		venvDir:   venvDir,
		uvPath:    uvPath,
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		venvLocks: make(map[string]*sync.Mutex),
	}
	return e, venvDir
}

// --- tests ------------------------------------------------------------------

func TestResolveInterpreter_NoRequirementsTxt(t *testing.T) {
	uvPath, counter := writeFakeUV(t, t.TempDir())
	e, _ := newTestExecutor(t, uvPath)
	srcDir := t.TempDir()
	outbox := make(chan *pb.WorkerMsg, 16)

	got, err := e.resolveInterpreter(context.Background(), srcDir, 1, outbox)
	if err != nil {
		t.Fatalf("resolveInterpreter: %v", err)
	}
	if got != e.pyPath {
		t.Errorf("want system python %q, got %q", e.pyPath, got)
	}
	if n := readCounter(t, counter); n != 0 {
		t.Errorf("uv should not have been invoked, was invoked %d times", n)
	}
}

func TestResolveInterpreter_NoUV_FallsBackWithWarning(t *testing.T) {
	e, _ := newTestExecutor(t, "")
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "requirements.txt"), []byte("requests==2.31.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outbox := make(chan *pb.WorkerMsg, 16)

	got, err := e.resolveInterpreter(context.Background(), srcDir, 7, outbox)
	if err != nil {
		t.Fatalf("resolveInterpreter: %v", err)
	}
	if got != e.pyPath {
		t.Errorf("expected fallback to system python, got %q", got)
	}
	logs := drainLogs(outbox, 50*time.Millisecond)
	var sawWarn bool
	for _, m := range logs {
		if strings.Contains(m, "uv is not installed") {
			sawWarn = true
			break
		}
	}
	if !sawWarn {
		t.Errorf("expected a deps warning, got logs %v", logs)
	}
}

func TestResolveInterpreter_CacheHit(t *testing.T) {
	uvPath, counter := writeFakeUV(t, t.TempDir())
	e, venvDir := newTestExecutor(t, uvPath)
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "requirements.txt"), []byte("requests==2.31.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outbox := make(chan *pb.WorkerMsg, 64)
	first, err := e.resolveInterpreter(context.Background(), srcDir, 1, outbox)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !strings.HasPrefix(first, venvDir) {
		t.Errorf("expected venv-internal python, got %q", first)
	}
	// Drain logs from the cold install.
	_ = drainLogs(outbox, 50*time.Millisecond)
	afterFirst := readCounter(t, counter)
	if afterFirst < 2 {
		t.Fatalf("expected at least venv+install (2 uv calls), got %d", afterFirst)
	}

	second, err := e.resolveInterpreter(context.Background(), srcDir, 2, outbox)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if second != first {
		t.Errorf("cache miss: first=%q second=%q", first, second)
	}
	if got := readCounter(t, counter); got != afterFirst {
		t.Errorf("expected uv not re-invoked on cache hit, counter was %d, now %d", afterFirst, got)
	}
	// No `[deps]` lines should have leaked on the cache-hit path.
	logs := drainLogs(outbox, 50*time.Millisecond)
	for _, m := range logs {
		if strings.HasPrefix(m, "[deps]") {
			t.Errorf("cache hit should be silent, but saw deps log %q", m)
		}
	}
}

func TestResolveInterpreter_DifferentHashesGetSeparateVenvs(t *testing.T) {
	uvPath, _ := writeFakeUV(t, t.TempDir())
	e, _ := newTestExecutor(t, uvPath)

	src1 := t.TempDir()
	src2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(src1, "requirements.txt"), []byte("requests==2.31.0\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src2, "requirements.txt"), []byte("bs4==0.0.1\n"), 0o644)

	outbox := make(chan *pb.WorkerMsg, 128)
	p1, err := e.resolveInterpreter(context.Background(), src1, 1, outbox)
	if err != nil {
		t.Fatalf("src1: %v", err)
	}
	p2, err := e.resolveInterpreter(context.Background(), src2, 2, outbox)
	if err != nil {
		t.Fatalf("src2: %v", err)
	}
	if p1 == p2 {
		t.Errorf("expected distinct venvs, got %q for both", p1)
	}
	// .../<venvDir>/<hash>/bin/python → up three levels is the shared venv root.
	parent := func(p string) string { return filepath.Dir(filepath.Dir(filepath.Dir(p))) }
	if parent(p1) != parent(p2) {
		t.Errorf("venv parent dirs diverged: %q vs %q", parent(p1), parent(p2))
	}
}

func TestResolveInterpreter_ConcurrentSameHash_SingleInstall(t *testing.T) {
	uvPath, counter := writeFakeUV(t, t.TempDir())
	e, _ := newTestExecutor(t, uvPath)
	srcDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(srcDir, "requirements.txt"), []byte("requests==2.31.0\n"), 0o644)

	outbox := make(chan *pb.WorkerMsg, 256)
	// Drain in background to avoid blocking the resolver on a full channel.
	doneDrain := make(chan struct{})
	go func() {
		defer close(doneDrain)
		drainLogs(outbox, 200*time.Millisecond)
	}()

	const N = 6
	var wg sync.WaitGroup
	wg.Add(N)
	got := make([]string, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = e.resolveInterpreter(context.Background(), srcDir, int64(i), outbox)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	for i := 1; i < N; i++ {
		if got[i] != got[0] {
			t.Errorf("goroutine %d returned different python path: %q vs %q", i, got[i], got[0])
		}
	}
	// Exactly one install: 2 uv invocations (venv + pip install). The lock
	// must have serialized the others into cache hits.
	if n := readCounter(t, counter); n != 2 {
		t.Errorf("expected 2 uv invocations (one install), got %d", n)
	}
	close(outbox)
	<-doneDrain
}

func TestResolveInterpreter_InstallFailure_LeavesNoCacheEntry(t *testing.T) {
	uvPath, _ := writeFakeUV(t, t.TempDir())
	e, venvDir := newTestExecutor(t, uvPath)
	srcDir := t.TempDir()
	// Magic marker recognized by fake-uv to exit non-zero.
	_ = os.WriteFile(filepath.Join(srcDir, "requirements.txt"), []byte("FAIL\n"), 0o644)

	outbox := make(chan *pb.WorkerMsg, 64)
	go drainLogs(outbox, 200*time.Millisecond)

	_, err := e.resolveInterpreter(context.Background(), srcDir, 1, outbox)
	if err == nil {
		t.Fatal("expected install to fail")
	}
	if !strings.Contains(err.Error(), "uv pip install") {
		t.Errorf("unexpected error wrapping: %v", err)
	}
	// venvDir should be empty — failed install must clean up its own cache key.
	entries, _ := os.ReadDir(venvDir)
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("expected no venv on failed install, found %q", e.Name())
		}
	}
}

// Compile-time guard that the channel signature still matches what
// resolveInterpreter expects — if anyone changes the WorkerMsg shape this
// breaks visibly here.
var _ = func() chan<- *pb.WorkerMsg { return make(chan *pb.WorkerMsg) }

// Reference the unused-import vacuum so go vet doesn't trip if a future edit
// removes one of the fmt-style strings above.
var _ = fmt.Sprintf

// ---------------------------------------------------------------------------
// Captcha path (Slice 16b)
// ---------------------------------------------------------------------------

// TestPumpEventsCaptcha feeds a synthetic FD3 stream containing a single
// captcha event and asserts that pumpEvents:
//
//   - flips the shared captchaObs to seen=true with the right message,
//   - emits an INFO log line so the live tail surfaces the reason,
//   - emits a TaskUpdate with state=CAPTCHA, class=captcha, and the
//     message in the error field.
func TestPumpEventsCaptcha(t *testing.T) {
	const taskID int64 = 4242
	r, w := io.Pipe()
	outbox := make(chan *pb.WorkerMsg, 16)
	var obs captchaObs
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	done := make(chan struct{})
	go func() {
		pumpEvents(r, taskID, outbox, &obs, nil, logger)
		close(done)
	}()

	if _, err := w.Write([]byte(`{"type":"captcha","data":{"message":"hit hCaptcha on /checkout"}}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = w.Close()
	<-done

	seen, msg := obs.snapshot()
	if !seen || msg != "hit hCaptcha on /checkout" {
		t.Errorf("captcha observer not set: seen=%v msg=%q", seen, msg)
	}

	var sawLog, sawUpdate bool
	close(outbox)
	for m := range outbox {
		switch p := m.Payload.(type) {
		case *pb.WorkerMsg_LogLine:
			if p.LogLine.Level == "INFO" && strings.Contains(p.LogLine.Message, "[captcha] hit hCaptcha") {
				sawLog = true
			}
		case *pb.WorkerMsg_TaskUpdate:
			u := p.TaskUpdate
			if u.TaskId != taskID {
				t.Errorf("TaskUpdate task_id = %d, want %d", u.TaskId, taskID)
			}
			if u.State != pb.TaskState_TASK_STATE_CAPTCHA {
				t.Errorf("TaskUpdate state = %v, want CAPTCHA", u.State)
			}
			if u.ErrorClass != "captcha" {
				t.Errorf("TaskUpdate error_class = %q, want %q", u.ErrorClass, "captcha")
			}
			if u.Error != "hit hCaptcha on /checkout" {
				t.Errorf("TaskUpdate error = %q, want forwarded message", u.Error)
			}
			sawUpdate = true
		}
	}
	if !sawLog {
		t.Errorf("expected an INFO log line with the captcha message")
	}
	if !sawUpdate {
		t.Errorf("expected a TaskUpdate(CAPTCHA) frame")
	}
}

// TestPumpEventsCaptchaMissingMessage covers the case where a spider
// calls self.captcha() with no message. The observer must still flip;
// the error field on the wire goes out empty rather than crashing.
func TestPumpEventsCaptchaMissingMessage(t *testing.T) {
	r, w := io.Pipe()
	outbox := make(chan *pb.WorkerMsg, 8)
	var obs captchaObs
	done := make(chan struct{})
	go func() {
		pumpEvents(r, 1, outbox, &obs, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	_, _ = w.Write([]byte(`{"type":"captcha","data":{}}` + "\n"))
	_ = w.Close()
	<-done
	seen, msg := obs.snapshot()
	if !seen {
		t.Errorf("observer must flip even with empty message")
	}
	if msg != "" {
		t.Errorf("expected empty message, got %q", msg)
	}
}

// TestPumpEventsCapturesErrorLog feeds the exact FD3 frame the Python runner
// emits on an uncaught exception — an ERROR log with the message headline and
// the traceback nested under fields.traceback — and asserts that pumpEvents:
//
//   - records the headline message into the errorObs so classifyOutcome can
//     use it as the terminal Result.Error instead of "exit status 1",
//   - folds the traceback into the emitted LogLine message so the full stack
//     survives in the durable (MinIO) + live (Redis) log.
func TestPumpEventsCapturesErrorLog(t *testing.T) {
	const taskID int64 = 7
	r, w := io.Pipe()
	outbox := make(chan *pb.WorkerMsg, 8)
	var obs errorObs
	done := make(chan struct{})
	go func() {
		pumpEvents(r, taskID, outbox, nil, &obs, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()

	frame := `{"type":"log","data":{"level":"ERROR","message":"ValueError: bad thing","ts_ns":1700000000000000000,"fields":{"traceback":"Traceback (most recent call last):\n  File \"spider.py\", line 10, in run\n    raise ValueError(\"bad thing\")\nValueError: bad thing"}}}` + "\n"
	if _, err := w.Write([]byte(frame)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = w.Close()
	<-done

	if got := obs.snapshot(); got != "ValueError: bad thing" {
		t.Errorf("errorObs message = %q, want %q", got, "ValueError: bad thing")
	}

	close(outbox)
	var sawErrorLog bool
	for m := range outbox {
		if ll, ok := m.Payload.(*pb.WorkerMsg_LogLine); ok && ll.LogLine.Level == "ERROR" {
			sawErrorLog = true
			if !strings.Contains(ll.LogLine.Message, "ValueError: bad thing") {
				t.Errorf("log message lost headline: %q", ll.LogLine.Message)
			}
			if !strings.Contains(ll.LogLine.Message, "Traceback (most recent call last)") {
				t.Errorf("log message lost traceback: %q", ll.LogLine.Message)
			}
			if ll.LogLine.TsNs != 1700000000000000000 {
				t.Errorf("ts_ns = %d, want preserved value", ll.LogLine.TsNs)
			}
		}
	}
	if !sawErrorLog {
		t.Errorf("expected an ERROR LogLine to be emitted")
	}
}

// TestClassifyOutcome exercises every branch of the pure decision
// function. The interesting row is "subprocess exit non-zero AND captcha
// was observed": pre-Slice-16b this returned FAILED + class=exit; now it
// must return CAPTCHA so the master-side retry path skips it.
func TestClassifyOutcome(t *testing.T) {
	timeoutErr := context.DeadlineExceeded
	cancelErr := context.Canceled
	someExit := errors.New("exit status 1")

	cases := []struct {
		name           string
		parentCtx      error
		runCtx         error
		waitErr        error
		seen           bool
		msg            string
		lastErr        string
		wantState      pb.TaskState
		wantErrorClass string
		wantError      string
	}{
		{
			name: "clean success, no captcha",
			seen: false, wantState: pb.TaskState_TASK_STATE_SUCCEEDED,
		},
		{
			name: "clean exit but captcha observed",
			seen: true, msg: "hcaptcha",
			wantState: pb.TaskState_TASK_STATE_CAPTCHA, wantErrorClass: "captcha", wantError: "hcaptcha",
		},
		{
			name:    "non-zero exit, no captcha, no captured log → bare exit status",
			waitErr: someExit, seen: false,
			wantState: pb.TaskState_TASK_STATE_FAILED, wantErrorClass: "exit", wantError: someExit.Error(),
		},
		{
			name:    "non-zero exit with captured ERROR log → records Python reason",
			waitErr: someExit, seen: false, lastErr: "ValueError: bad thing",
			wantState: pb.TaskState_TASK_STATE_FAILED, wantErrorClass: "exit", wantError: "ValueError: bad thing",
		},
		{
			name:    "non-zero exit AFTER captcha → CAPTCHA",
			waitErr: someExit, seen: true, msg: "hcaptcha",
			wantState: pb.TaskState_TASK_STATE_CAPTCHA, wantErrorClass: "captcha", wantError: "hcaptcha",
		},
		{
			name:      "parent cancellation always wins",
			parentCtx: cancelErr, waitErr: someExit, seen: true, msg: "x",
			wantState: pb.TaskState_TASK_STATE_CANCELLED,
		},
		{
			name:   "run-timeout always wins (no zombie classification)",
			runCtx: timeoutErr, waitErr: someExit, seen: true,
			wantState: pb.TaskState_TASK_STATE_TIMEOUT, wantErrorClass: "timeout", wantError: "task timed out",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyOutcome(tc.parentCtx, tc.runCtx, tc.waitErr, tc.seen, tc.msg, tc.lastErr)
			if got.State != tc.wantState {
				t.Errorf("state: got %v, want %v", got.State, tc.wantState)
			}
			if got.ErrorClass != tc.wantErrorClass {
				t.Errorf("class: got %q, want %q", got.ErrorClass, tc.wantErrorClass)
			}
			if got.Error != tc.wantError {
				t.Errorf("error: got %q, want %q", got.Error, tc.wantError)
			}
		})
	}
}
