package auth

import (
	"net/http"
	"testing"
)

// Starting and stopping a developer workspace opens for the same people who
// may launch it, and for nobody who may only read.
func TestWorkspaceStartStopFollowsLaunchAccess(t *testing.T) {
	cases := []struct {
		name      string
		principal Principal
		path      string
		want      bool
	}{
		{"engineer start", Principal{Roles: []string{RoleEngineer}}, "/api/v1/workspaces/workbench/start", true},
		{"engineer stop", Principal{Roles: []string{RoleEngineer}}, "/api/v1/workspaces/ide/stop", true},
		{"admin stop", Principal{Roles: []string{RoleAdmin}}, "/api/v1/workspaces/ide/stop", true},
		{"viewer start", Principal{Roles: []string{RoleViewer}}, "/api/v1/workspaces/workbench/start", false},
		{"service stop", Principal{Roles: []string{RoleService}}, "/api/v1/workspaces/workbench/stop", false},
		{"api token", Principal{Roles: []string{RoleEngineer}, Credential: "api_token"}, "/api/v1/workspaces/workbench/start", false},
		{"user with service", Principal{Roles: []string{RoleUser}, Provisioned: true, Services: []string{"ide"}}, "/api/v1/workspaces/ide/stop", true},
		{"user without service", Principal{Roles: []string{RoleUser}, Provisioned: true, Services: []string{"ide"}}, "/api/v1/workspaces/workbench/stop", false},
		{"unprovisioned user", Principal{Roles: []string{RoleUser}, Services: []string{"ide"}}, "/api/v1/workspaces/ide/start", false},
	}
	for _, tc := range cases {
		if got := Allowed(tc.principal, http.MethodPost, tc.path); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
