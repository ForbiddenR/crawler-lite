package hub

import (
	"testing"

	pb "github.com/yourteam/crawler-lite/internal/pb/worker/v1"
	"github.com/yourteam/crawler-lite/internal/task"
)

func TestMapStateAcceptedIsRunning(t *testing.T) {
	if got := mapState(pb.TaskState_TASK_STATE_ACCEPTED); got != task.StatusRunning {
		t.Fatalf("mapState(ACCEPTED) = %q, want %q", got, task.StatusRunning)
	}
}

func TestMapStateUnknownIsIgnored(t *testing.T) {
	if got := mapState(pb.TaskState_TASK_STATE_UNSPECIFIED); got != "" {
		t.Fatalf("mapState(UNSPECIFIED) = %q, want empty status", got)
	}
}
