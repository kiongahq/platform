package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/runtimeconfig"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

func TestSecretScopePermits(t *testing.T) {
	t.Setenv("KIONGA_KB_SECRET_REFS", "TEAM_QDRANT_KEY, DATABASE_URL")
	cases := []struct {
		scope runtimeconfig.SecretScope
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
		{knowledgeSecrets, "env:KIONGA_INTERNAL_TOKEN", false},
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
		if got := tc.scope.Permits(tc.ref); got != tc.want {
			t.Errorf("%s permits(%q) = %v, want %v", tc.scope.Prefix, tc.ref, got, tc.want)
		}
	}
}

func TestEmptyAllowlistEntryDoesNotPermitAnything(t *testing.T) {
	t.Setenv("KIONGA_KB_SECRET_REFS", " , ,")
	if knowledgeSecrets.Permits("env:OTHER_VARIABLE") {
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
	t.Setenv("KIONGA_INTERNAL_TOKEN", "gateway-private")
	stub, hits, _ := secretStub(t)
	server, _, projectID := secretTestServer(t)
	for _, ref := range []string{"env:DATABASE_URL", "env:KIONGA_INTERNAL_TOKEN", "env:HOME"} {
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
	for _, ref := range []string{"env:DATABASE_URL", "env:KIONGA_INTERNAL_TOKEN", "env:KIONGA_KB_SECRET_DOCS"} {
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

func TestPlatformSecretsAreScopedByType(t *testing.T) {
	cases := []struct {
		connectionType, ref string
		want                bool
	}{
		{"prefect", "env:KIONGA_PLATFORM_SECRET_PREFECT", true},
		{"openfaas", "env:OPENFAAS_PASSWORD", true},
		{"mlflow", "env:MLFLOW_TOKEN", true},
		{"prefect", "env:OPENFAAS_PASSWORD", false},
		{"mlflow", "env:OPENFAAS_PASSWORD", false},
		{"openfaas", "env:DATABASE_URL", false},
		{"prefect", "env:DATABASE_URL", false},
		{"kubernetes", "env:KIONGA_INTERNAL_TOKEN", false},
		{"prefect", "env:HOME", false},
		{"prefect", "env:KIONGA_KB_SECRET_DOCS", false},
	}
	for _, tc := range cases {
		if got := runtimeconfig.PlatformSecrets(tc.connectionType).Permits(tc.ref); got != tc.want {
			t.Errorf("%s %s: %v", tc.connectionType, tc.ref, got)
		}
	}
	for ref, want := range map[string]bool{"env:FEAST_TOKEN": true, "env:KIONGA_FEATURE_SECRET_X": true, "env:DATABASE_URL": false, "env:LAKE_CREDENTIALS": false} {
		if got := featureSecrets.Permits(ref); got != want {
			t.Errorf("feature %s: %v", ref, got)
		}
	}
	for ref, want := range map[string]bool{"env:LAKE_CREDENTIALS": true, "env:KIONGA_STORAGE_SECRET_X": true, "env:DATABASE_URL": false, "env:FEAST_TOKEN": false} {
		if got := storageSecrets.Permits(ref); got != want {
			t.Errorf("storage %s: %v", ref, got)
		}
	}
}

func TestPlatformConnectionRefusesGatewaySecret(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway-private")
	stub, hits, seen := secretStub(t)
	repository := store.New()
	s := &Server{store: repository}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/connections", s.createConnection)
	asAdmin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}})))
	})
	created := call(t, asAdmin, http.MethodPost, "/api/v1/connections", `{"name":"stolen","type":"prefect","endpoint":"`+stub.URL+`","secret_ref":"env:DATABASE_URL"}`)
	if created.Code != 422 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	if ok := call(t, asAdmin, http.MethodPost, "/api/v1/connections", `{"name":"fine","type":"prefect","endpoint":"`+stub.URL+`","secret_ref":"env:KIONGA_PLATFORM_SECRET_PREFECT"}`); ok.Code != 201 {
		t.Fatalf("permitted: %d %s", ok.Code, ok.Body.String())
	}
	// A record saved before the restriction stops resolving, and its check sends nothing.
	legacy := api.Connection{ID: "conn-legacy", Type: "prefect", Endpoint: stub.URL, SecretRef: "env:DATABASE_URL"}
	if _, err := connectionToken(legacy); err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("legacy: %v", err)
	}
	if err := checkConnection(newTestRequest("GET", "/", nil), legacy); err == nil {
		t.Fatal("legacy check passed")
	}
	if hits.Load() != 0 || seen.Load() != "" {
		t.Fatalf("hits=%d credential=%q", hits.Load(), seen.Load())
	}
	// The documented OpenFaaS form keeps working.
	t.Setenv("OPENFAAS_PASSWORD", "faas-"+"password")
	if token, err := connectionToken(api.Connection{Type: "openfaas", SecretRef: "env:OPENFAAS_PASSWORD"}); err != nil || token == "" {
		t.Fatalf("openfaas: %q %v", token, err)
	}
}

func TestFeatureStoreRefusesGatewaySecret(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway-private")
	stub, hits, seen := secretStub(t)
	repository := store.New()
	admin := domainServer(repository, auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}})
	body := `{"provider":"feast","name":"stolen","config":{"url":"` + stub.URL + `","feature_services":"x"},"secret_ref":"env:DATABASE_URL"}`
	created := call(t, admin, http.MethodPost, "/api/v1/admin/feature-stores", body)
	if created.Code != 422 || !strings.Contains(created.Body.String(), "KIONGA_FEATURE_SECRET_") {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	legacy := api.FeatureStoreConnection{ID: "fs-legacy", Name: "legacy", Provider: "feast", Config: map[string]string{"url": stub.URL, "feature_services": "x"}, SecretRef: "env:DATABASE_URL"}
	health := checkFeatureStore(context.Background(), legacy)
	if health.State != "configured" || !strings.Contains(health.Detail, "not permitted") {
		t.Fatalf("health: %+v", health)
	}
	if hits.Load() != 0 || seen.Load() != "" {
		t.Fatalf("hits=%d credential=%q", hits.Load(), seen.Load())
	}
}

func TestStorageConnectionRefusesGatewaySecret(t *testing.T) {
	t.Setenv("DATABASE_URL", "AKIAFAKE:gateway-private")
	repository := store.New()
	admin := domainServer(repository, auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}})
	body := `{"name":"stolen","endpoint":"https://s3.example.com","region":"us-east-1","bucket":"lake","secret_ref":"env:DATABASE_URL"}`
	created := call(t, admin, http.MethodPost, "/api/v1/admin/storage-connections", body)
	if created.Code != 422 || !strings.Contains(created.Body.String(), "KIONGA_STORAGE_SECRET_") {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	stub, hits, seen := secretStub(t)
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Storage refs"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	legacy := api.StorageConnection{ID: "st-legacy", Name: "legacy", Endpoint: stub.URL, Region: "us-east-1", Bucket: "lake", SecretRef: "env:DATABASE_URL", AllowedProjects: []string{project.ID}}
	if _, err := store.UpdateDoc(repository, store.StorageConnectionKind, legacy.ID, func(api.StorageConnection, bool) (api.StorageConnection, error) { return legacy, nil }, "storage_connection.created", "admin"); err != nil {
		t.Fatal(err)
	}
	if publicStorage(legacy).SecretPresent {
		t.Fatal("a refused reference reads as present")
	}
	objects := call(t, admin, http.MethodGet, "/api/v1/registered-storage/st-legacy/objects", "")
	if objects.Code != 503 {
		t.Fatalf("objects: %d %s", objects.Code, objects.Body.String())
	}
	if seen.Load() != "" || hits.Load() != 0 {
		t.Fatalf("hits=%d credential=%q", hits.Load(), seen.Load())
	}
}

func TestRefusedSecretRefsListsRecordsWithoutValues(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway-private")
	t.Setenv("KIONGA_STORAGE_SECRET_REFS", "ALLOWED_LAKE")
	repository := store.New()
	save := func(kind, id string, value any) {
		t.Helper()
		if _, err := repository.UpdateDocument(kind, id, func(json.RawMessage) (any, error) { return value, nil }, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repository.CreateConnection(api.CreateConnectionRequest{Name: "a", Type: "prefect", Endpoint: "http://x", SecretRef: "env:DATABASE_URL"}, "admin"); err != nil {
		t.Fatal(err)
	}
	save(store.FeatureStoreConnectionKind, "fs-1", api.FeatureStoreConnection{ID: "fs-1", SecretRef: "env:HOME"})
	save(store.FeatureStoreConnectionKind, "fs-2", api.FeatureStoreConnection{ID: "fs-2", SecretRef: "env:FEAST_TOKEN"})
	save(store.StorageConnectionKind, "st-1", api.StorageConnection{ID: "st-1", SecretRef: "env:OLD_LAKE"})
	save(store.StorageConnectionKind, "st-2", api.StorageConnection{ID: "st-2", SecretRef: "env:ALLOWED_LAKE"})
	save(knowledgeKind, "kb-1", knowledgeBase{ID: "kb-1", SecretRef: "none", DSNRef: "env:DATABASE_URL"})
	save(agentConnectionKind, "api-1", agentAPIConnection{ID: "api-1", SecretRef: "env:OLD_TOKEN"})
	joined := strings.Join(refusedSecretRefs(repository), "\n")
	for _, want := range []string{"env:DATABASE_URL", "feature_store fs-1 env:HOME", "storage_connection st-1 env:OLD_LAKE", "knowledge_base kb-1 env:DATABASE_URL", "agent_api_connection api-1 env:OLD_TOKEN"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "fs-2") || strings.Contains(joined, "st-2") || strings.Contains(joined, "gateway-private") {
		t.Errorf("wrong entries:\n%s", joined)
	}
}
