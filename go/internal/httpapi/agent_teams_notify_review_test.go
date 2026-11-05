package httpapi

import (
	"github.com/kiongahq/platform/internal/agentteam"
	"github.com/kiongahq/platform/internal/store"
	"testing"
	"time"
)

func TestTeamCompletionNotificationStable(t *testing.T) {
	h := newTeamHarness(t, store.New())
	run := agentteam.Run{ID: "notify-run", CoordinatorID: "coordinator"}
	h.saveWorker(agentteam.Worker{ID: run.CoordinatorID, Role: agentteam.RoleCoordinator, Status: agentteam.WorkerIdle})
	worker := agentteam.Worker{ID: "worker", Role: agentteam.RoleWorker, UpdatedAt: h.clock.now()}
	agentteam.Complete(&worker, "first")
	h.saveWorker(worker)
	h.engine.notifyCoordinator(run, worker)
	worker.UpdatedAt = worker.UpdatedAt.Add(time.Minute)
	h.engine.notifyCoordinator(run, worker)
	if got := len(h.worker(run.CoordinatorID).Inbox); got != 1 {
		t.Fatalf("one completion delivered %d times", got)
	}
	if err := agentteam.AddInbox(&worker, agentteam.InboxMessage{ID: "resume", Body: "continue"}); err != nil {
		t.Fatal(err)
	}
	first := worker
	agentteam.Complete(&worker, "second")
	h.saveWorker(worker)
	h.engine.notifyCoordinator(run, first)
	if !h.worker(worker.ID).NotifyPending {
		t.Fatal("old delivery cleared newer completion")
	}
	h.engine.notifyCoordinator(run, worker)
	if got := len(h.worker(run.CoordinatorID).Inbox); got != 2 {
		t.Fatalf("two completions delivered %d times", got)
	}
}
