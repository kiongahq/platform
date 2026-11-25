package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// connectionServer serves the connection routes as one principal, through
// the same authorization the gateway applies.
func connectionServer(repository store.Repository, as auth.Principal) http.Handler {
	s := &Server{store: repository}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/connections", s.createConnection)
	mux.HandleFunc("PUT /api/v1/connections/{id}", s.updateConnection)
	mux.HandleFunc("POST /api/v1/connections/{id}/test", s.testConnection)
	mux.HandleFunc("POST /api/v1/connections/{id}/activate", s.activateConnection)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), as)))
	})
}

func connectionFixture(t *testing.T, repository store.Repository, name, kind, endpoint string) api.Connection {
	t.Helper()
	item, err := repository.CreateConnection(api.CreateConnectionRequest{Name: name, Type: kind, Endpoint: endpoint, SecretRef: "none"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func storedConnection(t *testing.T, repository store.Repository, id string) api.Connection {
	t.Helper()
	for _, item := range repository.Connections() {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("connection %s not found", id)
	return api.Connection{}
}

func connectionBody(name, kind, endpoint, secretRef string) string {
	return `{"name":"` + name + `","type":"` + kind + `","endpoint":"` + endpoint + `","secret_ref":"` + secretRef + `"}`
}

func TestUpdateConnectionAuthorization(t *testing.T) {
	repository := store.New()
	connection := connectionFixture(t, repository, "tracking", "mlflow", "http://mlflow:5000")
	path := "/api/v1/connections/" + connection.ID
	body := connectionBody("tracking-2", "mlflow", "http://mlflow:5000", "none")
	denied := map[string]auth.Principal{
		"engineer": {Subject: "eng", Roles: []string{auth.RoleEngineer}},
		"viewer":   {Subject: "view", Roles: []string{auth.RoleViewer}},
		"service":  {Subject: "svc", Roles: []string{auth.RoleService}},
		"user":     {Subject: "user", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"platform"}},
		"disabled": {Subject: "gone", Roles: []string{auth.RoleAdmin}, Disabled: true},
		"nobody":   {},
	}
	for name, who := range denied {
		// The same people cannot create a connection either.
		if created := call(t, connectionServer(repository, who), http.MethodPost, "/api/v1/connections", connectionBody("made-by-"+name, "mlflow", "http://x", "none")); created.Code != http.StatusForbidden {
			t.Fatalf("%s create: %d %s", name, created.Code, created.Body.String())
		}
		if response := call(t, connectionServer(repository, who), http.MethodPut, path, body); response.Code != http.StatusForbidden {
			t.Fatalf("%s update: %d %s", name, response.Code, response.Body.String())
		}
	}
	if got := storedConnection(t, repository, connection.ID); got.Name != "tracking" {
		t.Fatalf("a refused update changed the connection: %+v", got)
	}
	for index, who := range []auth.Principal{{Subject: "root", Roles: []string{auth.RoleAdmin}}, {Subject: "ops", Roles: []string{auth.RoleOperator}}} {
		name := []string{"tracking-admin", "tracking-operator"}[index]
		response := call(t, connectionServer(repository, who), http.MethodPut, path, connectionBody(name, "mlflow", "http://mlflow:5000", "none"))
		if response.Code != http.StatusOK || decodeInto[api.Connection](t, response).Name != name {
			t.Fatalf("%s update: %d %s", who.Subject, response.Code, response.Body.String())
		}
	}
	events := repository.Audit()
	if len(events) == 0 || events[0].Action != "connection.updated" || events[0].ResourceID != connection.ID || events[0].Actor != "ops" {
		t.Fatalf("update was not audited: %+v", events)
	}
}

func TestUpdateConnectionValidationAndUniqueness(t *testing.T) {
	repository := store.New()
	admin := connectionServer(repository, localAdmin)
	connection := connectionFixture(t, repository, "tracking", "mlflow", "http://mlflow:5000")
	connectionFixture(t, repository, "Events", "kafka", "http://kafka-rest:8082")
	path := "/api/v1/connections/" + connection.ID
	cases := []struct {
		name, body string
		status     int
	}{
		{"missing name", connectionBody("", "mlflow", "http://mlflow:5000", "none"), 422},
		{"missing type", connectionBody("tracking", "", "http://mlflow:5000", "none"), 422},
		{"missing secret reference", connectionBody("tracking", "mlflow", "http://mlflow:5000", ""), 422},
		{"unknown field", `{"name":"tracking","type":"mlflow","endpoint":"http://x","secret_ref":"none","status":"healthy"}`, 400},
		{"activation cannot be written", `{"name":"tracking","type":"mlflow","endpoint":"http://x","secret_ref":"none","activated_at":"2026-01-01T00:00:00Z"}`, 400},
		{"name of another connection", connectionBody("Events", "mlflow", "http://mlflow:5000", "none"), 409},
		{"name of another connection in another case", connectionBody("EVENTS", "mlflow", "http://mlflow:5000", "none"), 409},
	}
	for _, tc := range cases {
		if response := call(t, admin, http.MethodPut, path, tc.body); response.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.name, response.Code, response.Body.String())
		}
	}
	if response := call(t, admin, http.MethodPut, "/api/v1/connections/conn-missing", connectionBody("other", "mlflow", "http://x", "none")); response.Code != 404 {
		t.Fatalf("missing connection: %d %s", response.Code, response.Body.String())
	}
	if got := storedConnection(t, repository, connection.ID); got != connection {
		t.Fatalf("refused updates changed the connection: %+v", got)
	}

	// Saving under its own name is not a conflict; a rename keeps the last check.
	if _, err := repository.UpdateConnectionStatus(connection.ID, "healthy", "Connection succeeded", "admin"); err != nil {
		t.Fatal(err)
	}
	renamed := call(t, admin, http.MethodPut, path, connectionBody("Tracking", "mlflow", "http://mlflow:5000", "none"))
	if got := decodeInto[api.Connection](t, renamed); renamed.Code != 200 || got.Name != "Tracking" || got.Status != "healthy" || got.ID != connection.ID {
		t.Fatalf("rename: %d %s", renamed.Code, renamed.Body.String())
	}
	// A new address has not been checked: pending until Test runs.
	moved := call(t, admin, http.MethodPut, path, connectionBody("Tracking", "mlflow", "http://mlflow-2:5000", "none"))
	if got := decodeInto[api.Connection](t, moved); moved.Code != 200 || got.Endpoint != "http://mlflow-2:5000" || got.Status != "pending" || got.CheckedAt != nil {
		t.Fatalf("address change: %d %s", moved.Code, moved.Body.String())
	}
}

func TestUpdateConnectionRefusesGatewaySecret(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway-private")
	t.Setenv("KIONGA_INTERNAL_TOKEN", "gateway-private")
	stub, hits, seen := secretStub(t)
	repository := store.New()
	admin := connectionServer(repository, localAdmin)
	connection := connectionFixture(t, repository, "pipelines", "prefect", stub.URL)
	path := "/api/v1/connections/" + connection.ID
	for _, ref := range []string{"env:DATABASE_URL", "env:KIONGA_INTERNAL_TOKEN", "env:HOME"} {
		response := call(t, admin, http.MethodPut, path, connectionBody("pipelines", "prefect", stub.URL, ref))
		if response.Code != 422 || strings.Contains(response.Body.String(), "gateway-private") {
			t.Fatalf("%s: %d %s", ref, response.Code, response.Body.String())
		}
		// Exactly what create answers for the same reference.
		created := call(t, admin, http.MethodPost, "/api/v1/connections", connectionBody("stolen", "prefect", stub.URL, ref))
		if created.Code != response.Code || created.Body.String() != response.Body.String() {
			t.Fatalf("%s: create answered %d %s, update %d %s", ref, created.Code, created.Body.String(), response.Code, response.Body.String())
		}
	}
	if got := storedConnection(t, repository, connection.ID); got.SecretRef != "none" {
		t.Fatalf("a refused reference was saved: %+v", got)
	}
	if hits.Load() != 0 || seen.Load() != "" {
		t.Fatalf("hits=%d credential=%q", hits.Load(), seen.Load())
	}
	permitted := call(t, admin, http.MethodPut, path, connectionBody("pipelines", "prefect", stub.URL, "env:KIONGA_PLATFORM_SECRET_PREFECT"))
	if permitted.Code != 200 || decodeInto[api.Connection](t, permitted).SecretRef != "env:KIONGA_PLATFORM_SECRET_PREFECT" {
		t.Fatalf("permitted reference: %d %s", permitted.Code, permitted.Body.String())
	}
}

func TestUpdateActiveRuntimeIsVerifiedBeforeItChanges(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	var checks atomic.Int64
	upstream := func(token string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			checks.Add(1)
			if r.URL.Path != "/api/health" || r.Header.Get("Authorization") != token || !healthy.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte("true"))
		}))
		t.Cleanup(server.Close)
		return server
	}
	first, second := upstream(""), upstream("Bearer private-test-token")
	t.Setenv("KIONGA_PLATFORM_SECRET_PREFECT", "private-test-token")
	repository := store.New()
	admin := connectionServer(repository, localAdmin)
	connection := connectionFixture(t, repository, "pipelines", "prefect", first.URL+"/api")
	path := "/api/v1/connections/" + connection.ID
	if activated := call(t, admin, http.MethodPost, path+"/activate", `{}`); activated.Code != 200 {
		t.Fatalf("activate: %d %s", activated.Code, activated.Body.String())
	}
	active := storedConnection(t, repository, connection.ID)
	unchanged := func(reason string) {
		t.Helper()
		got := storedConnection(t, repository, connection.ID)
		if got.Endpoint != active.Endpoint || got.SecretRef != active.SecretRef || got.Type != "prefect" || got.ActivatedAt == nil || got.Status != "healthy" {
			t.Fatalf("%s changed the active runtime: %+v", reason, got)
		}
	}

	// A rename is not a runtime change: no check, activation kept.
	before := checks.Load()
	renamed := call(t, admin, http.MethodPut, path, connectionBody("pipelines-main", "prefect", first.URL+"/api", "none"))
	if got := decodeInto[api.Connection](t, renamed); renamed.Code != 200 || got.Name != "pipelines-main" || got.ActivatedAt == nil || got.Status != "healthy" || checks.Load() != before {
		t.Fatalf("rename: %d %s checks=%d", renamed.Code, renamed.Body.String(), checks.Load()-before)
	}

	// The service of an activated connection is fixed.
	retyped := call(t, admin, http.MethodPut, path, connectionBody("pipelines-main", "openfaas", first.URL+"/api", "none"))
	if retyped.Code != 409 || !strings.Contains(retyped.Body.String(), "active_runtime") {
		t.Fatalf("service change: %d %s", retyped.Code, retyped.Body.String())
	}
	unchanged("a service change")

	// An address that fails its check is refused and nothing is saved.
	unreachable := call(t, admin, http.MethodPut, path, connectionBody("pipelines-main", "prefect", second.URL+"/api", "none"))
	if unreachable.Code != 422 || !strings.Contains(unreachable.Body.String(), "connection_failed") {
		t.Fatalf("failing address: %d %s", unreachable.Code, unreachable.Body.String())
	}
	unchanged("a failing address")

	// A credential the gateway cannot resolve is refused before any request.
	before = checks.Load()
	missing := call(t, admin, http.MethodPut, path, connectionBody("pipelines-main", "prefect", second.URL+"/api", "env:KIONGA_PLATFORM_SECRET_ABSENT"))
	if missing.Code != 422 || !strings.Contains(missing.Body.String(), "credential_unavailable") || checks.Load() != before {
		t.Fatalf("missing credential: %d %s", missing.Code, missing.Body.String())
	}
	unchanged("a missing credential")

	// Runtimes do not change under a pipeline in flight.
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Runtime project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	run, err := repository.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: project.ID, Name: "train"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	good := connectionBody("pipelines-main", "prefect", second.URL+"/api", "env:KIONGA_PLATFORM_SECRET_PREFECT")
	if busy := call(t, admin, http.MethodPut, path, good); busy.Code != 409 || !strings.Contains(busy.Body.String(), "runs_active") {
		t.Fatalf("change under a queued run: %d %s", busy.Code, busy.Body.String())
	}
	unchanged("a change under a queued run")
	if _, err := repository.CancelRun(run.ID, "admin"); err != nil {
		t.Fatal(err)
	}

	// A verified address and credential are saved and the runtime stays active.
	before = checks.Load()
	moved := call(t, admin, http.MethodPut, path, good)
	got := decodeInto[api.Connection](t, moved)
	if moved.Code != 200 || got.Endpoint != second.URL+"/api" || got.SecretRef != "env:KIONGA_PLATFORM_SECRET_PREFECT" || got.ActivatedAt == nil || got.Status != "healthy" || got.CheckedAt == nil || checks.Load() != before+1 {
		t.Fatalf("verified change: %d %s", moved.Code, moved.Body.String())
	}
	if selected := (&Server{store: repository}).activeConnection("prefect"); selected == nil || selected.Endpoint != second.URL+"/api" {
		t.Fatalf("active runtime: %+v", selected)
	}
	if strings.Contains(moved.Body.String(), "private-test-token") {
		t.Fatalf("response carried the credential: %s", moved.Body.String())
	}
}

func TestConnectionUpdateIsDocumentedAndRouted(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&Server{}).openapi(recorder, httptest.NewRequest("GET", "/api/openapi.json", nil))
	if !strings.Contains(recorder.Body.String(), `"/api/v1/connections/{id}":{"put"`) {
		t.Fatal("openapi lacks PUT /api/v1/connections/{id}")
	}
	// The gateway's own mux serves the route.
	t.Setenv("KIONGA_LOCAL_ROLE", "admin")
	server := testServer()
	created := call(t, server, http.MethodPost, "/api/v1/connections", connectionBody("routed", "mlflow", "http://mlflow:5000", "none"))
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	id := decodeInto[api.Connection](t, created).ID
	if updated := call(t, server, http.MethodPut, "/api/v1/connections/"+id, connectionBody("routed-2", "mlflow", "http://mlflow:5000", "none")); updated.Code != http.StatusOK {
		t.Fatalf("update: %d %s", updated.Code, updated.Body.String())
	}
	t.Setenv("KIONGA_LOCAL_ROLE", "engineer")
	if denied := call(t, testServer(), http.MethodPut, "/api/v1/connections/"+id, connectionBody("routed-3", "mlflow", "http://mlflow:5000", "none")); denied.Code != http.StatusForbidden {
		t.Fatalf("engineer update: %d %s", denied.Code, denied.Body.String())
	}
}

// An edit that lands while a check is in flight must not be labelled or
// activated by that check: the check saw the previous target.
func TestCheckDoesNotVouchForAConcurrentEdit(t *testing.T) {
	repository := store.New()
	admin := connectionServer(repository, localAdmin)
	var connection api.Connection
	var edit atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if edit.Load() {
			moved := api.CreateConnectionRequest{Name: connection.Name, Type: "prefect", Endpoint: "http://unchecked.example.test/api", SecretRef: "none"}
			if _, err := repository.UpdateConnection(connection.ID, moved, false, "someone-else"); err != nil {
				t.Errorf("concurrent edit: %v", err)
			}
		}
		_, _ = w.Write([]byte("true"))
	}))
	defer upstream.Close()
	for _, action := range []string{"activate", "test"} {
		edit.Store(false)
		connection = connectionFixture(t, repository, "pipelines-"+action, "prefect", upstream.URL+"/api")
		edit.Store(true)
		response := call(t, admin, http.MethodPost, "/api/v1/connections/"+connection.ID+"/"+action, `{}`)
		if response.Code != 409 || !strings.Contains(response.Body.String(), "connection_changed") {
			t.Fatalf("%s: %d %s", action, response.Code, response.Body.String())
		}
		got := storedConnection(t, repository, connection.ID)
		if got.Endpoint != "http://unchecked.example.test/api" || got.ActivatedAt != nil || got.Status != "pending" {
			t.Fatalf("%s vouched for an unchecked target: %+v", action, got)
		}
	}
}

// A pipeline submitted while the service is being checked keeps its runtime.
func TestRuntimeDoesNotChangeUnderARunSubmittedDuringTheCheck(t *testing.T) {
	repository := store.New()
	admin := connectionServer(repository, localAdmin)
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Runtime project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	var submit atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if submit.Load() {
			if _, err := repository.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: project.ID, Name: "train"}, "someone-else"); err != nil {
				t.Errorf("submit: %v", err)
			}
		}
		_, _ = w.Write([]byte("true"))
	}))
	defer upstream.Close()
	connection := connectionFixture(t, repository, "pipelines", "prefect", upstream.URL+"/api")
	path := "/api/v1/connections/" + connection.ID
	if activated := call(t, admin, http.MethodPost, path+"/activate", `{}`); activated.Code != 200 {
		t.Fatalf("activate: %d %s", activated.Code, activated.Body.String())
	}
	submit.Store(true)
	moved := call(t, admin, http.MethodPut, path, connectionBody("pipelines", "prefect", upstream.URL+"/api/", "none"))
	if moved.Code != 409 || !strings.Contains(moved.Body.String(), "runs_active") {
		t.Fatalf("change under a run submitted during the check: %d %s", moved.Code, moved.Body.String())
	}
	if got := storedConnection(t, repository, connection.ID); got.Endpoint != upstream.URL+"/api" {
		t.Fatalf("runtime changed under a run: %+v", got)
	}
}
