// Package scheduler starts pipeline runs from schedule triggers.
//
// Guarantees:
//   - One leader: a lease document (row-locked in PostgreSQL) means only one
//     gateway replica fires schedules at a time.
//   - At most once per slot: each (definition, trigger, slot) is claimed with
//     a create-only document before the run is submitted, so restarts or a
//     lost lease cannot double-fire a slot.
//   - Bounded catch-up: after downtime, a schedule runs once for the most
//     recent missed slot instead of replaying every missed slot.
//   - Runs execute as the schedule owner so quotas and policy apply to them.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/kiongahq/platform/internal/pipelinespec"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

const (
	LeaseKind = "scheduler_lease"
	SlotKind  = "schedule_slot"
	leaseID   = "pipelines"
)

// Store is the subset of the repository the scheduler uses.
type Store interface {
	store.Documents
	PipelineDefinitions() []api.PipelineDefinition
	SetPipelineTriggerState(string, int, store.TriggerState) (api.PipelineDefinition, error)
}

// Submit starts a run for a scheduled slot and returns its id. It must apply
// the same authorization and quota checks as a manual submission, using the
// definition owner's identity.
type Submit func(ctx context.Context, definition api.PipelineDefinition, slot time.Time) (string, error)

type Scheduler struct {
	Store    Store
	Submit   Submit
	Holder   string
	Now      func() time.Time
	Interval time.Duration
	LeaseTTL time.Duration
	// Fired is called after every slot decision; tests and metrics use it.
	Fired func(Outcome)
}

// Outcome records what happened to one due slot.
type Outcome struct {
	DefinitionID string
	Trigger      int
	Slot         time.Time
	RunID        string
	Err          error
	Skipped      string
}

type lease struct {
	Holder    string    `json:"holder"`
	ExpiresAt time.Time `json:"expires_at"`
}

type slotClaim struct {
	DefinitionID string    `json:"definition_id"`
	Trigger      int       `json:"trigger"`
	Slot         time.Time `json:"slot"`
	Holder       string    `json:"holder"`
	RunID        string    `json:"run_id,omitempty"`
	Error        string    `json:"error,omitempty"`
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Run ticks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.Tick(ctx); err != nil && !errors.Is(err, errNotLeader) {
			log.Printf("scheduler tick failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

var errNotLeader = errors.New("another replica holds the scheduler lease")

// Tick fires every due schedule once. It is safe to call concurrently from
// several replicas.
func (s *Scheduler) Tick(ctx context.Context) error {
	if err := s.acquireLease(); err != nil {
		return err
	}
	now := s.now()
	for _, definition := range s.Store.PipelineDefinitions() {
		for index, trigger := range definition.Triggers {
			if trigger.Type != "schedule" || trigger.Paused {
				continue
			}
			if trigger.NextRunAt == nil {
				next, err := pipelinespec.NextRun(trigger.Cron, trigger.Timezone, now)
				if err == nil {
					_, err = s.Store.SetPipelineTriggerState(definition.ID, index, store.TriggerState{NextRunAt: &next, LastRunAt: trigger.LastRunAt, LastRunID: trigger.LastRunID})
				}
				if err != nil {
					s.report(Outcome{DefinitionID: definition.ID, Trigger: index, Err: err})
				}
				continue
			}
			if trigger.NextRunAt.After(now) {
				continue
			}
			s.fire(ctx, definition, index, trigger, now)
		}
	}
	return nil
}

// fire runs the most recent due slot and advances the schedule past now.
func (s *Scheduler) fire(ctx context.Context, definition api.PipelineDefinition, index int, trigger api.PipelineTrigger, now time.Time) {
	slot := *trigger.NextRunAt
	// Catch-up: walk forward to the latest slot that is still <= now.
	for {
		next, err := pipelinespec.NextRun(trigger.Cron, trigger.Timezone, slot)
		if err != nil || next.After(now) {
			break
		}
		slot = next
	}
	next, err := pipelinespec.NextRun(trigger.Cron, trigger.Timezone, now)
	if err != nil {
		s.report(Outcome{DefinitionID: definition.ID, Trigger: index, Slot: slot, Err: err})
		return
	}
	outcome := Outcome{DefinitionID: definition.ID, Trigger: index, Slot: slot}
	claimed, err := s.claim(definition.ID, index, slot)
	switch {
	case err != nil:
		outcome.Err = err
	case !claimed:
		outcome.Skipped = "slot already claimed"
	default:
		runID, submitErr := s.Submit(ctx, definition, slot)
		outcome.RunID, outcome.Err = runID, submitErr
		s.recordClaim(definition.ID, index, slot, runID, submitErr)
	}
	state := store.TriggerState{NextRunAt: &next, LastRunAt: trigger.LastRunAt, LastRunID: trigger.LastRunID}
	if outcome.RunID != "" {
		state.LastRunAt, state.LastRunID = &slot, outcome.RunID
	}
	if _, err := s.Store.SetPipelineTriggerState(definition.ID, index, state); err != nil && outcome.Err == nil {
		outcome.Err = err
	}
	s.report(outcome)
}

func slotID(definitionID string, index int, slot time.Time) string {
	return fmt.Sprintf("%s#%d@%d", definitionID, index, slot.Unix())
}

// claim creates the slot document; false means another tick already did.
func (s *Scheduler) claim(definitionID string, index int, slot time.Time) (bool, error) {
	claimed := false
	_, err := store.UpdateDoc(s.Store, SlotKind, slotID(definitionID, index, slot), func(current slotClaim, exists bool) (slotClaim, error) {
		if exists {
			return current, store.ErrSkipWrite
		}
		claimed = true
		return slotClaim{DefinitionID: definitionID, Trigger: index, Slot: slot, Holder: s.Holder}, nil
	}, "schedule.slot_claimed", "scheduler")
	return claimed, err
}

func (s *Scheduler) recordClaim(definitionID string, index int, slot time.Time, runID string, submitErr error) {
	_, _ = store.UpdateDoc(s.Store, SlotKind, slotID(definitionID, index, slot), func(current slotClaim, _ bool) (slotClaim, error) {
		current.RunID = runID
		if submitErr != nil {
			current.Error = submitErr.Error()
		}
		return current, nil
	}, "schedule.slot_fired", "scheduler")
}

func (s *Scheduler) acquireLease() error {
	ttl := s.LeaseTTL
	if ttl <= 0 {
		ttl = 45 * time.Second
	}
	now := s.now()
	_, err := store.UpdateDoc(s.Store, LeaseKind, leaseID, func(current lease, exists bool) (lease, error) {
		if exists && current.Holder != s.Holder && current.ExpiresAt.After(now) {
			return current, errNotLeader
		}
		return lease{Holder: s.Holder, ExpiresAt: now.Add(ttl)}, nil
	}, "", "scheduler")
	return err
}

func (s *Scheduler) report(outcome Outcome) {
	if outcome.Err != nil {
		log.Printf("schedule %s trigger %d slot %s: %v", outcome.DefinitionID, outcome.Trigger, outcome.Slot.Format(time.RFC3339), outcome.Err)
	}
	if s.Fired != nil {
		s.Fired(outcome)
	}
}
