// Repository contract tests shared by local persistence behavior.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiongahq/platform/pkg/api"
)

func TestStorePersistsResourcesAndAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.json")
	first := New(path)
	project, err := first.CreateProject(api.CreateProjectRequest{Name: "Churn risk"}, "test-user")
	if err != nil {
		t.Fatal(err)
	}
	if project.Template != "production-ml" || project.TemplateVersion == "" || project.Framework == "" || project.ScaffoldCommand == "" {
		t.Fatalf("project template was not resolved: %#v", project)
	}
	for _, value := range []string{"--template-version 1.0.0", "--framework scikit-learn", "--accelerator cpu", "--profile power"} {
		if !strings.Contains(project.ScaffoldCommand, value) {
			t.Fatalf("scaffold command %q is missing %q", project.ScaffoldCommand, value)
		}
	}
	model, err := first.RegisterModel(api.RegisterModelRequest{
		ProjectID: project.ID, Name: "churn", Version: "1", ArtifactURI: "s3://models/churn/1",
		ServingImage: " ghcr.io/acme/churn-xgboost:1 ",
	}, "test-user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.PromoteModel(model.ID, "production", "reviewer"); err != nil {
		t.Fatal(err)
	}

	reloaded := New(path)
	if got := reloaded.Models(); len(got) != 1 || got[0].Stage != "production" || got[0].ServingImage != "ghcr.io/acme/churn-xgboost:1" {
		t.Fatalf("unexpected persisted models: %#v", got)
	}
	if got := reloaded.Audit(); len(got) != 3 || got[0].Actor != "reviewer" {
		t.Fatalf("unexpected audit events: %#v", got)
	}
}

func TestRegisterModelRejectsUnsafeServingImage(t *testing.T) {
	repository := New()
	for _, image := range []string{"   ", "ghcr.io/acme/model:1\n", "ghcr.io/acme/bad image:1"} {
		_, err := repository.RegisterModel(api.RegisterModelRequest{
			ProjectID: "prj-demo", Name: "risk", Version: "1", ArtifactURI: "models:/risk/1", ServingImage: image,
		}, "test-user")
		if err == nil {
			t.Fatalf("expected serving image %q to be rejected", image)
		}
	}
}

func TestProjectNamespaceIsUniqueAfterNormalization(t *testing.T) {
	repository := New()
	if _, err := repository.CreateProject(api.CreateProjectRequest{Name: "Risk Model"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateProject(api.CreateProjectRequest{Name: "risk-model"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("normalized namespace collision should return ErrConflict, got %v", err)
	}
}

func TestStoreValidatesAgentTraffic(t *testing.T) {
	s := New()
	agent, err := s.DeployAgent(api.DeployAgentRequest{
		ProjectID: "prj-demo", Name: "support", Version: "1", Image: "example/agent:1",
		GraphModule: "agents.support:graph", Autoscaling: api.AgentAutoscaling{MinReplicas: 2, MaxReplicas: 5},
		Resources: api.AgentResources{CPU: "1", Memory: "2Gi", GPU: 1}, OwnerSubject: "engineer@example.com",
	}, "test-user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAgentTraffic(agent.ID, 101, "test-user"); err == nil {
		t.Fatal("expected canary validation error")
	}
	if agent.Replicas != 2 || agent.Autoscaling.MaxReplicas != 5 || agent.Resources.GPUType != "nvidia.com/gpu" || agent.OwnerSubject != "engineer@example.com" {
		t.Fatalf("agent workload defaults were not normalized: %#v", agent)
	}
	invalid := api.DeployAgentRequest{ProjectID: "prj-demo", Name: "bad", Version: "1", Image: "bad:1", GraphModule: "bad:graph", Autoscaling: api.AgentAutoscaling{MinReplicas: 3, MaxReplicas: 2}}
	if _, err := s.DeployAgent(invalid, "test-user"); err == nil {
		t.Fatal("expected invalid autoscaling bounds to be rejected")
	}
	invalid = api.DeployAgentRequest{ProjectID: "prj-demo", Name: "bad-gpu", Version: "1", Image: "bad:1", GraphModule: "bad:graph", Resources: api.AgentResources{GPU: 1, GPUType: "not a resource"}}
	if _, err := s.DeployAgent(invalid, "test-user"); err == nil {
		t.Fatal("expected invalid GPU resource type to be rejected")
	}
}

func TestAgentGPUTypeMustBeAnExtendedResourceName(t *testing.T) {
	base := api.DeployAgentRequest{
		ProjectID: "prj-demo", Name: "gpu-agent", Version: "1", Image: "example/agent:1",
		GraphModule: "agents.gpu:graph", Resources: api.AgentResources{GPU: 1},
	}
	for _, resourceName := range []string{
		"cpu", "memory", "gpu", "kubernetes.io/gpu", "devices.k8s.io/gpu", "not a domain/gpu",
	} {
		req := base
		req.Resources.GPUType = resourceName
		if err := NormalizeAgentRequest(&req); err == nil {
			t.Errorf("expected GPU resource name %q to be rejected", resourceName)
		}
	}
	req := base
	req.Resources.GPUType = " vendor.example.com/accelerator "
	if err := NormalizeAgentRequest(&req); err != nil {
		t.Fatalf("valid extended resource name was rejected: %v", err)
	}
	if req.Resources.GPUType != "vendor.example.com/accelerator" {
		t.Fatalf("GPU resource name was not trimmed: %q", req.Resources.GPUType)
	}
}

func TestFileStoreNormalizesLegacyAgentRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-platform.json")
	legacy := `{
  "projects": [{"id": "prj-demo", "name": "Demo"}],
  "agents": [{
    "id": "agt-legacy", "project_id": "prj-demo", "owner_subject": "owner@example.com",
    "name": "legacy", "version": "1", "image": "example/legacy:1",
    "graph_module": "agents.legacy:graph", "replicas": 2,
    "resources": {"gpu": 1, "gpu_type": "cpu"}
  }]
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	agents := New(path).Agents()
	if len(agents) != 1 {
		t.Fatalf("unexpected agents: %#v", agents)
	}
	agent := agents[0]
	if agent.Replicas != 2 || agent.Autoscaling.MinReplicas != 2 || agent.Autoscaling.MaxReplicas != 2 {
		t.Fatalf("legacy autoscaling was not normalized: %#v", agent.Autoscaling)
	}
	if agent.Resources.CPU != "500m" || agent.Resources.Memory != "1Gi" || agent.Resources.GPUType != "nvidia.com/gpu" {
		t.Fatalf("legacy resources were not normalized: %#v", agent.Resources)
	}
	if agent.LLMBackend != "mock" || agent.OwnerSubject != "owner@example.com" || agent.Tools == nil {
		t.Fatalf("legacy record fields were not normalized or preserved: %#v", agent)
	}
}

func TestPostgresDecoderNormalizesLegacyAgentRecords(t *testing.T) {
	raw := []byte(`{
  "id": "agt-postgres", "project_id": "prj-demo", "owner_subject": "db-owner@example.com",
  "name": "legacy", "version": "1", "image": "example/legacy:1",
  "graph_module": "agents.legacy:graph",
  "resources": {"cpu": " ", "memory": "invalid", "gpu": 1, "gpu_type": "memory"}
}`)
	agent, err := decodeStoredValue[api.Agent](raw)
	if err != nil {
		t.Fatal(err)
	}
	if agent.Replicas != 1 || agent.Autoscaling.MinReplicas != 1 || agent.Autoscaling.MaxReplicas != 1 {
		t.Fatalf("Postgres legacy autoscaling was not normalized: %#v", agent)
	}
	if agent.Resources.CPU != "500m" || agent.Resources.Memory != "1Gi" || agent.Resources.GPUType != "nvidia.com/gpu" || agent.OwnerSubject != "db-owner@example.com" {
		t.Fatalf("Postgres legacy record was not normalized: %#v", agent)
	}
}

func TestAgentOwnerIsPersistedButCannotBeSuppliedInJSON(t *testing.T) {
	req := api.DeployAgentRequest{OwnerSubject: "trusted@example.com"}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "trusted@example.com") || strings.Contains(string(raw), "owner_subject") {
		t.Fatalf("owner subject must be server-controlled: %s", raw)
	}
	agentRaw, err := json.Marshal(api.Agent{OwnerSubject: req.OwnerSubject})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agentRaw), `"owner_subject":"trusted@example.com"`) {
		t.Fatalf("agent owner subject must be visible: %s", agentRaw)
	}
}

func TestFileStorePersistsAgentOwnerFromTrustedRequestField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.json")
	repository := New(path)
	deployed, err := repository.DeployAgent(api.DeployAgentRequest{
		ProjectID: "prj-demo", OwnerSubject: "owner@example.com", Name: "owned-agent",
		Version: "1", Image: "example/owned:1", GraphModule: "agents.owned:graph",
	}, "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if deployed.OwnerSubject != "owner@example.com" {
		t.Fatalf("deployed owner was not retained: %#v", deployed)
	}
	reloaded := New(path).Agents()
	if len(reloaded) != 1 || reloaded[0].OwnerSubject != "owner@example.com" {
		t.Fatalf("persisted owner was not restored: %#v", reloaded)
	}
}
