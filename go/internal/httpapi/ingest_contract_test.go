package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// Regression for contracts#10: a newer producer's extra fields must not make
// the gateway reject a step report or a trace, because the runner only prints
// the resulting 400 and the state is lost.
func TestOpenIngestContractsAcceptExtraFields(t *testing.T) {
	const token = "ingest-contract-test-token"
	t.Setenv("KIONGA_INTERNAL_TOKEN", token)
	repository := store.New()
	run, err := repository.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: "prj-demo", Name: "training-pipeline"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Steps) == 0 {
		t.Fatal("the run has no steps to report on")
	}
	stepsPath := "/api/v1/pipelines/runs/" + run.ID + "/steps"
	endpoints := map[string]string{"step-report": stepsPath, "log-batch": "/api/v1/pipelines/runs/" + run.ID + "/logs", "trace": "/api/v1/traces"}
	fixtures, err := filepath.Glob("../../../contracts/ingest/examples/*/extra-field.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != len(endpoints) {
		t.Fatalf("found %d extra-field fixtures, want %d: %v (run git submodule update --init)", len(fixtures), len(endpoints), fixtures)
	}
	server := New(repository, nil)
	post := func(path string, body []byte) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	for _, fixture := range fixtures {
		contract := filepath.Base(filepath.Dir(fixture))
		t.Run(contract, func(t *testing.T) {
			path, known := endpoints[contract]
			if !known {
				t.Fatalf("no endpoint mapped for contract %s", contract)
			}
			raw, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatal(err)
			}
			body := raw
			if contract == "step-report" {
				// The fixture names its own step; report on one this run has.
				var payload map[string]any
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Fatal(err)
				}
				payload["step"] = run.Steps[0].Name
				if body, err = json.Marshal(payload); err != nil {
					t.Fatal(err)
				}
			}
			response := post(path, body)
			if response.Code < 200 || response.Code >= 300 {
				t.Fatalf("POST %s = %d: %s", path, response.Code, response.Body.String())
			}
		})
	}
	current, err := repository.Run(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Steps[0].Status != "succeeded" {
		t.Fatalf("step status after the report = %q, want succeeded", current.Steps[0].Status)
	}
	// Open means unknown fields only: known fields stay typed.
	if response := post(stepsPath, []byte(`{"step":"x","status":"running","attempt":"one"}`)); response.Code != http.StatusBadRequest {
		t.Fatalf("mistyped known field = %d, want 400", response.Code)
	}
	if response := post("/api/v1/traces", []byte(`{"agent_id":"a","session_id":"s","duration_ms":"long"}`)); response.Code != http.StatusBadRequest {
		t.Fatalf("mistyped known trace field = %d, want 400", response.Code)
	}
}

// User-authored requests stay closed: a typo must not be silently dropped.
func TestUserAuthoredRequestsStillRejectUnknownFields(t *testing.T) {
	server := testServer()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"Typo project","templat":"blank"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "unknown field") {
		t.Fatalf("unknown field on a user request = %d: %s", response.Code, response.Body.String())
	}
}
