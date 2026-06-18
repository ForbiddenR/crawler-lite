// Package runner is the worker-side runtime.
//
// Week 2 contract:
//
//   - Open one long-lived bidi stream to the master (with reconnect+backoff).
//   - Send Hello, receive Welcome.
//   - On AssignTask: TaskExecutor downloads source, spawns python, pumps
//     log/item/screenshot events back through the outbox, and returns a
//     terminal Result that the worker forwards as a final TaskUpdate.
//   - On CancelTask: cancel the task's context (the executor's runCtx
//     observes the parent ctx and SIGTERMs the python subprocess).
//   - Send a Heartbeat every 10s.
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
	cfg  Config
	log  *slog.Logger
	exec *TaskExecutor

	freeSlots    atomic.Int32
	runningCount atomic.Int32

	mu      sync.Mutex
	running map[int64]context.CancelFunc

	outbox chan *pb.WorkerMsg
}

// NewWorker constructs a Worker; pass a TaskExecutor or nil. If nil, the
// worker runs in "fake mode" (week-1 behavior) which is still useful for
// integration tests of the gRPC layer.
func NewWorker(cfg Config, exec *TaskExecutor, log *slog.Logger) *Worker {
	w := &Worker{
		cfg:     cfg,
		log:     log,
		exec:    exec,
		running: make(map[int64]context.CancelFunc),
		outbox:  make(chan *pb.WorkerMsg, 64),
	}
	w.freeSlots.Store(cfg.Concurrency)
	return w
}

// Run is the connect-loop. Returns when ctx is cancelled.
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

	if err := stream.Send(&pb.WorkerMsg{
		Payload: &pb.WorkerMsg_Hello{
			Hello: &pb.Hello{
				WorkerId:     w.cfg.WorkerID,
				Version:      "0.2.0",
				Concurrency:  w.cfg.Concurrency,
				Capabilities: w.cfg.Capabilities,
				SharedSecret: w.cfg.SharedSecret,
			},
		},
	}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	first, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("recv welcome: %w", err)
	}
	wel := first.GetWelcome()
	if wel == nil {
		return errors.New("expected Welcome as first master frame")
	}
	w.log.Info("connected", "session", wel.SessionId)

	pumpDone := make(chan error, 1)
	go func() { pumpDone <- w.pumpOutbox(streamCtx, stream) }()

	hbDone := make(chan struct{})
	go func() {
		w.heartbeatLoop(streamCtx)
		close(hbDone)
	}()

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
		// no-op
	default:
		w.log.Warn("unknown MasterMsg payload")
	}
}

// startTask spawns a goroutine that runs the task through the TaskExecutor
// (or the fake lifecycle if no executor was provided). When the executor
// returns, we send the terminal TaskUpdate so the master can transition the
// task to its final status.
func (w *Worker) startTask(parent context.Context, a *pb.AssignTask) {
	w.log.Info("assignment received",
		"task", a.TaskId, "spider", a.SpiderId, "timeout_s", a.TimeoutS)

	w.freeSlots.Add(-1)
	w.runningCount.Add(1)

	taskCtx, cancel := context.WithCancel(parent)
	w.mu.Lock()
	w.running[a.TaskId] = cancel
	w.mu.Unlock()

	w.sendUpdate(a.TaskId, pb.TaskState_TASK_STATE_ACCEPTED, "", "")
	w.sendUpdate(a.TaskId, pb.TaskState_TASK_STATE_RUNNING, "", "")

	go func() {
		defer func() {
			w.mu.Lock()
			delete(w.running, a.TaskId)
			w.mu.Unlock()
			w.runningCount.Add(-1)
			w.freeSlots.Add(1)
		}()

		var res Result
		if w.exec != nil {
			res = w.exec.Run(taskCtx, a, w.outbox)
		} else {
			// Fallback fake lifecycle: useful for testing the gRPC plumbing
			// in environments without Python.
			select {
			case <-taskCtx.Done():
				res = Result{State: pb.TaskState_TASK_STATE_CANCELLED}
			case <-time.After(2 * time.Second):
				res = Result{State: pb.TaskState_TASK_STATE_SUCCEEDED}
			}
		}

		w.sendUpdate(a.TaskId, res.State, res.Error, res.ErrorClass)
		w.log.Info("task done", "task", a.TaskId, "state", res.State.String())
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

func (w *Worker) sendUpdate(taskID int64, state pb.TaskState, errMsg, errClass string) {
	w.outbox <- &pb.WorkerMsg{
		Payload: &pb.WorkerMsg_TaskUpdate{
			TaskUpdate: &pb.TaskUpdate{
				TaskId: taskID, State: state, Error: errMsg, ErrorClass: errClass,
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
				Payload: &pb.WorkerMsg_Heartbeat{
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
