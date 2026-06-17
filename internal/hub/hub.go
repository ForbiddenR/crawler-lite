// Package hub implements the master-side gRPC server for WorkerHub.
//
// Workers open a long-lived bidi stream and identify themselves with a Hello
// frame; the master answers with Welcome and registers the session. From then
// on, the master pushes AssignTask / CancelTask / Ping; the worker pushes
// Heartbeat / TaskUpdate / LogLine / ItemEmitted / ArtifactRef.
//
// Week 1: registry + Hello/Welcome + heartbeat counting + TaskUpdate routing.
// Week 2 brings real assignment, log/item/artifact sinks.
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

// WorkerHub is the gRPC server.
type WorkerHub struct {
	pb.UnimplementedWorkerHubServer

	log *slog.Logger

	// resolved via BindTaskService after TaskService is constructed.
	taskSvc TaskService

	mu       sync.RWMutex
	sessions map[string]*Session // keyed by session_id

	// secret enforced if non-empty (week 1: optional)
	sharedSecret string
}

// New constructs the hub. The shared secret is checked against Hello.shared_secret.
// If empty, no auth is enforced (dev only).
func New(log *slog.Logger) *WorkerHub {
	return &WorkerHub{
		log:      log,
		sessions: make(map[string]*Session),
	}
}

// BindTaskService closes the cycle with the task package.
func (h *WorkerHub) BindTaskService(s TaskService) { h.taskSvc = s }

// SetSharedSecret enables auth. Empty disables.
func (h *WorkerHub) SetSharedSecret(s string) { h.sharedSecret = s }

// Sessions returns a snapshot of currently-connected workers (for /api/workers).
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

	// Inbound loop: read frames, dispatch by oneof type.
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
		case *pb.WorkerMsg_LogLine:
			// Week 2: forward to LogSink. For now, just log on the master.
			h.log.Debug("worker log",
				"task", p.LogLine.TaskId,
				"level", p.LogLine.Level,
				"msg", p.LogLine.Message,
			)
		case *pb.WorkerMsg_Item:
			h.log.Debug("worker item", "task", p.Item.TaskId, "bytes", len(p.Item.PayloadJson))
		case *pb.WorkerMsg_Artifact:
			h.log.Debug("worker artifact", "task", p.Artifact.TaskId, "kind", p.Artifact.Kind, "key", p.Artifact.StorageKey)
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
	close(s.outbox)
}

// ----------------------------------------------------------------------------
// task.Hub interface (assignment + cancel)
// ----------------------------------------------------------------------------

// Assign is a stub for week 1 — picks the first session with free slots and
// pushes an AssignTask. Real selection (least-loaded, capability filtering,
// proxy attach) lands in week 2.
func (h *WorkerHub) Assign(ctx context.Context, t *task.Task) (bool, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.sessions {
		if s.FreeSlots > 0 {
			s.mu.Lock()
			s.running[t.ID] = struct{}{}
			s.FreeSlots--
			s.mu.Unlock()
			select {
			case s.outbox <- &pb.MasterMsg{
				Payload: &pb.MasterMsg_Assign{
					Assign: &pb.AssignTask{
						TaskId:        t.ID,
						SpiderId:      t.SpiderID,
						SpiderVersion: t.SpiderVersion,
						TimeoutS:      600,
					},
				},
			}:
				return true, nil
			default:
				// outbox full; try next worker
			}
		}
	}
	return false, nil
}

// CancelRunning broadcasts CancelTask to whichever worker is running this task.
func (h *WorkerHub) CancelRunning(ctx context.Context, taskID int64) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.sessions {
		s.mu.Lock()
		_, has := s.running[taskID]
		s.mu.Unlock()
		if !has {
			continue
		}
		select {
		case s.outbox <- &pb.MasterMsg{
			Payload: &pb.MasterMsg_Cancel{
				Cancel: &pb.CancelTask{TaskId: taskID},
			},
		}:
		default:
		}
		return nil
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
