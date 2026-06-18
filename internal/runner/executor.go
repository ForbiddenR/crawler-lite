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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	store    StorageClient
	pyPath   string // /usr/bin/python3 by default
	workDir  string // parent dir for per-task working dirs
	log      *slog.Logger
}

func NewTaskExecutor(store StorageClient, pyPath, workDir string, log *slog.Logger) *TaskExecutor {
	if pyPath == "" {
		pyPath = "python3"
	}
	if workDir == "" {
		workDir = "/tmp/crawler-lite"
	}
	return &TaskExecutor{store: store, pyPath: pyPath, workDir: workDir, log: log}
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

	cmd := exec.CommandContext(runCtx, e.pyPath, "-m", "crawlerkit.runner")
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
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); pumpEvents(eventR, a.TaskId, outbox, e.log) }()
	go func() { defer wg.Done(); pumpUserLog(stdout, a.TaskId, "INFO", outbox) }()
	go func() { defer wg.Done(); pumpUserLog(stderr, a.TaskId, "ERROR", outbox) }()

	// --- 6. Wait + classify outcome --------------------------------------
	waitErr := cmd.Wait()
	wg.Wait()

	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return Result{State: pb.TaskState_TASK_STATE_CANCELLED}
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		// Make sure no orphaned process group lingers.
		_ = killGroup(cmd.Process.Pid)
		return Result{State: pb.TaskState_TASK_STATE_TIMEOUT, Error: "task timed out", ErrorClass: "timeout"}
	case waitErr != nil:
		// If the SDK signalled a captcha block via FD 3, the captcha event
		// already went to the master; we mark the terminal state here.
		return Result{State: pb.TaskState_TASK_STATE_FAILED, Error: waitErr.Error(), ErrorClass: "exit"}
	default:
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

func pumpEvents(r io.Reader, taskID int64, outbox chan<- *pb.WorkerMsg, log *slog.Logger) {
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
			}
			_ = json.Unmarshal(ev.Data, &d)
			if d.TsNs == 0 {
				d.TsNs = time.Now().UnixNano()
			}
			outbox <- &pb.WorkerMsg{Payload: &pb.WorkerMsg_LogLine{LogLine: &pb.LogLine{
				TaskId: taskID, TsNs: d.TsNs, Level: d.Level, Message: d.Message,
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
			outbox <- &pb.WorkerMsg{Payload: &pb.WorkerMsg_TaskUpdate{TaskUpdate: &pb.TaskUpdate{
				TaskId: taskID, State: pb.TaskState_TASK_STATE_CAPTCHA, ErrorClass: "captcha",
			}}}
		default:
			log.Warn("unknown event type", "type", ev.Type)
		}
	}
}

func pumpUserLog(r io.Reader, taskID int64, level string, outbox chan<- *pb.WorkerMsg) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1024*1024)
	for sc.Scan() {
		outbox <- &pb.WorkerMsg{Payload: &pb.WorkerMsg_LogLine{LogLine: &pb.LogLine{
			TaskId: taskID, TsNs: time.Now().UnixNano(),
			Level: level, Message: sc.Text(),
		}}}
	}
}
