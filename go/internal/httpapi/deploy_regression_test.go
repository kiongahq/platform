package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// Regression: on Compose without SERVING_MANAGER_URL, Deploy used to record
// the model as deploying with a placeholder endpoint and start nothing. It
// must fail closed with not_configured and leave the record untouched.
func TestDeployModelWithoutServingManagerFailsClosed(t *testing.T) {
	t.Setenv("SERVING_MANAGER_URL", "")
	harness := &kubeHarness{server: testServer()}
	modelID := registerDeployableModel(t, harness.server)
	deployed := harness.do(t, http.MethodPost, "/api/v1/models/"+modelID+"/deploy", `{"canary_weight":0}`)
	if deployed.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d %s", deployed.Code, deployed.Body.String())
	}
	body := jsonBody(t, deployed)
	if body["error"] != "not_configured" || len(body["steps"].([]any)) == 0 {
		t.Fatalf("missing not_configured steps: %v", body)
	}
	listed := harness.do(t, http.MethodGet, "/api/v1/models", "").Body.String()
	for _, forbidden := range []string{`"deployment_status":"deploying"`, `"deployment_status":"serving"`, `"deployment_status":"pending"`, ":predict"} {
		if strings.Contains(listed, forbidden) {
			t.Fatalf("a deploy that started nothing left %s in %s", forbidden, listed)
		}
	}
}

func TestKubernetesExecutorIsAutomaticInCluster(t *testing.T) {
	for _, tc := range []struct {
		runtime, host, executor string
		want                    bool
	}{
		{"", "10.0.0.1", "", true},
		{"", "", "", false},
		{"kubernetes", "", "", true},
		{"compose", "10.0.0.1", "", false},
		{"", "10.0.0.1", "prefect", false},
		{"compose", "", "kubernetes", true},
	} {
		t.Setenv("KIONGA_RUNTIME", tc.runtime)
		t.Setenv("KUBERNETES_SERVICE_HOST", tc.host)
		t.Setenv("KIONGA_EXECUTOR", tc.executor)
		if got := kubernetesExecutorEnabled(); got != tc.want {
			t.Errorf("runtime=%q host=%q executor=%q: got %v", tc.runtime, tc.host, tc.executor, got)
		}
	}
}

func TestAgentScalingIsValidatedAndRecorded(t *testing.T) {
	harness := &kubeHarness{server: testServer()}
	projectID := createTestProject(t, harness.server)
	base := `{"project_id":"` + projectID + `","name":"support","version":"1","image":"ghcr.io/acme/agent:1","graph_module":"agents.support:graph","tools":[]`
	if bad := harness.do(t, http.MethodPost, "/api/v1/agents", base+`,"scaling":{"metric":"memory","target":5}}`); bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown metric: %d %s", bad.Code, bad.Body.String())
	}
	defaulted := harness.do(t, http.MethodPost, "/api/v1/agents", base+`}`)
	if defaulted.Code != http.StatusAccepted || !strings.Contains(defaulted.Body.String(), `"scaling":{"metric":"requests","target":100}`) {
		t.Fatalf("default scaling: %d %s", defaulted.Code, defaulted.Body.String())
	}
	cpu := harness.do(t, http.MethodPost, "/api/v1/agents", base+`,"scaling":{"metric":"cpu","target":65}}`)
	if cpu.Code != http.StatusAccepted || !strings.Contains(cpu.Body.String(), `"scaling":{"metric":"cpu","target":65}`) {
		t.Fatalf("cpu scaling: %d %s", cpu.Code, cpu.Body.String())
	}
}
