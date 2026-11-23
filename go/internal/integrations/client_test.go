package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
