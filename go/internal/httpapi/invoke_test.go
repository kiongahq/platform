package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ml-ai-ops/platform/pkg/api"
)

// deployTestAgent creates a project and an agent through the API and returns
// the agent id.
func deployTestAgent(t *testing.T, server http.Handler) string {
	t.Helper()
	create := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"Support workspace","template":"rag-agent"}`))
	created := httptest.NewRecorder()
	server.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("project create failed: %d %s", created.Code, created.Body.String())
	}
	projectID := strings.Split(strings.Split(created.Body.String(), `"id":"`)[1], `"`)[0]
	deploy := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(`{"project_id":"`+projectID+`","name":"support","version":"1.0","image":"registry/agents/support:1.0","graph_module":"agents.customer_support.graph:build"}`))
	deployed := httptest.NewRecorder()
	server.ServeHTTP(deployed, deploy)
	if deployed.Code != http.StatusAccepted {
		t.Fatalf("agent deploy failed: %d %s", deployed.Code, deployed.Body.String())
	}
	return strings.Split(strings.Split(deployed.Body.String(), `"id":"`)[1], `"`)[0]
}

func TestInvokeAgentProxiesToRuntime(t *testing.T) {
	var received map[string]any
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != "/invoke" {
			t.Fatalf("unexpected runtime path %s", r.URL.Path)
		}
		if got := r.Header.Get("X-MLAIOps-Agent-Name"); got != "support" {
			t.Fatalf("expected agent name header, got %q", got)
		}
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"reply":"hello","session_id":"sess-1","input_tokens":3,"output_tokens":2,"cost_usd":0,"duration_ms":5}`))
	}))
	defer runtime.Close()
	t.Setenv("AGENT_RUNTIME_URL", runtime.URL)

	server := testServer()
	agentID := deployTestAgent(t, server)
	invoke := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agentID+"/invoke", strings.NewReader(`{"message":"hi","user_id":"u123"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, invoke)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"reply":"hello"`) {
		t.Fatalf("unexpected invoke response: %d %s", response.Code, response.Body.String())
	}
	if received["message"] != "hi" || received["user_id"] != "u123" {
		t.Fatalf("runtime did not receive the payload: %v", received)
	}
}

func TestAgentRuntimeEndpointUsesImmutableID(t *testing.T) {
	t.Setenv("AGENT_RUNTIME_URL", "")
	t.Setenv("MLAIOPS_AGENT_NAMESPACE", "tenant-workloads")
	identifier := "agt/project one"
	want := "http://" + api.AgentDNSName(identifier) + ".tenant-workloads.svc"
	if got := agentRuntimeEndpoint(identifier); got != want {
		t.Fatalf("runtime endpoint = %q, want %q", got, want)
	}
	t.Setenv("AGENT_RUNTIME_URL", "http://agent-runtime:9000/")
	if got := agentRuntimeEndpoint(identifier); got != "http://agent-runtime:9000" {
		t.Fatalf("Compose override was not preserved: %q", got)
	}
}

func TestAgentListReportsLiveComposeRuntime(t *testing.T) {
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" || r.Header.Get("X-MLAIOps-Agent-ID") == "" {
			t.Fatalf("unexpected readiness request: %s agent=%q", r.URL.Path, r.Header.Get("X-MLAIOps-Agent-ID"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer runtime.Close()
	t.Setenv("AGENT_RUNTIME_URL", runtime.URL)
	server := testServer()
	agentID := deployTestAgent(t, server)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("agent list failed: %d %s", response.Code, response.Body.String())
	}
	var page api.Page[api.Agent]
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != agentID || page.Items[0].Status != "ready" || page.Items[0].EndpointURL != runtime.URL {
		t.Fatalf("agent list did not reflect the live shared runtime: %#v", page.Items)
	}
}

func TestAgentReadinessProbesUseBoundedConcurrency(t *testing.T) {
	var active, maximum atomic.Int32
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for observed := maximum.Load(); current > observed && !maximum.CompareAndSwap(observed, current); observed = maximum.Load() {
		}
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer runtime.Close()
	t.Setenv("AGENT_RUNTIME_URL", runtime.URL)
	agents := make([]api.Agent, 24)
	for index := range agents {
		agents[index] = api.Agent{ID: "agt-" + strings.Repeat("x", index+1), Name: "agent"}
	}
	enrichAgentRuntimeReadiness(context.Background(), agents)
	if got := maximum.Load(); got > 8 || got < 2 {
		t.Fatalf("readiness concurrency = %d, want 2..8", got)
	}
	for _, agent := range agents {
		if agent.Status != "ready" || agent.EndpointURL != runtime.URL {
			t.Fatalf("runtime-backed agent was not ready: %#v", agent)
		}
	}
}

func TestInvokeAgentGatesUnavailableRuntime(t *testing.T) {
	invoked := false
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		invoked = true
	}))
	defer runtime.Close()
	t.Setenv("AGENT_RUNTIME_URL", runtime.URL)
	server := testServer()
	agentID := deployTestAgent(t, server)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agentID+"/invoke", strings.NewReader(`{"message":"hi"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "agent_not_ready") || invoked {
		t.Fatalf("unready runtime was not gated: %d %s invoked=%t", response.Code, response.Body.String(), invoked)
	}
}

func TestInvokeAgentUnknownAgent(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/agt-missing/invoke", strings.NewReader(`{"message":"hi"}`))
	response := httptest.NewRecorder()
	testServer().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d %s", response.Code, response.Body.String())
	}
}

func TestInvokeAgentRequiresMessage(t *testing.T) {
	server := testServer()
	agentID := deployTestAgent(t, server)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agentID+"/invoke", strings.NewReader(`{"message":"  "}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d %s", response.Code, response.Body.String())
	}
}

func TestInvokeAgentRuntimeDown(t *testing.T) {
	t.Setenv("AGENT_RUNTIME_URL", "http://127.0.0.1:1")
	server := testServer()
	agentID := deployTestAgent(t, server)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agentID+"/invoke", strings.NewReader(`{"message":"hi"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d %s", response.Code, response.Body.String())
	}
}
