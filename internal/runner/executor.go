// Package runner — TaskExecutor.
//
// On each AssignTask the executor:
//
//  1. Downloads the spider source zip from MinIO.
//  2. Extracts it into a fresh per-task working directory.
//  3. Spawns `python -m crawlerkit.runner`, with FD 3 wired to a pipe and
//     spider config injected through env vars.
//  4. Reads structured events (logs, items, screenshots) from FD 3 as JSONL
//     and translates each into a pb.WorkerMsg pushed onto the outbox.
//  5. Reads stdout/stderr separately and forwards them as INFO/ERROR log
//     lines so user `print()` calls still surface in the UI.
//  6. Enforces the task timeout — SIGTERM first, then SIGKILL on the
//     process group to take down any spawned children (Chromium etc.).
//
// Returns when the subprocess exits, the timeout fires, or the parent ctx
// is cancelled.
package runner

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	pb "github.com/yourteam/crawler-lite/internal/pb/worker/v1"
)

// StorageClient is the slice of *storage.MinIOClient the executor needs.
type StorageClient interface {
	Download(ctx context.Context, key string) ([]byte, error)
	PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
}

type TaskExecutor struct {
	store   StorageClient
	pyPath  string // /usr/bin/python3 by default
	workDir string // parent dir for per-task working dirs
	venvDir string // parent dir for per-requirements-hash venvs (persistent across tasks)
	uvPath  string // resolved path to `uv`, or "" if uv isn't installed
	log     *slog.Logger

	// venvLocks serializes installs for the same requirements-hash so two
	// simultaneous tasks for the same spider don't race on the same venv dir.
	// One sync.Mutex per hash; the outer map is only read/extended under
	// venvMu.
	venvMu    sync.Mutex
	venvLocks map[string]*sync.Mutex
}

func NewTaskExecutor(store StorageClient, pyPath, workDir, venvDir, uvPath string, log *slog.Logger) *TaskExecutor {
	if pyPath == "" {
		pyPath = "python3"
	}
	if workDir == "" {
		workDir = "/tmp/crawler-lite"
	}
	if venvDir == "" {
		venvDir = "/var/lib/crawler-lite/venvs"
	}
	if uvPath == "" {
		if p, err := exec.LookPath("uv"); err == nil {
			uvPath = p
		}
	}
	if uvPath == "" {
		log.Warn("uv not installed; per-spider requirements.txt will be skipped",
			"hint", "install with `make tools-uv` or set UV_PATH")
	}
	return &TaskExecutor{
		store:     store,
		pyPath:    pyPath,
		workDir:   workDir,
		venvDir:   venvDir,
		uvPath:    uvPath,
		log:       log,
		venvLocks: make(map[string]*sync.Mutex),
	}
}

// Result is what the worker sends as the final TaskUpdate after Run returns.
type Result struct {
	State      pb.TaskState
	Error      string
	ErrorClass string
}

// Run executes a single task. The returned Result is what should be sent to
// the master as the terminal TaskUpdate. Intermediate events go onto outbox
// during execution.
func (e *TaskExecutor) Run(ctx context.Context, a *pb.AssignTask, outbox chan<- *pb.WorkerMsg) Result {
	// --- 1. Working dir ---------------------------------------------------
	dir, err := e.prepareDir(ctx, a)
	if err != nil {
		return Result{State: pb.TaskState_TASK_STATE_FAILED, Error: err.Error(), ErrorClass: "prepare"}
	}
	defer os.RemoveAll(dir)

	// --- 2. Per-task presigned PUT URL for screenshot uploads ------------
	presignBase, err := e.store.PresignPut(ctx,
		fmt.Sprintf("tasks/%d/_placeholder", a.TaskId), 2*time.Hour,
	)
	if err != nil {
		// Non-fatal — spiders without screenshots still work. Log and continue.
		e.log.Warn("presign put", "task", a.TaskId, "err", err)
	}

	// --- 2b. Resolve interpreter (per-spider venv if requirements.txt) ---
	pyExe, err := e.resolveInterpreter(ctx, dir, a.TaskId, outbox)
	if err != nil {
		stderr := err.Error()
		if len(stderr) > 2048 {
			stderr = stderr[:2048]
		}
		return Result{State: pb.TaskState_TASK_STATE_FAILED, Error: stderr, ErrorClass: "deps"}
	}

	// --- 3. Pipe for FD 3 -------------------------------------------------
	eventR, eventW, err := os.Pipe()
	if err != nil {
		return Result{State: pb.TaskState_TASK_STATE_FAILED, Error: err.Error(), ErrorClass: "pipe"}
	}
	defer eventR.Close()

	// --- 4. Spawn ---------------------------------------------------------
	timeout := time.Duration(a.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, pyExe, "-m", "crawlerkit.runner")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"PYTHONUNBUFFERED=1",
		"CRAWLERKIT_TASK_ID="+strconv.FormatInt(a.TaskId, 10),
		"CRAWLERKIT_SPIDER_ID="+strconv.FormatInt(a.SpiderId, 10),
		"CRAWLERKIT_EVENT_FD=3",
		"CRAWLERKIT_CONFIG="+string(a.ConfigJson),
		"CRAWLERKIT_ARGS="+string(a.ArgsJson),
		"CRAWLERKIT_PROXY_URL="+a.ProxyUrl,
		"CRAWLERKIT_PRESIGN_PUT="+presignBase,
	)
	cmd.ExtraFiles = []*os.File{eventW} // child sees this as FD 3
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{State: pb.TaskState_TASK_STATE_FAILED, Error: err.Error(), ErrorClass: "stdout"}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{State: pb.TaskState_TASK_STATE_FAILED, Error: err.Error(), ErrorClass: "stderr"}
	}

	if err := cmd.Start(); err != nil {
		return Result{State: pb.TaskState_TASK_STATE_FAILED, Error: err.Error(), ErrorClass: "spawn"}
	}
	// Close our copy of the write end so EOF reaches eventR when the child exits.
	_ = eventW.Close()

	// --- 5. Pump output streams ------------------------------------------
	//
	// captcha is shared state: pumpEvents flips Seen + Message when the
	// spider emits a `captcha` event; classifyOutcome consults the snapshot
	// after the subprocess exits so a "self.captcha(); raise" sequence
	// doesn't get overwritten to FAILED. errObs is the error-side analogue:
	// pumpEvents records the last ERROR log line so a non-zero exit records
	// the actual Python exception instead of the bare "exit status 1".
	var captcha captchaObs
	var errObs errorObs
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); pumpEvents(eventR, a.TaskId, outbox, &captcha, &errObs, e.log) }()
	go func() { defer wg.Done(); pumpUserLog(stdout, a.TaskId, "INFO", outbox, e.log) }()
	go func() { defer wg.Done(); pumpUserLog(stderr, a.TaskId, "ERROR", outbox, e.log) }()

	// --- 6. Wait + classify outcome --------------------------------------
	waitErr := cmd.Wait()
	wg.Wait()

	seenCaptcha, captchaMsg := captcha.snapshot()
	res := classifyOutcome(ctx.Err(), runCtx.Err(), waitErr, seenCaptcha, captchaMsg, errObs.snapshot())
	// On timeout we still need to reap any orphaned process group; do it
	// here so classifyOutcome stays a pure function.
	if res.State == pb.TaskState_TASK_STATE_TIMEOUT && cmd.Process != nil {
		_ = killGroup(cmd.Process.Pid)
	}
	return res
}

// captchaObs is the small piece of state shared between pumpEvents (which
// observes a `captcha` FD3 event) and classifyOutcome (which has to know
// whether to flip the final Result to CAPTCHA instead of FAILED).
type captchaObs struct {
	mu      sync.Mutex
	seen    bool
	message string
}

func (c *captchaObs) set(msg string) {
	c.mu.Lock()
	c.seen = true
	c.message = msg
	c.mu.Unlock()
}

func (c *captchaObs) snapshot() (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen, c.message
}

// errorObs captures the last ERROR-level log line the spider emitted on FD3,
// so the terminal Result records the actual Python failure reason (e.g.
// "ValueError: bad thing") instead of the bare process "exit status 1". It
// is the error-side analogue of captchaObs: pumpEvents populates it while
// draining FD3, classifyOutcome consults the snapshot after the subprocess
// exits. Only the headline message is kept here; the traceback is folded
// into the emitted LogLine (see the "log" case) so it survives in the
// durable log without bloating the task's error field.
type errorObs struct {
	mu      sync.Mutex
	message string
}

func (e *errorObs) set(msg string) {
	e.mu.Lock()
	e.message = msg
	e.mu.Unlock()
}

func (e *errorObs) snapshot() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.message
}

// classifyOutcome is a pure function: given the parent/run contexts, the
// subprocess wait error, and whether we observed a captcha event during
// the run, decide the terminal Result. No I/O, no clock, no goroutines —
// 100% unit-testable.
//
// The captcha-overrides-exit branch is the whole reason this is its own
// function: a spider that calls `self.captcha("...")` and then raises
// must still land as `captcha_blocked`, not `failed`. Slice 16a's retry
// policy hard-blocks `captcha` — flipping the class here is what keeps
// the captcha task from being retried as an `exit` failure.
//
// lastErr is the last ERROR-level log line captured from FD3 (the spider's
// uncaught-exception message, e.g. "ValueError: bad thing"). When the
// subprocess exited non-zero we prefer it over the bare "exit status 1" so
// the task actually records why the Python failed. ErrorClass stays "exit"
// — the failure was still an uncaught process exit, and that's the class
// the retry policy keys on.
func classifyOutcome(parentCtxErr, runCtxErr, waitErr error, seenCaptcha bool, captchaMsg, lastErr string) Result {
	switch {
	case errors.Is(parentCtxErr, context.Canceled):
		return Result{State: pb.TaskState_TASK_STATE_CANCELLED}
	case errors.Is(runCtxErr, context.DeadlineExceeded):
		return Result{State: pb.TaskState_TASK_STATE_TIMEOUT, Error: "task timed out", ErrorClass: "timeout"}
	case waitErr != nil:
		if seenCaptcha {
			return Result{State: pb.TaskState_TASK_STATE_CAPTCHA, Error: captchaMsg, ErrorClass: "captcha"}
		}
		err := lastErr
		if err == "" {
			err = waitErr.Error()
		}
		return Result{State: pb.TaskState_TASK_STATE_FAILED, Error: err, ErrorClass: "exit"}
	default:
		// Clean exit. If captcha fired, the task is captcha_blocked even
		// though the script returned successfully.
		if seenCaptcha {
			return Result{State: pb.TaskState_TASK_STATE_CAPTCHA, Error: captchaMsg, ErrorClass: "captcha"}
		}
		return Result{State: pb.TaskState_TASK_STATE_SUCCEEDED}
	}
}

// prepareDir downloads the spider source zip and extracts it into a fresh
// per-task directory under e.workDir.
func (e *TaskExecutor) prepareDir(ctx context.Context, a *pb.AssignTask) (string, error) {
	if a.SourceKey == "" {
		return "", errors.New("assign has no source_key")
	}
	zipBytes, err := e.store.Download(ctx, a.SourceKey)
	if err != nil {
		return "", fmt.Errorf("download source: %w", err)
	}
	if err := os.MkdirAll(e.workDir, 0o755); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(e.workDir, fmt.Sprintf("task-%d-*", a.TaskId))
	if err != nil {
		return "", err
	}
	if err := unzipTo(zipBytes, dir); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("unzip: %w", err)
	}
	return dir, nil
}

func unzipTo(data []byte, dir string) error {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, f := range r.File {
		// Reject paths that escape the destination dir (zip-slip).
		fp := filepath.Join(dir, f.Name)
		if !isWithin(dir, fp) {
			return fmt.Errorf("invalid path in zip: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(fp, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(fp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			out.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		rc.Close()
		out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

func isWithin(parent, child string) bool {
	p, _ := filepath.Abs(parent)
	c, _ := filepath.Abs(child)
	return p == c || (len(c) > len(p) && c[:len(p)] == p && c[len(p)] == filepath.Separator)
}

// ----------------------------------------------------------------------------
// resolveInterpreter — per-spider venv cache, keyed by requirements.txt hash.
//
// If the extracted source has no `requirements.txt`, we run with the system
// interpreter (e.pyPath) — current behavior, no surprise.
//
// Otherwise we hash the file contents, take the first 16 hex chars, and look
// up `<venvDir>/<hash>/bin/python`. If it exists, we use it. If it doesn't,
// and `uv` is installed, we `uv venv` + `uv pip install -r requirements.txt
// crawlerkit[selenium]` and stream the install output back to the task log as
// `[deps] …` lines so authors see progress live.
//
// If `uv` is NOT installed, we surface one warning into the task log and
// fall back to the system interpreter — the spider will likely ImportError,
// but the worker still boots and dep-free spiders keep working.
// ----------------------------------------------------------------------------
func (e *TaskExecutor) resolveInterpreter(ctx context.Context, dir string, taskID int64, outbox chan<- *pb.WorkerMsg) (string, error) {
	reqPath := filepath.Join(dir, "requirements.txt")
	reqBytes, err := os.ReadFile(reqPath)
	if errors.Is(err, os.ErrNotExist) {
		return e.pyPath, nil
	}
	if err != nil {
		return "", fmt.Errorf("read requirements.txt: %w", err)
	}
	if e.uvPath == "" {
		emitDepsLog(outbox, taskID, "WARN",
			"requirements.txt present but uv is not installed; spider deps will not be resolved")
		return e.pyPath, nil
	}

	sum := sha256.Sum256(reqBytes)
	hash := hex.EncodeToString(sum[:])[:16]
	venv := filepath.Join(e.venvDir, hash)
	pyExe := filepath.Join(venv, "bin", "python")

	// Serialize concurrent installs for the same hash. The outer map is
	// only mutated under venvMu; the per-hash mutex is locked outside.
	e.venvMu.Lock()
	mu, ok := e.venvLocks[hash]
	if !ok {
		mu = &sync.Mutex{}
		e.venvLocks[hash] = mu
	}
	e.venvMu.Unlock()

	mu.Lock()
	defer mu.Unlock()

	if _, err := os.Stat(pyExe); err == nil {
		return pyExe, nil // cache hit
	}

	emitDepsLog(outbox, taskID, "INFO", fmt.Sprintf("resolving spider deps into %s", venv))

	if err := os.MkdirAll(e.venvDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir venv parent: %w", err)
	}

	// `uv venv` is idempotent over a fresh path. We DON'T pre-remove the
	// dir, because a partially-built venv from a previous crashed install
	// would already be there — `uv venv` reinitializes it.
	if err := e.runUVStreamed(ctx, taskID, outbox,
		"venv", venv, "--python", e.pyPath); err != nil {
		_ = os.RemoveAll(venv)
		return "", fmt.Errorf("uv venv: %w", err)
	}

	installArgs := []string{
		"pip", "install",
		"--python", pyExe,
		"-r", reqPath,
		"crawlerkit[selenium]",
	}
	if err := e.runUVStreamed(ctx, taskID, outbox, installArgs...); err != nil {
		_ = os.RemoveAll(venv)
		return "", fmt.Errorf("uv pip install: %w", err)
	}

	// Sanity check: the install command may have "succeeded" yet not
	// produced a python binary if e.pyPath itself was broken.
	if _, err := os.Stat(pyExe); err != nil {
		_ = os.RemoveAll(venv)
		return "", fmt.Errorf("venv interpreter missing after install: %s", pyExe)
	}
	return pyExe, nil
}

// runUVStreamed invokes uv with the given args, forwarding stdout+stderr
// line-by-line into the task log prefixed `[deps] `. Blocks until uv exits.
func (e *TaskExecutor) runUVStreamed(ctx context.Context, taskID int64, outbox chan<- *pb.WorkerMsg, args ...string) error {
	cmd := exec.CommandContext(ctx, e.uvPath, args...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start uv: %w", err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); streamUV(stdout, outbox, taskID, "INFO", e.log) }()
	go func() { defer wg.Done(); streamUV(stderr, outbox, taskID, "INFO", e.log) }()
	wg.Wait()
	return cmd.Wait()
}

func streamUV(r io.Reader, outbox chan<- *pb.WorkerMsg, taskID int64, level string, log *slog.Logger) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1024*1024)
	for sc.Scan() {
		emitDepsLog(outbox, taskID, level, sc.Text())
	}
	// EOF is expected; anything else (e.g. an oversize uv output line) would
	// silently truncate the remaining install output.
	if err := sc.Err(); err != nil {
		log.Warn("uv output stream read", "task", taskID, "err", err)
	}
}

func emitDepsLog(outbox chan<- *pb.WorkerMsg, taskID int64, level, message string) {
	outbox <- &pb.WorkerMsg{Payload: &pb.WorkerMsg_LogLine{LogLine: &pb.LogLine{
		TaskId: taskID, TsNs: time.Now().UnixNano(),
		Level: level, Message: "[deps] " + message,
	}}}
}

func killGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	// Negative pid → kill the whole process group.
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// ----------------------------------------------------------------------------
// FD 3 pump: parses JSONL events and translates each to a WorkerMsg.
// ----------------------------------------------------------------------------

type rawEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func pumpEvents(r io.Reader, taskID int64, outbox chan<- *pb.WorkerMsg, captcha *captchaObs, errObs *errorObs, log *slog.Logger) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev rawEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			log.Warn("event parse", "err", err, "raw", string(sc.Bytes()))
			continue
		}
		switch ev.Type {
		case "log":
			var d struct {
				Level   string `json:"level"`
				Message string `json:"message"`
				TsNs    int64  `json:"ts_ns"`
				Fields  struct {
					Traceback string `json:"traceback"`
				} `json:"fields"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			if d.TsNs == 0 {
				d.TsNs = time.Now().UnixNano()
			}
			// The runner emits the uncaught exception as an ERROR log with
			// the traceback nested under fields.traceback. Capture the
			// headline message for the terminal Result, and fold the
			// traceback into the emitted LogLine so it survives in the
			// durable (MinIO) + live (Redis) log — the proto LogLine has no
			// fields slot, so the message is the only channel.
			if strings.EqualFold(d.Level, "ERROR") && errObs != nil {
				errObs.set(d.Message)
			}
			msg := d.Message
			if d.Fields.Traceback != "" {
				msg = msg + "\n" + d.Fields.Traceback
			}
			outbox <- &pb.WorkerMsg{Payload: &pb.WorkerMsg_LogLine{LogLine: &pb.LogLine{
				TaskId: taskID, TsNs: d.TsNs, Level: d.Level, Message: msg,
			}}}
		case "item":
			var d struct {
				Payload json.RawMessage `json:"payload"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			outbox <- &pb.WorkerMsg{Payload: &pb.WorkerMsg_Item{Item: &pb.ItemEmitted{
				TaskId: taskID, PayloadJson: []byte(d.Payload),
			}}}
		case "shot":
			var d struct {
				Name   string `json:"name"`
				Key    string `json:"key"`
				URL    string `json:"url"`
				Bytes  int    `json:"bytes"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			outbox <- &pb.WorkerMsg{Payload: &pb.WorkerMsg_Artifact{Artifact: &pb.ArtifactRef{
				TaskId: taskID, Kind: "screenshot", Name: d.Name, StorageKey: d.Key,
				Url: d.URL, Width: int32(d.Width), Height: int32(d.Height), Bytes: int32(d.Bytes),
			}}}
		case "captcha":
			var d struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			if captcha != nil {
				captcha.set(d.Message)
			}
			// Surface in the live log tail so operators see *why* the
			// task is captcha_blocked without digging into the error
			// field.
			outbox <- &pb.WorkerMsg{Payload: &pb.WorkerMsg_LogLine{LogLine: &pb.LogLine{
				TaskId: taskID, TsNs: time.Now().UnixNano(),
				Level: "INFO", Message: "[captcha] " + d.Message,
			}}}
			// Tell the master right away. classifyOutcome will send an
			// idempotent terminal update later (same state, same class,
			// same message) so the order between this frame and the
			// final one doesn't matter.
			outbox <- &pb.WorkerMsg{Payload: &pb.WorkerMsg_TaskUpdate{TaskUpdate: &pb.TaskUpdate{
				TaskId: taskID, State: pb.TaskState_TASK_STATE_CAPTCHA,
				Error: d.Message, ErrorClass: "captcha",
			}}}
		default:
			log.Warn("unknown event type", "type", ev.Type)
		}
	}
	// Scan stops on EOF (nil) or on a read/buffer error. A too-long line
	// (bufio.ErrTooLong) would otherwise silently drop the rest of the FD3
	// event stream, so surface it.
	if err := sc.Err(); err != nil {
		log.Warn("event stream read", "task", taskID, "err", err)
	}
}

func pumpUserLog(r io.Reader, taskID int64, level string, outbox chan<- *pb.WorkerMsg, log *slog.Logger) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1024*1024)
	for sc.Scan() {
		outbox <- &pb.WorkerMsg{Payload: &pb.WorkerMsg_LogLine{LogLine: &pb.LogLine{
			TaskId: taskID, TsNs: time.Now().UnixNano(),
			Level: level, Message: sc.Text(),
		}}}
	}
	// EOF is the expected stop; anything else (e.g. bufio.ErrTooLong from a
	// single oversize print) would silently truncate the remaining user log.
	if err := sc.Err(); err != nil {
		log.Warn("user log stream read", "task", taskID, "stream", level, "err", err)
	}
}
