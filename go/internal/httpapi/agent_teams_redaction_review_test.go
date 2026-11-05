package httpapi

import (
	"context"
	"encoding/json"
	"github.com/kiongahq/platform/internal/agentteam"
	"github.com/kiongahq/platform/internal/store"
	"strings"
	"testing"
)

func TestTeamRedactionLifecycle(t *testing.T) {
	h := newTeamHarness(t, store.New())
	secret := "sk-" + strings.Repeat("q", 24)
	team := h.team([]string{h.definition("one", nil)}, nil)
	raw := h.api("POST", "/api/v1/team-runs", mustJSON(t, map[string]any{"team_id": team, "input": secret}), 202)
	run := h.run(decodeTeam[teamRunView](t, raw).ID)
	if strings.Contains(string(raw), secret) || strings.Contains(run.Input, secret) {
		t.Error("run input leaked")
	}
	for _, path := range []string{"/api/v1/team-runs", "/api/v1/team-runs/" + run.ID} {
		if strings.Contains(string(h.api("GET", path, "", 200)), secret) {
			t.Error("run response leaked input")
		}
	}
	worker, lease, _, _ := h.engine.acquireLease(run, run.CoordinatorID, false)
	worker, err := h.engine.apply(lease, func(w *agentteam.Worker) error { agentteam.Deliver(w, randomToken); return nil })
	if err != nil {
		t.Fatal(err)
	}
	h.runner.script = func(_ context.Context, request turnRequest) (turnResponse, error) {
		if strings.Contains(mustJSON(t, request.Messages), secret) {
			t.Error("secret re-sent to model")
		}
		return turnResponse{Content: secret, ToolCalls: []agentteam.ModelCall{{Name: agentteam.ToolSendMessage, Arguments: json.RawMessage(mustJSON(t, map[string]string{"text": secret}))}}}, nil
	}
	worker, ok := h.engine.modelStep(context.Background(), run, worker, lease)
	if !ok {
		t.Fatal("model step")
	}
	if strings.Contains(worker.Transcript[len(worker.Transcript)-1].Content, secret) {
		t.Error("model content persisted unredacted")
	}
	call, _ := agentteam.PendingCall(worker)
	if !strings.Contains(string(call.Arguments), secret) {
		t.Fatal("pending arguments lost real value")
	}
	// Simulate a restart after a side effect began: recovery must settle it, not replay it.
	_, err = h.engine.apply(lease, func(w *agentteam.Worker) error { agentteam.MarkStarted(w, call.ID, h.clock.now()); return nil })
	if err != nil {
		t.Fatal(err)
	}
	h.clock.advance(2 * teamLeaseTTL)
	worker, lease, _, _ = h.engine.acquireLease(run, worker.ID, false)
	if lease == nil {
		t.Fatal("resume lease")
	}
	// Gateway calls can retry; explicitly settle as the execution path would.
	worker, ok = h.engine.settleCall(run, worker, lease, call, toolFailure("test", "done"))
	if !ok {
		t.Fatal("settle")
	}
	if strings.Contains(mustJSON(t, h.worker(worker.ID).Transcript), secret) {
		t.Error("settled transcript retains secret")
	}
	h.engine.modelStep(context.Background(), run, worker, lease)
}

func TestTeamRealArgumentsExecuteThenRedact(t *testing.T) {
	h := newTeamHarness(t, store.New())
	team := h.team([]string{h.definition("one", sandboxMember(sandboxGrant))}, nil)
	secret := "sk-" + strings.Repeat("r", 24)
	h.runner.set(workerScript("one", func(_ context.Context, r turnRequest) (turnResponse, error) {
		if r.Turn == 1 {
			return reply(secret, toolCall(agentteam.ToolWriteFile, map[string]string{"path": "value.txt", "content": secret})), nil
		}
		if strings.Contains(mustJSON(t, r.Messages), secret) {
			t.Error("settled arguments re-sent")
		}
		return reply("", finishCall("done")), nil
	}))
	runID := h.start(team, false)
	h.drive(runID)
	if h.sandbox.files["value.txt"] != secret {
		t.Fatal("execution lost real argument")
	}
	if strings.Contains(mustJSON(t, h.workerNamed(runID, "one").Transcript), secret) {
		t.Fatal("stored settled arguments leaked")
	}
}

func TestTeamCrashSettlementRedactsArguments(t *testing.T) {
	h := newTeamHarness(t, store.New())
	team := h.team([]string{h.definition("one", sandboxMember(sandboxGrant))}, nil)
	runID := h.spawnOne(team, false)
	worker := h.workerNamed(runID, "one")
	secret := "sk-" + strings.Repeat("s", 24)
	agentteam.ApplyReply(&worker, agentteam.ModelReply{ToolCalls: []agentteam.ModelCall{toolCall(agentteam.ToolWriteFile, map[string]string{"path": "value.txt", "content": secret})}})
	call, _ := agentteam.PendingCall(worker)
	agentteam.MarkStarted(&worker, call.ID, h.clock.now())
	h.saveWorker(worker)
	recovered, lease, _, unknown := h.engine.acquireLease(h.run(runID), worker.ID, false)
	if lease == nil || len(unknown) != 1 {
		t.Fatalf("crash rule not applied: lease=%v unknown=%v", lease, unknown)
	}
	if strings.Contains(mustJSON(t, recovered.Transcript), secret) {
		t.Fatal("crash settlement retained arguments")
	}
	if !strings.Contains(mustJSON(t, recovered.Transcript), agentteam.ErrOutcomeUnknown) {
		t.Fatal("missing unknown outcome")
	}
}
