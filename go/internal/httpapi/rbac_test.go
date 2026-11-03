package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMeReportsIdentityAndPermissions(t *testing.T) {
	request := newTestRequest(http.MethodGet, "/api/v1/me", nil)
	response := httptest.NewRecorder()
	testServer().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Subject     string          `json:"subject"`
		Roles       []string        `json:"roles"`
		Mode        string          `json:"mode"`
		Permissions map[string]bool `json:"permissions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Subject != "local-dev" || payload.Mode != "local" {
		t.Fatalf("unexpected identity: %+v", payload)
	}
	if !payload.Permissions["projects_write"] || !payload.Permissions["connections_write"] {
		t.Fatalf("default local admin must hold all permissions: %+v", payload.Permissions)
	}
}

func TestMeAndEnforcementForViewerRole(t *testing.T) {
	t.Setenv("MLAIOPS_LOCAL_ROLE", "viewer")
	server := testServer()

	me := httptest.NewRecorder()
	server.ServeHTTP(me, newTestRequest(http.MethodGet, "/api/v1/me", nil))
	var payload struct {
		Permissions map[string]bool `json:"permissions"`
	}
	if err := json.Unmarshal(me.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for key, allowed := range payload.Permissions {
		if allowed {
			t.Fatalf("viewer must not hold %s", key)
		}
	}

	denied := httptest.NewRecorder()
	server.ServeHTTP(denied, newTestRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"nope","template":"blank"}`)))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("viewer POST must be 403, got %d %s", denied.Code, denied.Body.String())
	}

	allowed := httptest.NewRecorder()
	server.ServeHTTP(allowed, newTestRequest(http.MethodGet, "/api/v1/projects", nil))
	if allowed.Code != http.StatusOK {
		t.Fatalf("viewer GET must be 200, got %d", allowed.Code)
	}
}

func TestEngineerRoleBoundary(t *testing.T) {
	t.Setenv("MLAIOPS_LOCAL_ROLE", "engineer")
	server := testServer()

	create := httptest.NewRecorder()
	server.ServeHTTP(create, newTestRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"Fraud model","template":"tabular-classification"}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("engineer must create projects, got %d %s", create.Code, create.Body.String())
	}

	connection := httptest.NewRecorder()
	server.ServeHTTP(connection, newTestRequest(http.MethodPost, "/api/v1/connections", strings.NewReader(`{"type":"mlflow","name":"x","endpoint":"http://x","secret_ref":"x"}`)))
	if connection.Code != http.StatusForbidden {
		t.Fatalf("engineer must not manage connections, got %d %s", connection.Code, connection.Body.String())
	}
}
