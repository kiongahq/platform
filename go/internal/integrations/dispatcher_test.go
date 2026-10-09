package integrations

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	platformapi "github.com/kiongahq/platform/pkg/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestDispatcherCreatesAgentCRD(t *testing.T) {
	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	dispatcher := NewDispatcher(client, "team-a")
	resource, _ := json.Marshal(map[string]any{
		"name": "support", "version": "1", "image": "support:1", "graph_module": "agents.support:graph",
		"replicas": 2, "autoscaling": map[string]any{"min_replicas": 2, "max_replicas": 5},
		"resources":   map[string]any{"cpu": "1", "memory": "2Gi", "gpu": 1, "gpu_type": "nvidia.com/gpu"},
		"llm_backend": "self-hosted", "tools": []string{"lookup"}, "canary_weight": 10,
	})
	command, _ := json.Marshal(LifecycleCommand{ID: "agt-1", Kind: "agent", Action: "agent.deployed", Resource: resource, Tenant: "team-a"})
	if err := dispatcher.Dispatch(context.Background(), KafkaRecord{Topic: "mlaiops.agent.commands", Value: command}); err != nil {
		t.Fatal(err)
	}
	agent, err := client.Resource(schema.GroupVersionResource{Group: "mlaiops.io", Version: "v1alpha1", Resource: "kiongaagents"}).Namespace("team-a").Get(context.Background(), platformapi.AgentDNSName("agt-1"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	maximum, _, _ := unstructured.NestedInt64(agent.Object, "spec", "replicas", "max")
	gpu, _, _ := unstructured.NestedInt64(agent.Object, "spec", "resources", "requests", "nvidia.com/gpu")
	if maximum != 5 || gpu != 1 {
		t.Fatalf("agent autoscaling/resources were not dispatched: %#v", agent.Object["spec"])
	}
}

func TestDispatcherKeepsSameNamedAgentsIndependent(t *testing.T) {
	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	dispatcher := NewDispatcher(client, "team-a")
	resource, _ := json.Marshal(map[string]any{
		"name": "support", "version": "1", "image": "support:1", "graph_module": "agents.support:graph",
		"autoscaling": map[string]any{"min_replicas": 1, "max_replicas": 1},
	})
	for _, id := range []string{"agt-project-a", "agt-project-b"} {
		command, _ := json.Marshal(LifecycleCommand{ID: id, Kind: "agent", Action: "agent.deployed", Resource: resource, Tenant: "team-a"})
		if err := dispatcher.Dispatch(context.Background(), KafkaRecord{Topic: "mlaiops.agent.commands", Value: command}); err != nil {
			t.Fatal(err)
		}
	}
	resourceClient := client.Resource(schema.GroupVersionResource{Group: "mlaiops.io", Version: "v1alpha1", Resource: "kiongaagents"}).Namespace("team-a")
	first, err := resourceClient.Get(context.Background(), platformapi.AgentDNSName("agt-project-a"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := resourceClient.Get(context.Background(), platformapi.AgentDNSName("agt-project-b"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.GetName() == second.GetName() {
		t.Fatalf("same-named control-plane agents collided: %s", first.GetName())
	}
}

func TestDispatcherRejectsGPUTypeCollidingWithNativeResource(t *testing.T) {
	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	dispatcher := NewDispatcher(client, "team-a")
	resource, _ := json.Marshal(map[string]any{
		"name": "malicious", "version": "1", "image": "agent:1", "graph_module": "agents.bad:graph",
		"autoscaling": map[string]any{"min_replicas": 1, "max_replicas": 1},
		"resources":   map[string]any{"cpu": "500m", "memory": "1Gi", "gpu": 8, "gpu_type": "cpu"},
	})
	command, _ := json.Marshal(LifecycleCommand{ID: "agt-malicious", Kind: "agent", Action: "agent.deployed", Resource: resource, Tenant: "team-a"})
	err := dispatcher.Dispatch(context.Background(), KafkaRecord{Topic: "mlaiops.agent.commands", Value: command})
	if err == nil || !strings.Contains(err.Error(), "collides with a native Kubernetes resource") {
		t.Fatalf("malformed GPU resource event was not rejected: %v", err)
	}
	resourceClient := client.Resource(schema.GroupVersionResource{Group: "mlaiops.io", Version: "v1alpha1", Resource: "kiongaagents"}).Namespace("team-a")
	if _, getErr := resourceClient.Get(context.Background(), platformapi.AgentDNSName("agt-malicious"), metav1.GetOptions{}); getErr == nil {
		t.Fatal("dispatcher must not create a CR from a malformed GPU resource event")
	}
}

func TestDispatcherUpsertsAndDeletesWorkspaceCRD(t *testing.T) {
	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	dispatcher := NewDispatcher(client, "mlaiops-workspaces")
	resource, _ := json.Marshal(map[string]any{
		"subject": "oidc|user@example.com", "services": []string{"workbench", "ide"}, "disabled": false,
		"compute": map[string]any{"vcpus": 4, "memory_gb": 8, "gpus": 0, "gpu_type": "", "max_vms": 2},
		"storage": map[string]any{"size_gb": 100},
	})
	command, _ := json.Marshal(LifecycleCommand{ID: "oidc|user@example.com", Kind: "user_access", Action: "access.upserted", Resource: resource, Tenant: "team-a"})
	record := KafkaRecord{Topic: "mlaiops.workspace.commands", Value: command}
	if err := dispatcher.Dispatch(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Dispatch(context.Background(), record); err != nil {
		t.Fatalf("workspace upsert must be idempotent: %v", err)
	}
	gvr := schema.GroupVersionResource{Group: "mlaiops.io", Version: "v1alpha1", Resource: "kiongaworkspaces"}
	workspace, err := client.Resource(gvr).Namespace("mlaiops-workspaces").Get(context.Background(), "workspace-oidc-user-example-com", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memory, _, _ := unstructured.NestedInt64(workspace.Object, "spec", "compute", "memoryGB")
	if memory != 8 {
		t.Fatalf("unexpected workspace compute: %#v", workspace.Object["spec"])
	}
	deleted, _ := json.Marshal(LifecycleCommand{ID: "oidc|user@example.com", Kind: "user_access", Action: "access.deleted", Resource: json.RawMessage(`{"subject":"oidc|user@example.com"}`), Tenant: "team-a"})
	if err := dispatcher.Dispatch(context.Background(), KafkaRecord{Topic: "mlaiops.workspace.commands", Value: deleted}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resource(gvr).Namespace("mlaiops-workspaces").Get(context.Background(), "workspace-oidc-user-example-com", metav1.GetOptions{}); err == nil {
		t.Fatal("workspace should be deleted when access is revoked")
	}
}
