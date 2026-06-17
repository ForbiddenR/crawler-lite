// Package runner is the worker-side runtime. Week 1 contract:
//
//   - Open one long-lived bidi stream to the master (with reconnect+backoff).
//   - Send Hello, receive Welcome.
//   - On AssignTask: spawn a goroutine that fakes a task lifecycle
//     (ACCEPTED → RUNNING → SUCCEEDED) and decrements/restores free slots.
//   - On CancelTask: cancel the goroutine for that task.
//   - Send a Heartbeat every 10s.
//
// Week 2 replaces fakeRun() with TaskExecutor that spawns python -m crawlerkit.runner.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/yourteam/crawler-lite/internal/pb/worker/v1"
)

type Config struct {
	MasterAddr   string
	WorkerID     string
	Concurrency  int32
	Capabilities []string
	SharedSecret string
}

type Worker struct {
	cfg Config
	log *slog.Logger

	// runtime state
	freeSlots    atomic.Int32
	runningCount atomic.Int32

	mu      sync.Mutex
	running map[int64]context.CancelFunc

	outbox chan *pb.WorkerMsg
}

func NewWorker(cfg Config, log *slog.Logger) *Worker {
	w := &Worker{
		cfg:     cfg,
		log:     log,
		running: make(map[int64]context.CancelFunc),
		outbox:  make(chan *pb.WorkerMsg, 64),
	}
	w.freeSlots.Store(cfg.Concurrency)
	return w
}

// Run is the connect-loop: dial master, hold the stream, reconnect on error
// with exponential backoff. Returns when ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := w.connectOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.log.Warn("worker disconnected, reconnecting",
				"err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (w *Worker) connectOnce(ctx context.Context) error {
	conn, err := grpc.NewClient(w.cfg.MasterAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	client := pb.NewWorkerHubClient(conn)

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := client.Connect(streamCtx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	// Send Hello.
	if err := stream.Send(&pb.WorkerMsg{
		Payload: &pb.WorkerMsg_Hello_{
			Hello: &pb.Hello{
				WorkerId:     w.cfg.WorkerID,
				Version:      "0.1.0",
				Concurrency:  w.cfg.Concurrency,
				Capabilities: w.cfg.Capabilities,
				SharedSecret: w.cfg.SharedSecret,
			},
		},
	}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	// Wait for Welcome (the master's first frame).
	first, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("recv welcome: %w", err)
	}
	wel := first.GetWelcome()
	if wel == nil {
		return errors.New("expected Welcome as first master frame")
	}
	w.log.Info("connected", "session", wel.SessionId)

	// Drain outbox to the wire.
	pumpDone := make(chan error, 1)
	go func() {
		pumpDone <- w.pumpOutbox(streamCtx, stream)
	}()

	// Heartbeat loop.
	hbDone := make(chan struct{})
	go func() {
		w.heartbeatLoop(streamCtx)
		close(hbDone)
	}()

	// Inbound loop.
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			cancel()
			<-pumpDone
			<-hbDone
			return err
		}
		w.handleMaster(streamCtx, msg)
	}
	cancel()
	<-pumpDone
	<-hbDone
	return nil
}

func (w *Worker) handleMaster(ctx context.Context, msg *pb.MasterMsg) {
	switch p := msg.Payload.(type) {
	case *pb.MasterMsg_Assign:
		w.startTask(ctx, p.Assign)
	case *pb.MasterMsg_Cancel:
		w.cancelTask(p.Cancel.TaskId)
	case *pb.MasterMsg_Ping:
		// Could echo back; for now ignore.
	default:
		w.log.Warn("unknown MasterMsg payload")
	}
}

// startTask runs the week-1 fake task lifecycle.
func (w *Worker) startTask(parent context.Context, a *pb.AssignTask) {
	w.log.Info("assignment received",
		"task", a.TaskId, "spider", a.SpiderId, "timeout_s", a.TimeoutS)

	// Reserve a slot.
	w.freeSlots.Add(-1)
	w.runningCount.Add(1)

	taskCtx, cancel := context.WithCancel(parent)
	w.mu.Lock()
	w.running[a.TaskId] = cancel
	w.mu.Unlock()

	// Send ACCEPTED then RUNNING.
	w.sendUpdate(a.TaskId, pb.TaskState_TASK_STATE_ACCEPTED, "")
	w.sendUpdate(a.TaskId, pb.TaskState_TASK_STATE_RUNNING, "")

	go func() {
		defer func() {
			w.mu.Lock()
			delete(w.running, a.TaskId)
			w.mu.Unlock()
			w.runningCount.Add(-1)
			w.freeSlots.Add(1)
		}()

		// Fake 2s of work.
		select {
		case <-taskCtx.Done():
			w.sendUpdate(a.TaskId, pb.TaskState_TASK_STATE_CANCELLED, "cancelled")
			return
		case <-time.After(2 * time.Second):
		}

		w.log.Info("task done (fake)", "task", a.TaskId)
		w.sendUpdate(a.TaskId, pb.TaskState_TASK_STATE_SUCCEEDED, "")
	}()
}

func (w *Worker) cancelTask(taskID int64) {
	w.mu.Lock()
	cancel, ok := w.running[taskID]
	w.mu.Unlock()
	if ok {
		cancel()
	}
}

func (w *Worker) sendUpdate(taskID int64, state pb.TaskState, errMsg string) {
	w.outbox <- &pb.WorkerMsg{
		Payload: &pb.WorkerMsg_TaskUpdate_{
			TaskUpdate: &pb.TaskUpdate{
				TaskId: taskID, State: state, Error: errMsg,
			},
		},
	}
}

func (w *Worker) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.outbox <- &pb.WorkerMsg{
				Payload: &pb.WorkerMsg_Heartbeat_{
					Heartbeat: &pb.Heartbeat{
						RunningTasks: w.runningCount.Load(),
						FreeSlots:    w.freeSlots.Load(),
					},
				},
			}
		}
	}
}

func (w *Worker) pumpOutbox(ctx context.Context, stream pb.WorkerHub_ConnectClient) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m := <-w.outbox:
			if err := stream.Send(m); err != nil {
				return err
			}
		}
	}
}
