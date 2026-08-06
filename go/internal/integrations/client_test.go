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
		if r.URL.Path != "/topics/mlaiops.audit.operations" {
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
	if err := NewKafkaREST(server.URL, "").Publish(context.Background(), "mlaiops.audit.operations", map[string]string{"action": "created"}); err != nil {
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
