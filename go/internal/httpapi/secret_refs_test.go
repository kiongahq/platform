package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

func TestSecretScopePermits(t *testing.T) {
	t.Setenv("KIONGA_KB_SECRET_REFS", "TEAM_QDRANT_KEY, DATABASE_URL")
	cases := []struct {
		scope secretScope
		ref   string
		want  bool
	}{
		{knowledgeSecrets, "env:KIONGA_KB_SECRET_DOCS", true},
		{knowledgeSecrets, "env:KIONGA_QDRANT_KEY", true},
		{knowledgeSecrets, "env:KIONGA_KNOWLEDGE_DSN", true},
		{knowledgeSecrets, "env:TEAM_QDRANT_KEY", true},
		{connectionSecrets, "env:KIONGA_CONNECTION_SECRET_CRM", true},
		{connectionSecrets, "env:KIONGA_AGENT_API_TOKEN", true},
		// The gateway's own secrets, even when an operator allowlists one.
		{knowledgeSecrets, "env:DATABASE_URL", false},
		{knowledgeSecrets, "env:MLAIOPS_INTERNAL_TOKEN", false},
		{knowledgeSecrets, "env:KIONGA_CREDENTIAL_KEY", false},
		{connectionSecrets, "env:DATABASE_URL", false},
		{connectionSecrets, "env:KIONGA_SANDBOX_TOKEN", false},
		// Scopes do not read each other's variables.
		{connectionSecrets, "env:KIONGA_KB_SECRET_DOCS", false},
		{knowledgeSecrets, "env:KIONGA_CONNECTION_SECRET_CRM", false},
		{connectionSecrets, "env:TEAM_QDRANT_KEY", false},
		// The bare prefix, an unlisted name and malformed references.
		{knowledgeSecrets, "env:KIONGA_KB_SECRET_", false},
		{knowledgeSecrets, "env:HOME", false},
		{knowledgeSecrets, "env:", false},
		{knowledgeSecrets, "KIONGA_KB_SECRET_DOCS", false},
		{knowledgeSecrets, "", false},
	}
	for _, tc := range cases {
		if got := tc.scope.permits(tc.ref); got != tc.want {
			t.Errorf("%s permits(%q) = %v, want %v", tc.scope.prefix, tc.ref, got, tc.want)
		}
	}
}

func TestEmptyAllowlistEntryDoesNotPermitAnything(t *testing.T) {
	t.Setenv("KIONGA_KB_SECRET_REFS", " , ,")
	if knowledgeSecrets.permits("env:OTHER_VARIABLE") {
		t.Fatal("an empty allowlist entry permitted a variable")
	}
}

// secretStub counts requests and records the credentials it was sent.
func secretStub(t *testing.T) (*httptest.Server, *atomic.Int64, *atomic.Value) {
	t.Helper()
	var hits atomic.Int64
	var seen atomic.Value
	seen.Store("")
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		seen.Store(r.Header.Get("api-key") + r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"result":true}`))
	}))
	t.Cleanup(stub.Close)
	return stub, &hits, &seen
}

func secretTestServer(t *testing.T) (http.Handler, store.Repository, string) {
	t.Helper()
	repository := store.New()
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Secret refs"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return domainServer(repository, auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}}), repository, project.ID
}

func TestKnowledgeBaseRejectsGatewaySecretBeforeAnyNetworkCall(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway-private")
	t.Setenv("MLAIOPS_INTERNAL_TOKEN", "gateway-private")
	stub, hits, _ := secretStub(t)
	server, _, projectID := secretTestServer(t)
	for _, ref := range []string{"env:DATABASE_URL", "env:MLAIOPS_INTERNAL_TOKEN", "env:HOME"} {
		body := `{"project_id":"` + projectID + `","name":"stolen","backend":"qdrant","dimensions":3,"endpoint":"` + stub.URL + `","secret_ref":"` + ref + `"}`
		created := call(t, server, http.MethodPost, "/api/v1/admin/knowledge-bases", body)
		if created.Code != 422 {
			t.Fatalf("%s: got %d %s", ref, created.Code, created.Body.String())
		}
		if strings.Contains(created.Body.String(), "gateway-private") {
			t.Fatalf("%s: response carried the secret: %s", ref, created.Body.String())
		}
	}
	pg := `{"project_id":"` + projectID + `","name":"stolen-pg","backend":"pgvector","dimensions":3,"dsn_ref":"env:DATABASE_URL"}`
	if created := call(t, server, http.MethodPost, "/api/v1/admin/knowledge-bases", pg); created.Code != 422 {
		t.Fatalf("pgvector DSN: got %d %s", created.Code, created.Body.String())
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("stub received %d requests", got)
	}
}

func TestKnowledgeBasePermittedReferenceReachesBackend(t *testing.T) {
	secret := "permitted-" + "value"
	t.Setenv("KIONGA_KB_SECRET_DOCS", secret)
	stub, hits, seen := secretStub(t)
	server, _, projectID := secretTestServer(t)
	body := `{"project_id":"` + projectID + `","name":"permitted","backend":"qdrant","dimensions":3,"endpoint":"` + stub.URL + `","secret_ref":"env:KIONGA_KB_SECRET_DOCS"}`
	created := call(t, server, http.MethodPost, "/api/v1/admin/knowledge-bases", body)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	if hits.Load() != 1 || seen.Load() != secret {
		t.Fatalf("hits=%d credential=%q", hits.Load(), seen.Load())
	}
}

// A base saved before the restriction keeps its reference, so the check also
// has to hold when the reference is resolved.
func TestStoredKnowledgeBaseWithGatewaySecretStopsResolving(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway-private")
	stub, hits, seen := secretStub(t)
	server, repository, projectID := secretTestServer(t)
	legacy := knowledgeBase{ID: "kb-legacy", ProjectID: projectID, Name: "legacy", Backend: "qdrant", Endpoint: stub.URL, SecretRef: "env:DATABASE_URL", Dimensions: 3}
	if _, err := store.UpdateDoc(repository, knowledgeKind, legacy.ID, func(_ knowledgeBase, _ bool) (knowledgeBase, error) { return legacy, nil }, "knowledge_base.created", "admin"); err != nil {
		t.Fatal(err)
	}
	added := call(t, server, http.MethodPost, "/api/v1/knowledge-bases/kb-legacy/chunks", `{"text":"x","vector":[1,0,0]}`)
	if added.Code != 503 {
		t.Fatalf("add: %d %s", added.Code, added.Body.String())
	}
	if hits.Load() != 0 || seen.Load() != "" {
		t.Fatalf("stub hits=%d credential=%q", hits.Load(), seen.Load())
	}
}

func TestAgentConnectionRejectsGatewaySecretAndKnowledgeSecret(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway-private")
	stub, hits, _ := secretStub(t)
	server, _, projectID := secretTestServer(t)
	for _, ref := range []string{"env:DATABASE_URL", "env:MLAIOPS_INTERNAL_TOKEN", "env:KIONGA_KB_SECRET_DOCS"} {
		body := `{"project_id":"` + projectID + `","name":"stolen","endpoint":"` + stub.URL + `","secret_ref":"` + ref + `"}`
		if created := call(t, server, http.MethodPost, "/api/v1/admin/agent-api-connections", body); created.Code != 422 {
			t.Fatalf("%s: got %d %s", ref, created.Code, created.Body.String())
		}
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("stub received %d requests", got)
	}
}

func TestStoredAgentConnectionWithGatewaySecretStopsResolving(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway-private")
	stub, hits, seen := secretStub(t)
	server, repository, projectID := secretTestServer(t)
	legacy := agentAPIConnection{ID: "api-legacy", ProjectID: projectID, Name: "legacy", Endpoint: stub.URL, SecretRef: "env:DATABASE_URL"}
	if _, err := store.UpdateDoc(repository, agentConnectionKind, legacy.ID, func(_ agentAPIConnection, _ bool) (agentAPIConnection, error) { return legacy, nil }, "agent_api_connection.created", "admin"); err != nil {
		t.Fatal(err)
	}
	response := call(t, server, http.MethodPost, "/api/v1/agent-api-connections/api-legacy/invoke", `{"a":1}`)
	if response.Code != 503 {
		var parsed map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &parsed)
		t.Fatalf("invoke: %d %v", response.Code, parsed)
	}
	if hits.Load() != 0 || seen.Load() != "" {
		t.Fatalf("stub hits=%d credential=%q", hits.Load(), seen.Load())
	}
}
