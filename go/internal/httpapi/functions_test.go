package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeOpenFaaS(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "admin" || pass != "secret" {
			t.Errorf("basic auth not forwarded")
		}
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/system/functions":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["service"] != "sentiment-fn" || payload["image"] != "registry/fn:1" {
				t.Errorf("unexpected deploy payload: %v", payload)
			}
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && r.URL.Path == "/system/functions":
			_, _ = w.Write([]byte(`[{"name":"sentiment-fn","image":"registry/fn:1","replicas":0}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/function/sentiment-fn":
			_, _ = w.Write([]byte(`{"sentiment":"positive"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/async-function/sentiment-fn":
			w.Header().Set("X-Call-Id", "call-123")
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestFunctionsReportUnconfiguredHonestly(t *testing.T) {
	t.Setenv("OPENFAAS_URL", "")
	response := httptest.NewRecorder()
	testServer().ServeHTTP(response, newTestRequest(http.MethodGet, "/api/v1/functions", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"configured":false`) {
		t.Fatalf("unexpected: %d %s", response.Code, response.Body.String())
	}
}

func TestFunctionDeployListInvoke(t *testing.T) {
	upstream := fakeOpenFaaS(t)
	defer upstream.Close()
	t.Setenv("OPENFAAS_URL", upstream.URL)
	t.Setenv("OPENFAAS_USER", "admin")
	t.Setenv("OPENFAAS_PASSWORD", "secret")
	server := testServer()

	deploy := newTestRequest(http.MethodPost, "/api/v1/functions", strings.NewReader(`{"name":"sentiment-fn","image":"registry/fn:1"}`))
	deployed := httptest.NewRecorder()
	server.ServeHTTP(deployed, deploy)
	if deployed.Code != http.StatusAccepted {
		t.Fatalf("deploy failed: %d %s", deployed.Code, deployed.Body.String())
	}

	listed := httptest.NewRecorder()
	server.ServeHTTP(listed, newTestRequest(http.MethodGet, "/api/v1/functions", nil))
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"sentiment-fn"`) {
		t.Fatalf("list failed: %d %s", listed.Code, listed.Body.String())
	}

	invoked := httptest.NewRecorder()
	server.ServeHTTP(invoked, newTestRequest(http.MethodPost, "/api/v1/functions/sentiment-fn/invoke", strings.NewReader(`{"text":"great"}`)))
	if invoked.Code != http.StatusOK || !strings.Contains(invoked.Body.String(), "positive") {
		t.Fatalf("invoke failed: %d %s", invoked.Code, invoked.Body.String())
	}

	queued := httptest.NewRecorder()
	server.ServeHTTP(queued, newTestRequest(http.MethodPost, "/api/v1/functions/sentiment-fn/invoke-async", strings.NewReader(`{"text":"later"}`)))
	if queued.Code != http.StatusAccepted || !strings.Contains(queued.Body.String(), `"call_id":"call-123"`) {
		t.Fatalf("async invoke failed: %d %s", queued.Code, queued.Body.String())
	}
}

func TestFunctionDeployValidatesEventTrigger(t *testing.T) {
	upstream := fakeOpenFaaS(t)
	defer upstream.Close()
	t.Setenv("OPENFAAS_URL", upstream.URL)
	response := httptest.NewRecorder()
	testServer().ServeHTTP(response, newTestRequest(http.MethodPost, "/api/v1/functions", strings.NewReader(`{"name":"sentiment-fn","image":"registry/fn:1","annotations":{"topic":"cron-function","schedule":"not cron"}}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "five-field") {
		t.Fatalf("expected trigger validation failure, got %d %s", response.Code, response.Body.String())
	}
}

func TestFunctionDeployWithoutConfigIsConflict(t *testing.T) {
	t.Setenv("OPENFAAS_URL", "")
	response := httptest.NewRecorder()
	testServer().ServeHTTP(response, newTestRequest(http.MethodPost, "/api/v1/functions", strings.NewReader(`{"name":"x","image":"y"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", response.Code)
	}
}
