package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

type harness struct {
	store   *store.Store
	clock   time.Time
	mu      sync.Mutex
	runs    []time.Time
	failing bool
}

func newHarness(t *testing.T, cron string, start time.Time) (*harness, api.PipelineDefinition) {
	t.Helper()
	h := &harness{store: store.New(), clock: start}
	project, err := h.store.CreateProject(api.CreateProjectRequest{Name: "Scheduler project"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := h.store.UpsertPipelineDefinition("", api.UpsertPipelineDefinitionRequest{
		ProjectID: project.ID, Name: "nightly", Version: "1",
		Triggers: []api.PipelineTrigger{{Type: "schedule", Cron: cron, Timezone: "UTC"}},
		Jobs:     []api.PipelineJob{{Name: "a", Kind: "container", Image: "busybox"}},
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// Definitions compute NextRunAt from the wall clock; pin it to the fake clock.
	next := time.Date(start.Year(), start.Month(), start.Day(), start.Hour()+1, 0, 0, 0, time.UTC)
	if _, err := h.store.SetPipelineTriggerState(definition.ID, 0, store.TriggerState{NextRunAt: &next}); err != nil {
		t.Fatal(err)
	}
	return h, definition
}

func (h *harness) scheduler(holder string) *Scheduler {
	return &Scheduler{Store: h.store, Holder: holder, Now: func() time.Time { return h.clock }, Submit: func(_ context.Context, d api.PipelineDefinition, slot time.Time) (string, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.failing {
			return "", errors.New("quota exceeded")
		}
		h.runs = append(h.runs, slot)
		return fmt.Sprintf("run-%d", len(h.runs)), nil
	}}
}

func TestFiresOnceAtTheSlotAndAdvances(t *testing.T) {
	h, definition := newHarness(t, "0 * * * *", time.Date(2026, 10, 9, 10, 30, 0, 0, time.UTC))
	s := h.scheduler("a")
	if err := s.Tick(context.Background()); err != nil || len(h.runs) != 0 {
		t.Fatalf("fired early: %v %v", err, h.runs)
	}
	h.clock = time.Date(2026, 10, 9, 11, 0, 5, 0, time.UTC)
	_ = s.Tick(context.Background())
	_ = s.Tick(context.Background())
	if len(h.runs) != 1 || !h.runs[0].Equal(time.Date(2026, 10, 9, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("runs: %v", h.runs)
	}
	updated, _ := h.store.PipelineDefinition(definition.ID)
	trigger := updated.Triggers[0]
	if trigger.LastRunID != "run-1" || !trigger.NextRunAt.Equal(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("trigger state: %+v", trigger)
	}
	if updated.Revision != definition.Revision {
		t.Fatal("scheduler bookkeeping created a definition revision")
	}
}

func TestCatchUpRunsOnlyTheLatestMissedSlot(t *testing.T) {
	h, _ := newHarness(t, "0 * * * *", time.Date(2026, 10, 9, 10, 30, 0, 0, time.UTC))
	h.clock = time.Date(2026, 10, 9, 16, 20, 0, 0, time.UTC) // five slots missed
	_ = h.scheduler("a").Tick(context.Background())
	if len(h.runs) != 1 || !h.runs[0].Equal(time.Date(2026, 10, 9, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("catch-up runs: %v", h.runs)
	}
}

func TestOnlyTheLeaseHolderFiresAndSlotsNeverDoubleFire(t *testing.T) {
	h, _ := newHarness(t, "0 * * * *", time.Date(2026, 10, 9, 11, 0, 1, 0, time.UTC))
	h.clock = time.Date(2026, 10, 9, 12, 0, 1, 0, time.UTC)
	first, second := h.scheduler("a"), h.scheduler("b")
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = first.Tick(context.Background()) }()
		go func() { defer wg.Done(); _ = second.Tick(context.Background()) }()
	}
	wg.Wait()
	if len(h.runs) != 1 {
		t.Fatalf("slot fired %d times", len(h.runs))
	}
	// Whichever replica ticked last holds the lease; let it lapse so the
	// exclusion check below starts from a known holder.
	h.clock = h.clock.Add(time.Minute)
	if err := first.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := second.Tick(context.Background()); !errors.Is(err, errNotLeader) {
		t.Fatalf("second replica not excluded: %v", err)
	}
	if err := first.Tick(context.Background()); err != nil {
		t.Fatalf("holder could not renew: %v", err)
	}
	h.clock = h.clock.Add(2 * time.Minute) // holder stopped; lease expired
	if err := second.Tick(context.Background()); err != nil {
		t.Fatalf("lease not taken over after expiry: %v", err)
	}
}

func TestPausedSchedulesDoNotFireAndFailuresAreReported(t *testing.T) {
	h, definition := newHarness(t, "0 * * * *", time.Date(2026, 10, 9, 11, 0, 1, 0, time.UTC))
	req := api.UpsertPipelineDefinitionRequest{ProjectID: definition.ProjectID, Name: definition.Name, Version: definition.Version,
		Triggers: []api.PipelineTrigger{{Type: "schedule", Cron: "0 * * * *", Timezone: "UTC", Paused: true}}, Jobs: definition.Jobs}
	if _, err := h.store.UpsertPipelineDefinition(definition.ID, req, "owner"); err != nil {
		t.Fatal(err)
	}
	_ = h.scheduler("a").Tick(context.Background())
	if len(h.runs) != 0 {
		t.Fatal("paused schedule fired")
	}
	req.Triggers[0].Paused = false
	if _, err := h.store.UpsertPipelineDefinition(definition.ID, req, "owner"); err != nil {
		t.Fatal(err)
	}
	next := time.Date(2026, 10, 9, 11, 0, 0, 0, time.UTC)
	_, _ = h.store.SetPipelineTriggerState(definition.ID, 0, store.TriggerState{NextRunAt: &next})
	h.failing = true
	var outcomes []Outcome
	s := h.scheduler("a")
	s.Fired = func(o Outcome) { outcomes = append(outcomes, o) }
	_ = s.Tick(context.Background())
	if len(outcomes) != 1 || outcomes[0].Err == nil {
		t.Fatalf("failure not reported: %+v", outcomes)
	}
	updated, _ := h.store.PipelineDefinition(definition.ID)
	if !updated.Triggers[0].NextRunAt.After(next) {
		t.Fatal("failed slot is retried forever instead of advancing")
	}
	claim, err := store.GetDoc[slotClaim](h.store, SlotKind, slotID(definition.ID, 0, next))
	if err != nil || claim.Error != "quota exceeded" {
		t.Fatalf("slot claim does not record the failure: %+v %v", claim, err)
	}
}

func TestLeaseRenewalIsNotAudited(t *testing.T) {
	h, _ := newHarness(t, "0 * * * *", time.Date(2026, 10, 9, 10, 30, 0, 0, time.UTC))
	before := len(h.store.Audit())
	s := h.scheduler("a")
	for i := 0; i < 5; i++ {
		_ = s.Tick(context.Background())
	}
	if after := len(h.store.Audit()); after != before {
		t.Fatalf("idle ticks wrote %d audit events", after-before)
	}
}
