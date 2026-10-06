package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ml-ai-ops/platform/internal/auth"
	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

type hubTransport func(*http.Request) (*http.Response, error)

func (f hubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func mockHub(t *testing.T, f func(*http.Request) (int, string)) {
	t.Helper()
	previous := hubHTTP
	hubHTTP = &http.Client{Transport: hubTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "huggingface.co" || r.URL.Scheme != "https" {
			t.Errorf("unexpected Hub host %s", r.URL)
		}
		code, body := f(r)
		return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}), CheckRedirect: previous.CheckRedirect}
	t.Cleanup(func() { hubHTTP = previous })
	t.Setenv("KIONGA_CREDENTIAL_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
}
func hubCall(handler http.Handler, subject, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Subject: subject, Roles: []string{"admin"}}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestHubAccountEncryptedPersistentAndIsolated(t *testing.T) {
	mockHub(t, func(r *http.Request) (int, string) {
		if r.Header.Get("Authorization") != "Bearer hf_private" {
			t.Error("missing personal token")
		}
		return 200, `{"name":"alice-hf"}`
	})
	path := t.TempDir() + "/state.json"
	repo := store.New(path)
	handler := New(repo, fstest.MapFS{})
	response := hubCall(handler, "alice", "PUT", "/api/v1/settings/huggingface", `{"token":"hf_private"}`)
	if response.Code != 200 || strings.Contains(response.Body.String(), "hf_private") || !strings.Contains(response.Body.String(), "alice-hf") {
		t.Fatalf("connect: %d %s", response.Code, response.Body)
	}
	disk, _ := os.ReadFile(path)
	if strings.Contains(string(disk), "hf_private") {
		t.Fatal("plaintext credential on disk")
	}
	server := &Server{store: store.New(path)}
	if token, err := server.hubToken("alice"); err != nil || token != "hf_private" {
		t.Fatal("encrypted account not restored")
	}
	if token, err := server.hubToken("bob"); err != nil || token != "" {
		t.Fatal("credential leaked across subjects")
	}
	stolen, _ := repo.HubAccount("alice")
	_ = repo.SaveHubAccount("bob", stolen)
	if _, err := (&Server{store: repo}).hubToken("bob"); err == nil {
		t.Fatal("ciphertext not bound to subject")
	}
	response = hubCall(handler, "alice", "DELETE", "/api/v1/settings/huggingface", "")
	if response.Code != 200 {
		t.Fatal(response.Body)
	}
	if token, err := (&Server{store: repo}).hubToken("alice"); err != nil || token != "" {
		t.Fatal("disconnect left usable token")
	}
}

func TestHubImportPinsCommitAndRequiresEvaluation(t *testing.T) {
	commit := strings.Repeat("a", 40)
	mockHub(t, func(r *http.Request) (int, string) {
		if r.URL.Path != "/api/models/org/model/revision/main" {
			t.Errorf("unexpected path %s", r.URL)
		}
		return 200, `{"id":"org/model","sha":"` + commit + `"}`
	})
	repo := store.New()
	handler := New(repo, fstest.MapFS{})
	result := hubCall(handler, "alice", "POST", "/api/v1/models/huggingface/import", `{"project_id":"prj-demo","repo_id":"org/model"}`)
	if result.Code != 201 {
		t.Fatalf("import: %d %s", result.Code, result.Body)
	}
	var model api.Model
	_ = json.Unmarshal(result.Body.Bytes(), &model)
	if model.ArtifactURI != "hf://org/model@"+commit || model.GateStatus != "needs_evaluation" {
		t.Fatalf("wrong provenance/gate: %+v", model)
	}
	if _, err := repo.PromoteModel(model.ID, "production", "alice"); err == nil {
		t.Fatal("unevaluated model promoted")
	}
	if _, err := repo.DeployModel(model.ID, 0, "alice"); err == nil {
		t.Fatal("unevaluated model deployed")
	}
}

func TestHubErrorsAndValidation(t *testing.T) {
	mockHub(t, func(r *http.Request) (int, string) { return 403, `{"error":"secret upstream detail"}` })
	handler := New(store.New(), fstest.MapFS{})
	for _, repo := range []string{"https://evil.test/model", "../model", "org/../../x", "org/model?token=x"} {
		body, _ := json.Marshal(map[string]string{"repo_id": repo, "project_id": "prj-demo"})
		if r := hubCall(handler, "alice", "POST", "/api/v1/models/huggingface/import", string(body)); r.Code != 400 {
			t.Fatalf("unsafe repo accepted %s", repo)
		}
	}
	r := hubCall(handler, "alice", "GET", "/api/v1/models/huggingface?search=model", "")
	if r.Code != 502 || !strings.Contains(r.Body.String(), "gated") || strings.Contains(r.Body.String(), "secret upstream detail") {
		t.Fatalf("bad error %s", r.Body)
	}
	t.Setenv("KIONGA_CREDENTIAL_KEY", "")
	if r = hubCall(handler, "alice", "PUT", "/api/v1/settings/huggingface", `{"token":"hf_private"}`); r.Code != 503 {
		t.Fatal("allowed plaintext fallback")
	}
}

func TestHubRBAC(t *testing.T) {
	for _, tc := range []struct {
		p            auth.Principal
		method, path string
		want         bool
	}{
		{auth.Principal{Roles: []string{"user"}, Provisioned: true, Services: []string{"models"}}, "POST", "/api/v1/models/huggingface/import", true},
		{auth.Principal{Roles: []string{"user"}, Provisioned: true, Services: []string{"agents"}}, "GET", "/api/v1/models/huggingface", false},
		{auth.Principal{Roles: []string{"viewer"}}, "POST", "/api/v1/models/huggingface/import", false},
		{auth.Principal{Roles: []string{"admin"}, Credential: "api_token"}, "GET", "/api/v1/settings/huggingface", false},
		{auth.Principal{Roles: []string{"service"}}, "GET", "/api/v1/settings/huggingface", false},
	} {
		if got := auth.Allowed(tc.p, tc.method, tc.path); got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
}

func TestHubImportRejectsUnassignedProjectBeforeContactingHub(t *testing.T) {
	mockHub(t, func(r *http.Request) (int, string) { t.Fatal("unassigned import contacted Hub"); return 500, "{}" })
	server := &Server{store: store.New()}
	r := httptest.NewRequest("POST", "/api/v1/models/huggingface/import", strings.NewReader(`{"project_id":"prj-demo","repo_id":"org/model"}`))
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Subject: "alice", Roles: []string{"user"}, Provisioned: true, Services: []string{"models"}, ProjectIDs: []string{"another-project"}}))
	w := httptest.NewRecorder()
	server.importHuggingFaceModel(w, r)
	if w.Code != 403 {
		t.Fatalf("unassigned import: %d %s", w.Code, w.Body)
	}
}
