// Package hub implements the master-side gRPC server for WorkerHub.
//
// Workers open a long-lived bidi stream and identify themselves with a Hello
// frame; the master answers with Welcome and registers the session. From then
// on, the master pushes AssignTask / CancelTask / Ping; the worker pushes
// Heartbeat / TaskUpdate / LogLine / ItemEmitted / ArtifactRef.
package hub

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	pb "github.com/yourteam/crawler-lite/internal/pb/worker/v1"
	"github.com/yourteam/crawler-lite/internal/task"
)

// TaskService is the slice of *task.Service this package depends on. Defined
// here to break the cycle between `task` and `hub`.
type TaskService interface {
	OnUpdate(ctx context.Context, taskID int64, status task.Status, errMsg, errClass, workerID string) error
}

// Sinks bundles the side-effect handlers for the streamed worker output.
// Constructed once in app.Build and passed in.
type Sinks struct {
	Log      LogSink
	Item     ItemSink
	Artifact ArtifactSink
}

// LogSink takes streamed log lines from a worker and persists / fans them out.
// The implementation lives in sinks.go.
type LogSink interface {
	Write(ctx context.Context, line *pb.LogLine) error
}

type ItemSink interface {
	Write(ctx context.Context, taskID, spiderID int64, payload []byte) error
}

type ArtifactSink interface {
	Write(ctx context.Context, ref *pb.ArtifactRef) error
}

// Session represents a connected worker.
type Session struct {
	WorkerID     string
	SessionID    string
	Capabilities []string
	Concurrency  int32
	FreeSlots    int32
	RunningTasks int32

	outbox chan *pb.MasterMsg
	cancel context.CancelFunc

	mu      sync.Mutex
	running map[int64]struct{}
}

// taskMeta records the master's view of which task is on which worker, plus
// the spider id (so the ItemSink can persist items without a second DB hit).
type taskMeta struct {
	sessionID string
	spiderID  int64
}

// WorkerHub is the gRPC server.
type WorkerHub struct {
	pb.UnimplementedWorkerHubServer

	log   *slog.Logger
	sinks Sinks

	taskSvc TaskService

	mu       sync.RWMutex
	sessions map[string]*Session // keyed by session_id
	tasks    map[int64]taskMeta  // task_id → metadata

	sharedSecret string
}

// New constructs the hub. Sinks are required (use no-op implementations in
// tests if you don't care about side effects).
func New(log *slog.Logger, sinks Sinks) *WorkerHub {
	return &WorkerHub{
		log:      log,
		sinks:    sinks,
		sessions: make(map[string]*Session),
		tasks:    make(map[int64]taskMeta),
	}
}

// BindTaskService closes the cycle with the task package.
func (h *WorkerHub) BindTaskService(s TaskService) { h.taskSvc = s }

// SetSharedSecret enables auth. Empty disables.
func (h *WorkerHub) SetSharedSecret(s string) { h.sharedSecret = s }

// Sessions returns a snapshot of currently-connected workers.
func (h *WorkerHub) Sessions() []*Session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*Session, 0, len(h.sessions))
	for _, s := range h.sessions {
		out = append(out, s)
	}
	return out
}

// ----------------------------------------------------------------------------
// gRPC method
// ----------------------------------------------------------------------------

// Connect is the long-lived bidi stream a worker opens.
func (h *WorkerHub) Connect(stream pb.WorkerHub_ConnectServer) error {
	ctx := stream.Context()

	// First frame must be Hello.
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return errors.New("first frame must be Hello")
	}
	if h.sharedSecret != "" && hello.SharedSecret != h.sharedSecret {
		h.log.Warn("worker auth failed", "worker_id", hello.WorkerId)
		return errors.New("invalid shared secret")
	}

	// Register session.
	sessionID := uuid.NewString()
	streamCtx, cancel := context.WithCancel(ctx)
	sess := &Session{
		WorkerID:     hello.WorkerId,
		SessionID:    sessionID,
		Capabilities: hello.Capabilities,
		Concurrency:  hello.Concurrency,
		FreeSlots:    hello.Concurrency,
		outbox:       make(chan *pb.MasterMsg, 64),
		cancel:       cancel,
		running:      make(map[int64]struct{}),
	}
	h.register(sess)
	defer h.unregister(sess)

	h.log.Info("worker connected",
		"worker_id", hello.WorkerId,
		"session", sessionID,
		"version", hello.Version,
		"concurrency", hello.Concurrency,
	)

	// Send Welcome.
	if err := stream.Send(&pb.MasterMsg{
		Payload: &pb.MasterMsg_Welcome{
			Welcome: &pb.Welcome{SessionId: sessionID},
		},
	}); err != nil {
		return err
	}

	// Outbound pump: drain `outbox` to the wire.
	pumpDone := make(chan error, 1)
	go func() {
		pumpDone <- h.pumpOutbox(streamCtx, sess, stream)
	}()

	// Inbound loop.
	if err := h.readLoop(streamCtx, sess, stream); err != nil {
		cancel()
		<-pumpDone
		h.log.Info("worker disconnected", "session", sessionID, "err", err)
		return err
	}
	cancel()
	<-pumpDone
	h.log.Info("worker disconnected", "session", sessionID)
	return nil
}

func (h *WorkerHub) readLoop(ctx context.Context, sess *Session, stream pb.WorkerHub_ConnectServer) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		switch p := msg.Payload.(type) {
		case *pb.WorkerMsg_Hello:
			h.log.Warn("unexpected Hello after registration", "session", sess.SessionID)

		case *pb.WorkerMsg_Heartbeat:
			sess.RunningTasks = p.Heartbeat.RunningTasks
			sess.FreeSlots = p.Heartbeat.FreeSlots

		case *pb.WorkerMsg_TaskUpdate:
			if h.taskSvc != nil {
				_ = h.taskSvc.OnUpdate(ctx,
					p.TaskUpdate.TaskId,
					mapState(p.TaskUpdate.State),
					p.TaskUpdate.Error,
					p.TaskUpdate.ErrorClass,
					sess.WorkerID,
				)
			}
			if isTerminal(p.TaskUpdate.State) {
				h.releaseTask(sess, p.TaskUpdate.TaskId)
			}

		case *pb.WorkerMsg_LogLine:
			if h.sinks.Log != nil {
				if err := h.sinks.Log.Write(ctx, p.LogLine); err != nil {
					h.log.Warn("log sink", "task", p.LogLine.TaskId, "err", err)
				}
			}

		case *pb.WorkerMsg_Item:
			if h.sinks.Item != nil {
				meta, ok := h.lookupTask(p.Item.TaskId)
				if !ok {
					h.log.Warn("item for unknown task", "task", p.Item.TaskId)
					continue
				}
				if err := h.sinks.Item.Write(ctx, p.Item.TaskId, meta.spiderID, p.Item.PayloadJson); err != nil {
					h.log.Warn("item sink", "task", p.Item.TaskId, "err", err)
				}
			}

		case *pb.WorkerMsg_Artifact:
			if h.sinks.Artifact != nil {
				if err := h.sinks.Artifact.Write(ctx, p.Artifact); err != nil {
					h.log.Warn("artifact sink", "task", p.Artifact.TaskId, "err", err)
				}
			}

		default:
			h.log.Warn("unknown WorkerMsg payload", "session", sess.SessionID)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (h *WorkerHub) pumpOutbox(ctx context.Context, sess *Session, stream pb.WorkerHub_ConnectServer) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m, ok := <-sess.outbox:
			if !ok {
				return nil
			}
			if err := stream.Send(m); err != nil {
				return err
			}
		}
	}
}

func (h *WorkerHub) register(s *Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[s.SessionID] = s
}

func (h *WorkerHub) unregister(s *Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, s.SessionID)
	for tid, meta := range h.tasks {
		if meta.sessionID == s.SessionID {
			delete(h.tasks, tid)
		}
	}
	close(s.outbox)
}

func (h *WorkerHub) lookupTask(taskID int64) (taskMeta, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	m, ok := h.tasks[taskID]
	return m, ok
}

func (h *WorkerHub) releaseTask(sess *Session, taskID int64) {
	sess.mu.Lock()
	delete(sess.running, taskID)
	if sess.FreeSlots < sess.Concurrency {
		sess.FreeSlots++
	}
	sess.mu.Unlock()

	h.mu.Lock()
	delete(h.tasks, taskID)
	h.mu.Unlock()
}

// ----------------------------------------------------------------------------
// task.Hub interface (assignment + cancel)
// ----------------------------------------------------------------------------

// Assign picks the first worker with free slots, decrements its slot counter,
// and pushes the AssignTask. Returns false if no worker has capacity.
//
// Worker selection is deliberately simple — first-fit. Least-loaded /
// capability-aware selection lands when capacity becomes a concern.
func (h *WorkerHub) Assign(ctx context.Context, a *pb.AssignTask) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sessions {
		s.mu.Lock()
		if s.FreeSlots <= 0 {
			s.mu.Unlock()
			continue
		}
		s.running[a.TaskId] = struct{}{}
		s.FreeSlots--
		s.mu.Unlock()

		select {
		case s.outbox <- &pb.MasterMsg{
			Payload: &pb.MasterMsg_Assign{Assign: a},
		}:
			h.tasks[a.TaskId] = taskMeta{sessionID: s.SessionID, spiderID: a.SpiderId}
			return true, nil
		default:
			// Outbox full — undo our reservation and try the next worker.
			s.mu.Lock()
			delete(s.running, a.TaskId)
			s.FreeSlots++
			s.mu.Unlock()
		}
	}
	return false, nil
}

// CancelRunning broadcasts CancelTask to whichever worker is running this task.
func (h *WorkerHub) CancelRunning(ctx context.Context, taskID int64) error {
	h.mu.RLock()
	meta, ok := h.tasks[taskID]
	sess := h.sessions[meta.sessionID]
	h.mu.RUnlock()
	if !ok || sess == nil {
		return nil
	}
	select {
	case sess.outbox <- &pb.MasterMsg{
		Payload: &pb.MasterMsg_Cancel{Cancel: &pb.CancelTask{TaskId: taskID}},
	}:
	default:
	}
	return nil
}

func mapState(s pb.TaskState) task.Status {
	switch s {
	case pb.TaskState_TASK_STATE_RUNNING:
		return task.StatusRunning
	case pb.TaskState_TASK_STATE_SUCCEEDED:
		return task.StatusSucceeded
	case pb.TaskState_TASK_STATE_FAILED:
		return task.StatusFailed
	case pb.TaskState_TASK_STATE_CANCELLED:
		return task.StatusCancelled
	case pb.TaskState_TASK_STATE_TIMEOUT:
		return task.StatusTimeout
	case pb.TaskState_TASK_STATE_CAPTCHA:
		return task.StatusCaptchaBlocked
	default:
		return task.StatusQueued
	}
}

func isTerminal(s pb.TaskState) bool {
	switch s {
	case pb.TaskState_TASK_STATE_SUCCEEDED,
		pb.TaskState_TASK_STATE_FAILED,
		pb.TaskState_TASK_STATE_CANCELLED,
		pb.TaskState_TASK_STATE_TIMEOUT,
		pb.TaskState_TASK_STATE_CAPTCHA:
		return true
	}
	return false
}
