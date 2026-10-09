package store

import (
	"testing"

	"github.com/kiongahq/platform/pkg/api"
)

func TestOperationalLifecycle(t *testing.T) {
	repository := New()
	run, err := repository.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: "prj-demo", Name: "quality"}, "engineer")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := repository.CancelRun(run.ID, "engineer")
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel: %#v %v", cancelled, err)
	}
	retry, err := repository.RetryRun(run.ID, "engineer")
	if err != nil || retry.ParentRunID != run.ID {
		t.Fatalf("retry: %#v %v", retry, err)
	}

	model, err := repository.RegisterModel(api.RegisterModelRequest{ProjectID: "prj-demo", Name: "risk", Version: "1", ArtifactURI: "s3://models/risk/1", Metrics: map[string]float64{"accuracy": .91}}, "engineer")
	if err != nil {
		t.Fatal(err)
	}
	model, err = repository.PromoteModel(model.ID, "production", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	deployed, err := repository.DeployModel(model.ID, 10, "operator")
	if err != nil || deployed.CanaryWeight != 10 {
		t.Fatalf("deploy: %#v %v", deployed, err)
	}
	rolledBack, err := repository.RollbackModel(model.ID, "operator")
	if err != nil || rolledBack.DeploymentStatus != "rolled_back" {
		t.Fatalf("rollback: %#v %v", rolledBack, err)
	}

	trace, err := repository.RecordTrace(api.RecordTraceRequest{AgentID: "agt-1", SessionID: "session-1", Status: "running", CurrentNode: "reason", InputTokens: 100, OutputTokens: 20, CostUSD: .01})
	if err != nil || trace.Tokens != 120 {
		t.Fatalf("trace: %#v %v", trace, err)
	}
	sessions := repository.AgentSessions("agt-1")
	if len(sessions) != 1 || sessions[0].Turns != 1 || sessions[0].InputTokens != 100 {
		t.Fatalf("sessions: %#v", sessions)
	}
}

func TestFailedGateBlocksProduction(t *testing.T) {
	repository := New()
	model, err := repository.RegisterModel(api.RegisterModelRequest{ProjectID: "prj-demo", Name: "weak", Version: "1", ArtifactURI: "s3://models/weak/1", Metrics: map[string]float64{"accuracy": .5}}, "engineer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PromoteModel(model.ID, "production", "reviewer"); err == nil {
		t.Fatal("expected quality gate failure")
	}
}
