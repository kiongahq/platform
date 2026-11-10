package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakePrefect(t *testing.T, deploymentFound bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/deployments/name/training-pipeline/kionga":
			if !deploymentFound {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"id":"dep-1"}`))
		case "/api/deployments/dep-1/create_flow_run":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["name"] != "run-42" {
				t.Errorf("run name not forwarded: %v", payload)
			}
			parameters := payload["parameters"].(map[string]any)
			if parameters["run_id"] != "run-42" {
				t.Errorf("run_id parameter missing: %v", payload)
			}
			_, _ = w.Write([]byte(`{"id":"fr-9"}`))
		case "/api/flow_runs/fr-9/set_state":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			state := payload["state"].(map[string]any)
			if state["type"] != "CANCELLING" {
				t.Errorf("unexpected cancel state: %v", payload)
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected prefect path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestPrefectCreateFlowRun(t *testing.T) {
	server := fakePrefect(t, true)
	defer server.Close()
	prefect := NewPrefect(server.URL, "")
	id, err := prefect.CreateFlowRun(context.Background(), "training-pipeline", "kionga", "run-42", map[string]any{"run_id": "run-42"})
	if err != nil || id != "fr-9" {
		t.Fatalf("unexpected result: %q %v", id, err)
	}
	if err := prefect.CancelFlowRun(context.Background(), "fr-9"); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
}

func TestPrefectAcceptsAPIConventionURL(t *testing.T) {
	server := fakePrefect(t, true)
	defer server.Close()
	// PREFECT_API_URL conventionally ends in /api; the client must not
	// produce /api/api/... paths.
	prefect := NewPrefect(server.URL+"/api", "")
	id, err := prefect.CreateFlowRun(context.Background(), "training-pipeline", "kionga", "run-42", map[string]any{"run_id": "run-42"})
	if err != nil || id != "fr-9" {
		t.Fatalf("unexpected result: %q %v", id, err)
	}
}

func TestPrefectMissingDeploymentFailsClosed(t *testing.T) {
	server := fakePrefect(t, false)
	defer server.Close()
	prefect := NewPrefect(server.URL, "")
	if _, err := prefect.CreateFlowRun(context.Background(), "training-pipeline", "kionga", "run-42", nil); err == nil {
		t.Fatal("expected deployment resolution error")
	}
}
