package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMLflowTransitionUsesStandardAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/2.0/mlflow/model-versions/transition-stage" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stage"] != "Production" || body["archive_existing_versions"] != true {
			t.Errorf("unexpected body: %#v", body)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	if err := NewMLflow(server.URL, "").TransitionStage(context.Background(), "churn", "3", "Production"); err != nil {
		t.Fatal(err)
	}
}

func TestKafkaPublishesConfluentRESTEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/topics/kionga.audit.operations" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/vnd.kafka.json.v2+json" {
			t.Errorf("content type = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.kafka.v2+json" {
			t.Errorf("accept = %q", got)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	if err := NewKafkaREST(server.URL, "").Publish(context.Background(), "kionga.audit.operations", map[string]string{"action": "created"}); err != nil {
		t.Fatal(err)
	}
}

func TestClientSurfacesIntegrationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	err := NewLangfuse(server.URL, "").Ingest(context.Background(), []map[string]any{{"id": "1"}})
	if err == nil {
		t.Fatal("expected integration error")
	}
}

// The operator resolves a registered version's files through these three
// MLflow REST calls; each must hit the documented path with its query.
func TestMLflowArtifactLocationCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/2.0/mlflow/model-versions/get-download-uri":
			if r.Method != http.MethodGet || r.URL.Query().Get("name") != "churn model" || r.URL.Query().Get("version") != "3" {
				t.Errorf("download uri request: %s %s", r.Method, r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"artifact_uri":"s3://kionga-models/1/abc/artifacts/model"}`))
		case "/api/2.0/mlflow/runs/get":
			if r.URL.Query().Get("run_id") != "abc" {
				t.Errorf("run request: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"run":{"info":{"artifact_uri":"s3://kionga-models/1/abc/artifacts"}}}`))
		case "/api/2.0/mlflow/logged-models/m-123":
			_, _ = w.Write([]byte(`{"model":{"info":{"artifact_uri":"s3://kionga-models/1/models/m-123/artifacts"}}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	mlflow := NewMLflow(server.URL, "")
	ctx := context.Background()
	if uri, err := mlflow.ModelVersionDownloadURI(ctx, "churn model", "3"); err != nil || uri != "s3://kionga-models/1/abc/artifacts/model" {
		t.Fatalf("download uri: %q %v", uri, err)
	}
	if uri, err := mlflow.RunArtifactURI(ctx, "abc"); err != nil || uri != "s3://kionga-models/1/abc/artifacts" {
		t.Fatalf("run artifact uri: %q %v", uri, err)
	}
	if uri, err := mlflow.LoggedModelArtifactURI(ctx, "m-123"); err != nil || uri != "s3://kionga-models/1/models/m-123/artifacts" {
		t.Fatalf("logged model uri: %q %v", uri, err)
	}
}

// The gateway reads a model version's files through the MLflow server's own
// artifact route, bounded, and never copies a response body into an error.
func TestMLflowModelVersionArtifact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/model-versions/get-artifact" {
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-1" {
			t.Errorf("authorization = %q", got)
		}
		query := r.URL.Query()
		if query.Get("name") != "churn model" || query.Get("version") != "3" {
			t.Errorf("unexpected query %s", r.URL.RawQuery)
		}
		switch query.Get("path") {
		case "MLmodel":
			_, _ = w.Write([]byte("artifact_path: model\n"))
		case "big.json":
			_, _ = w.Write([]byte(strings.Repeat("x", 65)))
		case "big-chunked.json":
			w.(http.Flusher).Flush()
			_, _ = w.Write([]byte(strings.Repeat("x", 65)))
		case "missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error_code":"RESOURCE_DOES_NOT_EXIST","message":"artifact body text"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error_code":"not an identifier: artifact body text"}`))
		}
	}))
	defer server.Close()
	mlflow := NewMLflow(server.URL+"/", "token-1")
	ctx := context.Background()
	if raw, err := mlflow.ModelVersionArtifact(ctx, "churn model", "3", "MLmodel", 64); err != nil || string(raw) != "artifact_path: model\n" {
		t.Fatalf("MLmodel = %q, %v", raw, err)
	}
	for _, path := range []string{"big.json", "big-chunked.json"} {
		if _, err := mlflow.ModelVersionArtifact(ctx, "churn model", "3", path, 64); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("%s: err = %v, want ErrTooLarge", path, err)
		}
	}
	var status *StatusError
	if _, err := mlflow.ModelVersionArtifact(ctx, "churn model", "3", "missing", 64); !errors.As(err, &status) || status.Status != http.StatusNotFound || status.Code != "RESOURCE_DOES_NOT_EXIST" || strings.Contains(err.Error(), "artifact body text") {
		t.Fatalf("missing: err = %v", err)
	}
	if _, err := mlflow.ModelVersionArtifact(ctx, "churn model", "3", "broken", 64); !errors.As(err, &status) || status.Status != http.StatusInternalServerError || status.Code != "" || strings.Contains(err.Error(), "artifact body text") {
		t.Fatalf("broken: err = %v", err)
	}
	if !mlflow.Configured() || NewMLflow("", "token").Configured() {
		t.Fatal("Configured must follow the base URL")
	}
	if _, err := NewMLflow("", "").ModelVersionArtifact(ctx, "a", "1", "MLmodel", 64); err == nil {
		t.Fatal("an unconfigured client must fail")
	}
}

// A redirect from MLflow is never followed: the gateway would otherwise fetch
// whatever address the answer names, with its token on a same-host redirect.
func TestMLflowModelVersionArtifactDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed.Add(1)
		_, _ = w.Write([]byte("internal content"))
	}))
	defer target.Close()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			followed.Add(1)
			return
		}
		destination := target.URL + "/metadata"
		if r.URL.Query().Get("path") == "same-host" {
			destination = "/elsewhere"
		}
		http.Redirect(w, r, destination, http.StatusFound)
	}))
	defer server.Close()
	for _, path := range []string{"other-host", "same-host"} {
		calls.Store(0)
		raw, err := NewMLflow(server.URL, "token-1").ModelVersionArtifact(context.Background(), "churn", "3", path, 64)
		var status *StatusError
		if !errors.As(err, &status) || status.Status != http.StatusFound || raw != nil {
			t.Fatalf("%s: got %q, %v; want a 302 StatusError", path, raw, err)
		}
	}
	if followed.Load() != 0 {
		t.Fatal("the client followed a redirect")
	}
}
