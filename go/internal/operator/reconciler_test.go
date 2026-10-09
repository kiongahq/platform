package operator

import "testing"

import platformapi "github.com/kiongahq/platform/pkg/api"

func TestReconcileAgentBuildsWorkloadAndTraffic(t *testing.T) {
	plan, err := ReconcileAgent(AgentSpec{
		ResourceID: "agt-support-v2", Name: "support", Namespace: "team-a", Version: "2.1.0", Image: "registry/support:2.1.0",
		GraphModule: "agents.support:graph", MinReplicas: 2, MaxReplicas: 10,
		LLMBackend: "self-hosted", CanaryWeight: 15, StableRef: "support-1-9-0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Workload.Name != platformapi.AgentDNSName("agt-support-v2") || plan.Workload.Replicas != 2 {
		t.Fatalf("unexpected workload: %#v", plan.Workload)
	}
	if plan.Traffic.StableWeight != 85 || plan.Traffic.CanaryWeight != 15 {
		t.Fatalf("unexpected traffic: %#v", plan.Traffic)
	}
	if len(plan.Workload.Containers) != 2 {
		t.Fatal("agent workload must include trace proxy sidecar")
	}
}

func TestReconcileAgentRejectsUnsafeTraffic(t *testing.T) {
	_, err := ReconcileAgent(AgentSpec{Name: "x", Namespace: "x", Version: "1", Image: "x", GraphModule: "x", MinReplicas: 1, MaxReplicas: 1, CanaryWeight: 110})
	if err == nil {
		t.Fatal("expected validation error")
	}
}
