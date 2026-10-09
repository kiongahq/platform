package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

func TestAdminSetsLocalPasswordOnlyForProvisionedUsersInLocalMode(t *testing.T) {
	data := store.New()
	s := &Server{store: data}
	put := func(subject, body string) int {
		request := httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/"+subject+"/local-password", strings.NewReader(body))
		request.SetPathValue("subject", subject)
		response := httptest.NewRecorder()
		s.setLocalPassword(response, request)
		return response.Code
	}
	if code := put("alice", `{"password":"long-enough-password"}`); code != http.StatusNotFound {
		t.Fatalf("unprovisioned subject: %d", code)
	}
	if _, err := data.UpsertUserAccess("alice", api.UpsertUserAccessRequest{Role: "user", Services: []string{"projects"}}, "admin"); err != nil {
		t.Fatal(err)
	}
	if code := put("alice", `{"password":"short"}`); code != http.StatusBadRequest {
		t.Fatalf("weak password: %d", code)
	}
	if code := put("alice", `{"password":"long-enough-password"}`); code != http.StatusOK {
		t.Fatalf("valid password: %d", code)
	}
	if !store.VerifyLocalPassword(data, "alice", "long-enough-password") {
		t.Fatal("password not stored")
	}
	t.Setenv("OIDC_ISSUER", "https://idp.example")
	if code := put("alice", `{"password":"another-long-password"}`); code != http.StatusConflict {
		t.Fatalf("OIDC mode must refuse local passwords: %d", code)
	}
}
