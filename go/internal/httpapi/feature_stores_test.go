package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/storage"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

const feastSecret = "feast-bearer-0f9a8b7c"

func fakeFeastServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+feastSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestFeatureStoreConnectionLifecycleNeverEchoesSecrets(t *testing.T) {
	feast := fakeFeastServer(t)
	defer feast.Close()
	t.Setenv("FEAST_TOKEN", feastSecret)
	repository := store.New()
	project := scaffoldProjectFixture(t, repository)
	admin := domainServer(repository, localAdmin)
	responses := []*httptest.ResponseRecorder{}
	record := func(response *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		responses = append(responses, response)
		return response
	}

	body := `{"provider":"feast","name":"team-feast","config":{"url":"` + feast.URL + `","feature_services":"customer_profile"},"secret_ref":"env:FEAST_TOKEN","allowed_projects":["` + project.ID + `"]}`
	created := record(call(t, admin, http.MethodPost, "/api/v1/admin/feature-stores", body))
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	connection := decodeInto[api.FeatureStoreConnection](t, created)
	if connection.Kind != "external" || !connection.SecretPresent || connection.Health.State != "configured" || !connection.Capabilities.Online || connection.Capabilities.Offline {
		t.Fatalf("created connection: %+v", connection)
	}
	tested := record(call(t, admin, http.MethodPost, "/api/v1/admin/feature-stores/"+connection.ID+"/test", `{}`))
	if health := decodeInto[api.FeatureStoreConnection](t, tested).Health; health.State != "healthy" || health.CheckedAt == nil {
		t.Fatalf("test: %s", tested.Body.String())
	}
	record(call(t, admin, http.MethodGet, "/api/v1/admin/feature-stores", ""))
	record(call(t, admin, http.MethodGet, "/api/v1/admin/feature-stores/"+connection.ID, ""))
	record(call(t, admin, http.MethodGet, "/api/v1/features/stores", ""))

	t.Setenv("FEAST_TOKEN", "wrong-"+feastSecret)
	rejected := record(call(t, admin, http.MethodPost, "/api/v1/admin/feature-stores/"+connection.ID+"/test", `{}`))
	if health := decodeInto[api.FeatureStoreConnection](t, rejected).Health; health.State != "unavailable" {
		t.Fatalf("wrong credential: %+v", health)
	}
	t.Setenv("FEAST_TOKEN", "")
	unset := record(call(t, admin, http.MethodPost, "/api/v1/admin/feature-stores/"+connection.ID+"/test", `{}`))
	if health := decodeInto[api.FeatureStoreConnection](t, unset).Health; health.State != "configured" || !strings.Contains(health.Detail, "env:FEAST_TOKEN") {
		t.Fatalf("missing secret must stay configured with a reason: %+v", health)
	}
	for _, response := range responses {
		if strings.Contains(response.Body.String(), feastSecret) {
			t.Fatalf("secret leaked: %s", response.Body.String())
		}
	}
	raw, _ := repository.GetDocument(store.FeatureStoreConnectionKind, connection.ID)
	if strings.Contains(string(raw), feastSecret) {
		t.Fatal("secret persisted")
	}

	updated := call(t, admin, http.MethodPut, "/api/v1/admin/feature-stores/"+connection.ID, strings.Replace(body, "team-feast", "team-feast-2", 1))
	if updated.Code != http.StatusOK || decodeInto[api.FeatureStoreConnection](t, updated).Health.State != "configured" {
		t.Fatalf("update resets health to configured: %d %s", updated.Code, updated.Body.String())
	}
	if response := call(t, admin, http.MethodDelete, "/api/v1/admin/feature-stores/"+connection.ID, ""); response.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", response.Code)
	}
	if response := call(t, admin, http.MethodDelete, "/api/v1/admin/feature-stores/internal", ""); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("internal store cannot be deleted: %d", response.Code)
	}
}

func TestFeatureStoreValidation(t *testing.T) {
	repository := store.New()
	admin := domainServer(repository, localAdmin)
	cases := map[string]string{
		"secret in config":   `{"provider":"feast","name":"a1","config":{"url":"http://feast:6566","token":"abc"}}`,
		"credentials in url": `{"provider":"feast","name":"a1","config":{"url":"http://user:pw@feast:6566"}}`,
		"contract only":      `{"provider":"tecton","name":"a1","config":{}}`,
		"unknown provider":   `{"provider":"nope","name":"a1","config":{}}`,
		"internal duplicate": `{"provider":"internal","name":"a1","config":{"online_url":"redis://redis:6379"}}`,
		"bad secret ref":     `{"provider":"feast","name":"a1","config":{"url":"http://feast:6566"},"secret_ref":"FEAST_TOKEN"}`,
		"unknown project":    `{"provider":"feast","name":"a1","config":{"url":"http://feast:6566"},"allowed_projects":["prj-missing"]}`,
		"bad name":           `{"provider":"feast","name":"Bad Name","config":{"url":"http://feast:6566"}}`,
		"missing feast url":  `{"provider":"feast","name":"a1","config":{}}`,
		"unknown config key": `{"provider":"feast","name":"a1","config":{"url":"http://feast:6566","region":"x"}}`,
	}
	for name, body := range cases {
		if response := call(t, admin, http.MethodPost, "/api/v1/admin/feature-stores", body); response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: expected 422, got %d %s", name, response.Code, response.Body.String())
		} else if strings.Contains(response.Body.String(), "pw@") || strings.Contains(response.Body.String(), `"abc"`) {
			t.Fatalf("%s: validation echoed a secret: %s", name, response.Body.String())
		}
	}
	providers := call(t, admin, http.MethodGet, "/api/v1/admin/feature-store-providers", "")
	if !strings.Contains(providers.Body.String(), "contract available — no adapter") || !strings.Contains(providers.Body.String(), `"name":"feast"`) {
		t.Fatalf("providers: %s", providers.Body.String())
	}
	if auth.Allowed(auth.Principal{Roles: []string{auth.RoleEngineer}}, http.MethodGet, "/api/v1/admin/feature-stores") ||
		auth.Allowed(auth.Principal{Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"features"}}, http.MethodPost, "/api/v1/admin/storage-connections") {
		t.Fatal("connection administration must be admin/operator only")
	}
}

func TestBuiltinInternalStoreHealth(t *testing.T) {
	healthy := true
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable","mode":"redis"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","mode":"redis"}`))
	}))
	defer gateway.Close()
	repository := store.New()
	admin := domainServer(repository, localAdmin)
	t.Setenv("MLAIOPS_FEATURE_GATEWAY_URL", "")
	unconfigured := decodeInto[api.FeatureStoreConnection](t, call(t, admin, http.MethodPost, "/api/v1/admin/feature-stores/internal/test", `{}`))
	if unconfigured.Health.State != "configured" {
		t.Fatalf("unconfigured internal store: %+v", unconfigured.Health)
	}
	t.Setenv("MLAIOPS_FEATURE_GATEWAY_URL", gateway.URL)
	ok := decodeInto[api.FeatureStoreConnection](t, call(t, admin, http.MethodPost, "/api/v1/admin/feature-stores/internal/test", `{}`))
	if ok.Health.State != "healthy" || !strings.Contains(ok.Health.Detail, "s3://mlaiops-features") {
		t.Fatalf("internal healthy: %+v", ok.Health)
	}
	healthy = false
	down := decodeInto[api.FeatureStoreConnection](t, call(t, admin, http.MethodPost, "/api/v1/admin/feature-stores/internal/test", `{}`))
	if down.Health.State != "unavailable" {
		t.Fatalf("internal down: %+v", down.Health)
	}
	stores := decodeInto[api.Page[api.FeatureStoreSummary]](t, call(t, admin, http.MethodGet, "/api/v1/features/stores", ""))
	if stores.Items[0].ID != "internal" || stores.Items[0].Health.State != "unavailable" || !stores.Items[0].AllProjects {
		t.Fatalf("persisted internal health must be shown: %+v", stores.Items[0])
	}
}

func TestFeatureStoresAreScopedToCallerProjects(t *testing.T) {
	repository := store.New()
	mine := scaffoldProjectFixture(t, repository)
	other, _ := repository.CreateProject(api.CreateProjectRequest{Name: "Other team", Template: "blank-python"}, "admin")
	for i, projects := range [][]string{{other.ID}, {mine.ID, other.ID}} {
		connection := api.FeatureStoreConnection{ID: "fs-" + string(rune('a'+i)), Provider: "feast", Name: "store-" + string(rune('a'+i)), Kind: "external", Config: map[string]string{"url": "http://feast:6566"}, AllowedProjects: projects}
		if _, err := store.SaveFeatureStoreConnection(repository, connection, true, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	user := domainServer(repository, auth.Principal{Subject: "alice", Roles: []string{auth.RoleUser}, Services: []string{"features"}, ProjectIDs: []string{mine.ID}, Provisioned: true})
	stores := decodeInto[api.Page[api.FeatureStoreSummary]](t, call(t, user, http.MethodGet, "/api/v1/features/stores", ""))
	if stores.Total != 2 || stores.Items[0].ID != "internal" || stores.Items[1].ID != "fs-b" {
		t.Fatalf("user sees the internal store and only stores shared with their projects: %+v", stores.Items)
	}
	if strings.Join(stores.Items[1].AllowedProjects, ",") != mine.ID {
		t.Fatalf("other teams' project ids must not be disclosed: %+v", stores.Items[1].AllowedProjects)
	}
	if response := call(t, user, http.MethodPost, "/api/v1/features/customer_profile/materializations", `{}`); response.Code != http.StatusForbidden {
		t.Fatalf("users cannot report materializations: %d", response.Code)
	}
}

func TestFeatureVersionsLineageAndFreshness(t *testing.T) {
	repository := store.New()
	server := New(repository, nil)
	admin := domainServer(repository, localAdmin)
	apply := func(body string) {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/features", strings.NewReader(body)))
		if response.Code != http.StatusCreated {
			t.Fatalf("apply: %d %s", response.Code, response.Body.String())
		}
	}
	v1 := `{"name":"customer_profile","entity":"user_id","fields":[{"name":"plan","type":"string"}],"source":"s3://raw/customers","ttl_seconds":3600}`
	apply(v1)
	apply(v1)
	apply(`{"name":"customer_profile","entity":"user_id","fields":[{"name":"plan","type":"string"},{"name":"csat","type":"float64"}],"source":"s3://raw/customers","ttl_seconds":3600}`)
	versions := decodeInto[api.Page[api.FeatureDefinitionVersion]](t, call(t, admin, http.MethodGet, "/api/v1/features/customer_profile/versions", ""))
	if versions.Total != 2 || versions.Items[0].Version != 2 || len(versions.Items[1].Fields) != 1 {
		t.Fatalf("versions: %+v", versions.Items)
	}
	views := decodeInto[api.Page[api.FeatureViewDetail]](t, call(t, admin, http.MethodGet, "/api/v1/features/views", ""))
	if views.Items[0].Freshness.State != "never" || views.Items[0].Version != 2 || views.Items[0].Store.Kind != "internal" {
		t.Fatalf("before materialization: %+v", views.Items[0])
	}
	report := call(t, admin, http.MethodPost, "/api/v1/features/customer_profile/materializations", `{"run_id":"mat-1","source_dataset":"s3://raw/customers","offline_uri":"s3://mlaiops-features/customer_profile/v2/snapshot.parquet","entity_count":3}`)
	if report.Code != http.StatusCreated {
		t.Fatalf("report: %d %s", report.Code, report.Body.String())
	}
	if record := decodeInto[api.FeatureMaterialization](t, report); record.ViewVersion != 2 || record.Status != "succeeded" || record.ReportedBy != "admin" {
		t.Fatalf("lineage links the current version: %+v", record)
	}
	failed := call(t, admin, http.MethodPost, "/api/v1/features/customer_profile/materializations", `{"run_id":"mat-2","source_dataset":"s3://raw/customers","status":"failed","error":"source unreachable"}`)
	if failed.Code != http.StatusCreated {
		t.Fatalf("failed report: %d %s", failed.Code, failed.Body.String())
	}
	views = decodeInto[api.Page[api.FeatureViewDetail]](t, call(t, admin, http.MethodGet, "/api/v1/features/views", ""))
	view := views.Items[0]
	if view.Freshness.State != "fresh" || view.OnlineEntityCount != 3 || view.FailureCount != 1 || view.LatestFailure != "source unreachable" || view.LastRun.RunID != "mat-2" {
		t.Fatalf("after runs: %+v", view)
	}
	lineage := decodeInto[api.Page[api.FeatureMaterialization]](t, call(t, admin, http.MethodGet, "/api/v1/features/customer_profile/lineage", ""))
	if lineage.Total != 2 {
		t.Fatalf("lineage: %+v", lineage)
	}
	for body, field := range map[string]string{
		`{"run_id":"x; rm","source_dataset":"s3://raw"}`:               "run_id",
		`{"run_id":"r1","source_dataset":""}`:                          "source_dataset",
		`{"run_id":"r1","source_dataset":"s3://raw","status":"maybe"}`: "status",
	} {
		if response := call(t, admin, http.MethodPost, "/api/v1/features/customer_profile/materializations", body); response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), field) {
			t.Fatalf("%s: %d %s", body, response.Code, response.Body.String())
		}
	}
	if response := call(t, admin, http.MethodGet, "/api/v1/features/missing/versions", ""); response.Code != http.StatusNotFound {
		t.Fatalf("missing view: %d", response.Code)
	}
}

func TestStorageConnectionsValidateTestAndHideCredentials(t *testing.T) {
	const credential = "AKIAEXAMPLE:very-secret-storage-key"
	t.Setenv("LAKE_CREDENTIALS", credential)
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && r.URL.Path == "/lake" && strings.Contains(r.URL.RawQuery, "AKIAEXAMPLE") {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer s3.Close()
	storageCheckClient = func(storage.BucketTarget) *http.Client { return s3.Client() }
	defer func() { storageCheckClient = nil }()
	repository := store.New()
	admin := domainServer(repository, localAdmin)
	created := call(t, admin, http.MethodPost, "/api/v1/admin/storage-connections", `{"name":"team-lake","endpoint":"`+s3.URL+`/","region":"us-east-1","bucket":"lake","path_style":true,"secret_ref":"env:LAKE_CREDENTIALS"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	connection := decodeInto[api.StorageConnection](t, created)
	if !connection.SecretPresent || connection.Endpoint != s3.URL {
		t.Fatalf("connection: %+v", connection)
	}
	tested := call(t, admin, http.MethodPost, "/api/v1/admin/storage-connections/"+connection.ID+"/test", `{}`)
	if health := decodeInto[api.StorageConnection](t, tested).Health; health.State != "healthy" {
		t.Fatalf("test: %s", tested.Body.String())
	}
	t.Setenv("LAKE_CREDENTIALS", "OTHER:key")
	denied := call(t, admin, http.MethodPost, "/api/v1/admin/storage-connections/"+connection.ID+"/test", `{}`)
	if health := decodeInto[api.StorageConnection](t, denied).Health; health.State != "unavailable" || !strings.Contains(health.Detail, "Access denied") {
		t.Fatalf("denied: %+v", health)
	}
	list := call(t, admin, http.MethodGet, "/api/v1/admin/storage-connections", "")
	for _, response := range []*httptest.ResponseRecorder{created, tested, denied, list} {
		if strings.Contains(response.Body.String(), "very-secret-storage-key") || strings.Contains(response.Body.String(), "AKIAEXAMPLE") {
			t.Fatalf("credential leaked: %s", response.Body.String())
		}
	}
	for name, body := range map[string]string{
		"no secret":     `{"name":"lake-2","endpoint":"https://s3.example.com","bucket":"lake","path_style":true}`,
		"inline creds":  `{"name":"lake-2","endpoint":"https://AKIA:secret@s3.example.com","bucket":"lake","secret_ref":"env:X"}`,
		"bad bucket":    `{"name":"lake-2","endpoint":"https://s3.example.com","bucket":"../x","secret_ref":"env:X"}`,
		"private key":   `{"name":"lake-2","endpoint":"https://s3.example.com","bucket":"lake","secret_ref":"env:X","ca_bundle":"-----BEGIN PRIVATE KEY-----"}`,
		"duplicate":     `{"name":"team-lake","endpoint":"https://s3.example.com","bucket":"lake","secret_ref":"env:X"}`,
		"unknown field": `{"name":"lake-2","endpoint":"https://s3.example.com","bucket":"lake","secret_ref":"env:X","secret_access_key":"x"}`,
	} {
		response := call(t, admin, http.MethodPost, "/api/v1/admin/storage-connections", body)
		if response.Code != http.StatusUnprocessableEntity && response.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, response.Code, response.Body.String())
		}
	}
}
