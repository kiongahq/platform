package httpapi

import (
	"context"
	"github.com/kiongahq/platform/internal/agentteam"
	"github.com/kiongahq/platform/internal/store"
	"testing"
	"time"
)

func TestTeamSlotReplacementSurvivesOldHolder(t *testing.T) {
	h := newTeamHarness(t, store.New())
	run := agentteam.Run{ID: "slot-run"}
	run.Snapshot.Team.Limits.MaxConcurrentWorkers = 1
	a, b := h.engine, h.newEngine("gateway-b")
	if !a.acquireSlot(run, "worker", "a") {
		t.Fatal("acquire a")
	}
	h.clock.advance(2 * teamLeaseTTL)
	if !b.acquireSlot(run, "worker", "b") {
		t.Fatal("acquire b")
	}
	if a.renewSlot(run.ID, "worker", "a") {
		t.Fatal("old holder renewed replacement")
	}
	if !b.renewSlot(run.ID, "worker", "b") {
		t.Fatal("replacement cannot renew")
	}
	h.saveWorker(agentteam.Worker{ID: "worker", LeaseToken: "lease-a"})
	a.renewEvery = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a.keepAlive(ctx, cancel, run, &teamLease{workerID: "worker", token: "lease-a", slotToken: "a"}, true)
	if ctx.Err() != context.Canceled {
		t.Fatal("holder did not stop after losing slot")
	}
	a.releaseSlot(run.ID, "worker", "a")
	if a.acquireSlot(run, "other", "other") {
		t.Fatal("old holder released replacement slot")
	}
}
