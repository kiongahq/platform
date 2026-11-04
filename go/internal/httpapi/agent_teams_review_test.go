package httpapi

import (
	"context"
	"github.com/kiongahq/platform/internal/agentteam"
	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
	"net/http"
	"testing"
)

func TestTeamInternalCallServiceDenied(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{"POST", "/api/v1/functions/test/invoke"},
		{"POST", "/api/v1/agent-api-connections/test/invoke"},
		{"POST", "/api/v1/knowledge-bases/test/search"},
		{"GET", "/api/v1/datasets/test/objects"},
		{"GET", "/api/v1/datasets/test/object"},
	} {
		t.Run(route.path, func(t *testing.T) {
			h := newTeamHarness(t, store.New())
			h.profile("starter", func(r *api.UpsertUserAccessRequest) { r.Role = auth.RoleUser })
			called := false
			result := h.server.internalCall(context.Background(), auth.Principal{Subject: "starter", Roles: []string{auth.RoleAdmin}, Provisioned: true}, func(http.ResponseWriter, *http.Request) { called = true }, route.method, route.path, nil, nil, nil)
			if called || result.code != agentteam.ErrAccessDenied {
				t.Fatalf("handler executed=%v, outcome=%+v", called, result)
			}
		})
	}
}
