package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ml-ai-ops/platform/internal/auth"
	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

const goodDigest = "registry.example/team/train@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func preflightFixture(t *testing.T, overridable *api.Overridable) (*Server, api.PipelineDefinition) {
	t.Helper()
	t.Setenv("PREFECT_API_URL", "http://prefect.invalid/api")
	data := store.New()
	project, err := data.CreateProject(api.CreateProjectRequest{Name: "Preflight project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := data.UpsertPipelineDefinition("", api.UpsertPipelineDefinitionRequest{
		ProjectID: project.ID, Name: "train", Version: "1", Overridable: overridable,
		Parameters: map[string]any{"window": "daily", "dataset_version": "v3", "seed": 7},
		Jobs: []api.PipelineJob{
			{Name: "extract", Kind: "container", Image: "busybox"},
			{Name: "train", Kind: "container", Image: "busybox", DependsOn: []string{"extract"}},
			{Name: "report", Kind: "container", Image: "busybox", DependsOn: []string{"train"}},
		},
	}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{store: data}, definition
}

var admin = auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}}

func failures(result PreflightResult) string {
	var out []string
	for _, check := range result.Checks {
		if check.Status == "fail" {
			out = append(out, check.Name+": "+check.Message)
		}
	}
	return strings.Join(out, " | ")
}

func TestPreflightAppliesOnlyOverridableParameters(t *testing.T) {
	s, definition := preflightFixture(t, &api.Overridable{Parameters: []string{"window", "dataset_version"}})
	result := s.preflight(admin, api.SubmitPipelineRequest{ProjectID: definition.ProjectID, DefinitionID: definition.ID,
		Overrides: &api.RunOverrides{Parameters: map[string]any{"window": "weekly"}}})
	if !result.Allowed || result.Parameters["window"] != "weekly" || result.Parameters["dataset_version"] != "v3" {
		t.Fatalf("allowed override: %v %v", result.Parameters, failures(result))
	}
	result = s.preflight(admin, api.SubmitPipelineRequest{ProjectID: definition.ProjectID, DefinitionID: definition.ID,
		Overrides: &api.RunOverrides{Parameters: map[string]any{"seed": 1}}})
	if result.Allowed || !strings.Contains(failures(result), `"seed" cannot be overridden`) {
		t.Fatalf("locked parameter accepted: %s", failures(result))
	}
	result = s.preflight(admin, api.SubmitPipelineRequest{ProjectID: definition.ProjectID, DefinitionID: definition.ID,
		Overrides: &api.RunOverrides{Parameters: map[string]any{"undeclared": 1}}})
	if result.Allowed {
		t.Fatal("undeclared parameter accepted")
	}
	for _, field := range result.Fields {
		if field.Field == "parameters.seed" && (!field.Locked || field.Reason == "") {
			t.Fatalf("seed must be shown as locked with a reason: %+v", field)
		}
	}
}

func TestPreflightImageRules(t *testing.T) {
	s, definition := preflightFixture(t, &api.Overridable{Image: true})
	submit := func(image string) PreflightResult {
		return s.preflight(admin, api.SubmitPipelineRequest{ProjectID: definition.ProjectID, DefinitionID: definition.ID,
			Overrides: &api.RunOverrides{Nodes: map[string]api.NodeOverride{"train": {Image: image}}}})
	}
	if result := submit("registry.example/team/train:latest"); result.Allowed || !strings.Contains(failures(result), "pinned by digest") {
		t.Fatalf("tag accepted: %s", failures(result))
	}
	if result := submit(goodDigest); !result.Allowed {
		t.Fatalf("digest rejected: %s", failures(result))
	}
	t.Setenv("KIONGA_IMAGE_REGISTRIES", "ghcr.io/approved")
	if result := submit(goodDigest); result.Allowed || !strings.Contains(failures(result), "not approved") {
		t.Fatalf("unapproved registry accepted: %s", failures(result))
	}
	s2, d2 := preflightFixture(t, nil)
	result := s2.preflight(admin, api.SubmitPipelineRequest{ProjectID: d2.ProjectID, DefinitionID: d2.ID,
		Overrides: &api.RunOverrides{Nodes: map[string]api.NodeOverride{"train": {Image: goodDigest, Resources: &api.JobResources{CPU: "2"}}}}})
	if result.Allowed || !strings.Contains(failures(result), "does not allow image") || !strings.Contains(failures(result), "does not allow resource") {
		t.Fatalf("locked node fields accepted: %s", failures(result))
	}
}

func TestPreflightCapacityAndUnsupportedPriority(t *testing.T) {
	s, definition := preflightFixture(t, &api.Overridable{Resources: true})
	if _, err := s.store.UpsertUserAccess("alice", api.UpsertUserAccessRequest{Role: "user", Services: []string{"pipelines"}, ProjectIDs: []string{definition.ProjectID},
		Compute: api.ComputeGrant{VCPUs: 2, MemoryGB: 4, MaxRuns: 2}}, "admin"); err != nil {
		t.Fatal(err)
	}
	alice := auth.Principal{Subject: "alice", Roles: []string{auth.RoleUser}, Services: []string{"pipelines"}, ProjectIDs: []string{definition.ProjectID}, Provisioned: true}
	result := s.preflight(alice, api.SubmitPipelineRequest{ProjectID: definition.ProjectID, DefinitionID: definition.ID,
		Overrides: &api.RunOverrides{Nodes: map[string]api.NodeOverride{"train": {Resources: &api.JobResources{CPU: "16", Memory: "64Gi"}}}}})
	if result.Allowed || !strings.Contains(failures(result), "capacity") {
		t.Fatalf("over-quota override accepted: %s", failures(result))
	}
	result = s.preflight(admin, api.SubmitPipelineRequest{ProjectID: definition.ProjectID, DefinitionID: definition.ID, Overrides: &api.RunOverrides{Priority: "high"}})
	if result.Allowed {
		t.Fatal("unsupported priority accepted")
	}
}

func TestRerunFromFailureReusesSucceededNodes(t *testing.T) {
	s, definition := preflightFixture(t, &api.Overridable{Nodes: true})
	previous, err := s.store.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: definition.ProjectID, DefinitionID: definition.ID}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.store.UpdateRunStep(previous.ID, api.UpdateRunStepRequest{Step: "extract", Status: "succeeded", Message: "loading\n{\"rows\": 12}"}, "engine")
	_, _ = s.store.UpdateRunStep(previous.ID, api.UpdateRunStepRequest{Step: "train", Status: "failed", Message: "oom"}, "engine")
	result := s.preflight(admin, api.SubmitPipelineRequest{ProjectID: definition.ProjectID, DefinitionID: definition.ID, Overrides: &api.RunOverrides{RerunFromRunID: previous.ID}})
	if !result.Allowed || len(result.ReusedNodes) != 1 || result.ReusedNodes[0] != "extract" {
		t.Fatalf("rerun: %v %s", result.ReusedNodes, failures(result))
	}
	run, _ := s.store.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: definition.ProjectID, DefinitionID: definition.ID, Overrides: &api.RunOverrides{RerunFromRunID: previous.ID}}, "admin")
	reuse := s.priorOutputs(run)
	if rows, _ := reuse["extract"].(map[string]any)["rows"].(float64); rows != 12 || len(reuse) != 1 {
		t.Fatalf("prior outputs: %#v", reuse)
	}
	// Selecting a node also runs everything it depends on.
	jobs := ApplyNodeOverrides(definition.Jobs, &api.RunOverrides{SelectedNodes: []string{"train"}})
	if len(jobs) != 2 || jobs[0].Name != "extract" || jobs[1].Name != "train" {
		t.Fatalf("selected closure: %+v", jobs)
	}
}

func TestSubmitEnforcesPreflightAndRecordsOverrides(t *testing.T) {
	var dispatched map[string]any
	prefect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/deployments/name/"):
			w.Write([]byte(`{"id":"dep-1"}`))
		case strings.HasSuffix(r.URL.Path, "/create_flow_run"):
			_ = json.NewDecoder(r.Body).Decode(&dispatched)
			w.Write([]byte(`{"id":"flow-run-1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer prefect.Close()
	t.Setenv("PREFECT_API_URL", prefect.URL)
	data := store.New()
	project, _ := data.CreateProject(api.CreateProjectRequest{Name: "Submit project"}, "admin")
	definition, _ := data.UpsertPipelineDefinition("", api.UpsertPipelineDefinitionRequest{ProjectID: project.ID, Name: "f", Version: "1",
		Overridable: &api.Overridable{Parameters: []string{"window"}}, Parameters: map[string]any{"window": "daily"},
		Jobs: []api.PipelineJob{{Name: "a", Kind: "container", Image: "busybox"}}}, "admin")
	handler := New(data, nil)
	body := `{"project_id":"` + project.ID + `","definition_id":"` + definition.ID + `","overrides":{"nodes":{"a":{"image":"evil:latest"}}}}`
	response := do(t, handler, "POST", "/api/v1/pipelines/submit", body)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "image overrides") {
		t.Fatalf("crafted override accepted: %d %s", response.Code, response.Body)
	}
	body = `{"project_id":"` + project.ID + `","definition_id":"` + definition.ID + `","trigger":"schedule","overrides":{"parameters":{"window":"weekly"}}}`
	response = do(t, handler, "POST", "/api/v1/pipelines/submit", body)
	var run api.PipelineRun
	_ = json.Unmarshal(response.Body.Bytes(), &run)
	if response.Code != http.StatusAccepted || run.EngineRunID != "flow-run-1" {
		t.Fatalf("submit: %d %s", response.Code, response.Body)
	}
	parameters, _ := dispatched["parameters"].(map[string]any)
	pinned, _ := parameters["definition"].(map[string]any)
	if pinned["revision"] != float64(1) || pinned["sha256"] != definition.SHA256 {
		t.Fatalf("engine did not receive the pinned revision: %#v", parameters)
	}
	if run.Trigger != "manual" || run.Parameters["window"] != "weekly" || run.Provenance == nil ||
		run.Provenance.Overrides.Parameters["window"] != "weekly" || run.Provenance.DefinitionRevision != 1 || run.Provenance.PolicyDecision == "" {
		t.Fatalf("run record: %d %+v %+v", response.Code, run, run.Provenance)
	}
}
