package store

import (
	"testing"

	"github.com/kiongahq/platform/pkg/api"
)

func submitTestRun(t *testing.T, s *Store) api.PipelineRun {
	t.Helper()
	project, err := s.CreateProject(api.CreateProjectRequest{Name: "step test project"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: project.ID, Name: "training-pipeline"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestUpdateRunStepDrivesRunLifecycle(t *testing.T) {
	s := New()
	run := submitTestRun(t, s)

	updated, err := s.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: "validate-data", Status: "running"}, "flow")
	if err != nil || updated.Status != "running" {
		t.Fatalf("run should be running: %v %v", updated.Status, err)
	}
	for _, step := range []string{"validate-data", "train-model", "evaluate"} {
		if updated, err = s.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: step, Status: "succeeded"}, "flow"); err != nil {
			t.Fatal(err)
		}
	}
	// register-model is still pending: the run must NOT complete early.
	if updated.Status != "running" {
		t.Fatalf("run must wait for every templated step: %s", updated.Status)
	}
	if updated, err = s.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: "register-model", Status: "succeeded"}, "flow"); err != nil {
		t.Fatal(err)
	}
	if updated.Status != "succeeded" || updated.Progress != 100 {
		t.Fatalf("run should be complete: status=%s progress=%d", updated.Status, updated.Progress)
	}
	if len(updated.Logs) < 5 {
		t.Fatalf("step transitions must be logged, got %d entries", len(updated.Logs))
	}
}

func TestUpdateRunStepFailureFailsRun(t *testing.T) {
	s := New()
	run := submitTestRun(t, s)
	updated, err := s.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: "train-model", Status: "failed", Message: "OOM"}, "flow")
	if err != nil || updated.Status != "failed" {
		t.Fatalf("run should fail with its step: %v %v", updated.Status, err)
	}
	// Terminal runs are not resurrected by late reports.
	late, err := s.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: "evaluate", Status: "succeeded"}, "flow")
	if err != nil || late.Status != "failed" {
		t.Fatalf("late report must not resurrect the run: %v %v", late.Status, err)
	}
}

func TestUpdateRunStepAppendsUnknownSteps(t *testing.T) {
	s := New()
	run := submitTestRun(t, s)
	updated, err := s.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: "publish-report", Status: "succeeded"}, "flow")
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Steps) != 5 {
		t.Fatalf("dynamic step should be appended, got %d steps", len(updated.Steps))
	}
	if updated.Progress != 20 {
		t.Fatalf("progress must count completed/total: %d", updated.Progress)
	}
}

func TestUpdateRunStepValidation(t *testing.T) {
	s := New()
	run := submitTestRun(t, s)
	if _, err := s.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: "x", Status: "sideways"}, "flow"); err == nil {
		t.Fatal("invalid status must be rejected")
	}
	if _, err := s.UpdateRunStep("run-missing", api.UpdateRunStepRequest{Step: "x", Status: "running"}, "flow"); err != ErrNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestPipelineRunResultsDoNotAliasStoredExecutionState(t *testing.T) {
	s := New()
	project, err := s.CreateProject(api.CreateProjectRequest{Name: "run ownership project"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.SubmitPipeline(api.SubmitPipelineRequest{
		ProjectID:  project.ID,
		Parameters: map[string]any{"nested": map[string]any{"value": "original"}},
	}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	run.Steps[0].Status = "caller-mutated"
	run.Steps[0].DependsOn = append(run.Steps[0].DependsOn, "caller-dependency")
	run.Parameters["nested"].(map[string]any)["value"] = "caller-mutated"
	run.Logs[0].Message = "caller-mutated"

	stored, err := s.Run(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Steps[0].Status == "caller-mutated" ||
		len(stored.Steps[0].DependsOn) != 0 ||
		stored.Parameters["nested"].(map[string]any)["value"] != "original" ||
		stored.Logs[0].Message == "caller-mutated" {
		t.Fatalf("returned run aliases stored state: %#v", stored)
	}

	listed := s.Runs()
	listed[0].Steps[0].Status = "list-mutated"
	reloaded, err := s.Run(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Steps[0].Status == "list-mutated" {
		t.Fatal("Runs returned a step slice backed by repository state")
	}
}

func TestSessionsScopedPerAgent(t *testing.T) {
	s := New()
	// Same session id reported by two different agents must produce two
	// separate sessions, each with its own accounting.
	for _, agentID := range []string{"agt-1", "agt-2"} {
		if _, err := s.RecordTrace(api.RecordTraceRequest{AgentID: agentID, SessionID: "shared-id", Status: "succeeded", InputTokens: 5, OutputTokens: 5}); err != nil {
			t.Fatal(err)
		}
	}
	first, second := s.AgentSessions("agt-1"), s.AgentSessions("agt-2")
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("each agent must own its session: %d %d", len(first), len(second))
	}
	if first[0].Turns != 1 || second[0].Turns != 1 {
		t.Fatalf("turns must not leak across agents: %d %d", first[0].Turns, second[0].Turns)
	}
}

func TestSetRunEngine(t *testing.T) {
	s := New()
	run := submitTestRun(t, s)
	linked, err := s.SetRunEngine(run.ID, "fr-123")
	if err != nil || linked.EngineRunID != "fr-123" {
		t.Fatalf("engine link failed: %v %v", linked.EngineRunID, err)
	}
}
