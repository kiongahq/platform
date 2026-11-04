package httpapi

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kiongahq/platform/internal/agentteam"
	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
)

func TestTeamDefinitionLifecycle(t *testing.T) {
	forEachTeamStore(t, func(t *testing.T, repository store.Repository) {
		h := newTeamHarness(t, repository)
		created := decodeTeam[agentDefinitionDoc](t, h.api("POST", "/api/v1/agent-definitions", mustJSON(t, map[string]any{"project_id": h.project, "name": "bug-fixer", "description": "Fix bugs", "instructions": "Fix"}), 201))
		if !agentDefinitionID.MatchString(created.ID) || created.Revision != 1 {
			t.Fatalf("%+v", created)
		}
		// The contract's defaults are stored.
		content := created.Content
		if content.Model != "llm" || content.Workspace != "none" || content.Limits.MaxTurns != 100 || content.Limits.MaxTokens != 200000 || content.Limits.MaxSeconds != 3600 || content.Tools == nil {
			t.Fatalf("defaults: %+v", content)
		}
		validateContract(t, "definition", created)

		update := func(revision int) string {
			return mustJSON(t, map[string]any{"revision": revision, "name": "bug-fixer", "description": "Fix bugs", "instructions": "Fix better"})
		}
		path := "/api/v1/agent-definitions/" + created.ID
		saved := decodeTeam[agentDefinitionDoc](t, h.api("PUT", path, update(1), 200))
		if saved.Revision != 2 || saved.Content.Instructions != "Fix better" {
			t.Fatalf("%+v", saved)
		}
		stale := h.callAs(h.starter, "PUT", path, update(1))
		if stale.Code != 409 {
			t.Fatalf("stale save: %d %s", stale.Code, stale.Body.String())
		}
		conflict := decodeTeam[map[string]json.RawMessage](t, stale.Body.Bytes())
		if latest := decodeTeam[agentDefinitionDoc](t, conflict["latest"]); latest.Revision != 2 {
			t.Fatalf("latest: %s", conflict["latest"])
		}
		if string(conflict["error"]) != `"revision_conflict"` {
			t.Fatalf("%s", stale.Body.String())
		}
		// Keeps exactly the last 20 revisions, newest first.
		for revision := 2; revision < 26; revision++ {
			h.api("PUT", path, update(revision), 200)
		}
		revisions := decodeTeam[map[string][]revisionEntry[agentteam.DefinitionContent]](t, h.api("GET", path+"/revisions", "", 200))["items"]
		if len(revisions) != 20 || revisions[0].Revision != 26 || revisions[19].Revision != 7 {
			t.Fatalf("revisions: %d first %d", len(revisions), revisions[0].Revision)
		}
		listed := decodeTeam[map[string][]json.RawMessage](t, h.api("GET", "/api/v1/agent-definitions", "", 200))["items"]
		if len(listed) != 1 || strings.Contains(string(listed[0]), "history") {
			t.Fatalf("list exposes history: %s", listed)
		}
		single := h.api("GET", path, "", 200)
		if strings.Contains(string(single), "history") {
			t.Fatalf("get exposes history: %s", single)
		}
		duplicate := h.callAs(h.starter, "POST", "/api/v1/agent-definitions", mustJSON(t, map[string]any{"project_id": h.project, "name": "bug-fixer", "description": "x", "instructions": "x"}))
		if duplicate.Code != 409 {
			t.Fatalf("duplicate name: %d", duplicate.Code)
		}
		team := h.team([]string{created.ID}, nil)
		if inUse := h.callAs(h.starter, "DELETE", path, ""); inUse.Code != 409 {
			t.Fatalf("delete in use: %d %s", inUse.Code, inUse.Body.String())
		}
		h.api("DELETE", "/api/v1/agent-teams/"+team, "", 204)
		h.api("DELETE", path, "", 204)
		h.api("GET", path, "", 404)
	})
}

func TestTeamValidationAtSave(t *testing.T) {
	h := newTeamHarness(t, store.New())
	member := h.definition("worker-one", nil)
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"concurrent above total", map[string]any{"coordinator": map[string]any{"instructions": "x"}, "members": []string{member}, "limits": map[string]any{"max_workers_total": 2, "max_concurrent_workers": 3}}, "cannot exceed"},
		{"default concurrent above total", map[string]any{"coordinator": map[string]any{"instructions": "x"}, "members": []string{member}, "limits": map[string]any{"max_workers_total": 2}}, "cannot exceed"},
		{"design id", map[string]any{"coordinator": map[string]any{"instructions": "x"}, "members": []string{"agd-" + strings.Repeat("a", 24)}}, "adf-"},
		{"missing member", map[string]any{"coordinator": map[string]any{"instructions": "x"}, "members": []string{"adf-" + strings.Repeat("a", 24)}}, "not found"},
		{"no coordinator", map[string]any{"members": []string{member}}, "coordinator"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.body["project_id"], tc.body["name"] = h.project, "a-team"
			response := h.callAs(h.starter, "POST", "/api/v1/agent-teams", mustJSON(t, tc.body))
			if response.Code != 422 || !strings.Contains(response.Body.String(), tc.want) {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
		})
	}
	team := decodeTeam[agentTeamDoc](t, h.api("GET", "/api/v1/agent-teams/"+h.team([]string{member}, nil), "", 200))
	validateContract(t, "team", team)
}

func TestTeamBodiesAreClosedAndGrantsChecked(t *testing.T) {
	h := newTeamHarness(t, store.New())
	unknown := mustJSON(t, map[string]any{"project_id": h.project, "name": "x-y", "description": "d", "instructions": "i", "secret": "s"})
	if response := h.callAs(h.starter, "POST", "/api/v1/agent-definitions", unknown); response.Code != 400 {
		t.Fatalf("unknown field: %d", response.Code)
	}
	for name, grant := range map[string]map[string]any{
		"connection": {"kind": "connection", "ref": "api-" + strings.Repeat("a", 32)},
		"knowledge":  {"kind": "knowledge", "ref": "kb-" + strings.Repeat("a", 32)},
		"dataset":    {"kind": "dataset", "ref": "ds-" + strings.Repeat("a", 32)},
		"tool":       {"kind": "tool", "ref": "nothing-registered"},
	} {
		body := mustJSON(t, map[string]any{"project_id": h.project, "name": "x-" + name, "description": "d", "instructions": "i", "tools": []any{grant}})
		if response := h.callAs(h.starter, "POST", "/api/v1/agent-definitions", body); response.Code != 422 || !strings.Contains(response.Body.String(), "not available") {
			t.Errorf("%s: %d %s", name, response.Code, response.Body.String())
		}
	}
}

func TestTeamResourcesAreProjectScoped(t *testing.T) {
	h := newTeamHarness(t, store.New())
	id := h.definition("private-one", nil)
	team := h.team([]string{id}, nil)
	outsider := auth.Principal{Subject: "bob", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"agents"}, ProjectIDs: []string{"other"}}
	for _, path := range []string{"/api/v1/agent-definitions/" + id, "/api/v1/agent-definitions/" + id + "/revisions", "/api/v1/agent-teams/" + team} {
		if response := h.callAs(outsider, "GET", path, ""); response.Code != 404 {
			t.Errorf("%s: %d", path, response.Code)
		}
	}
	if items := decodeTeam[map[string][]json.RawMessage](t, h.callAs(outsider, "GET", "/api/v1/agent-definitions", "").Body.Bytes())["items"]; len(items) != 0 {
		t.Errorf("list leaked: %s", items)
	}
	start := h.callAs(outsider, "POST", "/api/v1/team-runs", mustJSON(t, map[string]any{"team_id": team, "input": "x"}))
	if start.Code != 404 {
		t.Errorf("foreign start: %d", start.Code)
	}
	for _, credential := range []string{designRunCredential, "workspace"} {
		machine := auth.Principal{Subject: "alice", Roles: []string{auth.RoleEngineer}, Credential: credential, WorkspaceProjectID: h.project}
		if response := h.callAs(machine, "POST", "/api/v1/agent-definitions", mustJSON(t, map[string]any{"project_id": h.project, "name": "m-one", "description": "d", "instructions": "i"})); response.Code != 403 {
			t.Errorf("%s creates definition: %d", credential, response.Code)
		}
		if response := h.callAs(machine, "POST", "/api/v1/team-runs", mustJSON(t, map[string]any{"team_id": team, "input": "x"})); response.Code != 403 {
			t.Errorf("%s starts run: %d", credential, response.Code)
		}
	}
}

func TestTeamRoutesRBAC(t *testing.T) {
	viewer := auth.Principal{Subject: "v", Roles: []string{auth.RoleViewer}}
	engineer := auth.Principal{Subject: "e", Roles: []string{auth.RoleEngineer}}
	withAgents := auth.Principal{Subject: "u", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"agents"}}
	withoutAgents := auth.Principal{Subject: "u", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"models"}}
	service := auth.Principal{Subject: "s", Roles: []string{auth.RoleService}}
	for _, path := range []string{"/api/v1/agent-definitions", "/api/v1/agent-teams", "/api/v1/team-runs", "/api/v1/team-runs/x/cancel", "/api/v1/team-runs/x/messages"} {
		cases := []struct {
			name   string
			who    auth.Principal
			method string
			want   bool
		}{
			{"viewer read", viewer, "GET", true}, {"viewer write", viewer, "POST", false},
			{"engineer write", engineer, "POST", true}, {"user read", withAgents, "GET", true}, {"user write", withAgents, "POST", true},
			{"no service read", withoutAgents, "GET", false}, {"no service write", withoutAgents, "POST", false},
			{"service read", service, "GET", false}, {"service write", service, "POST", false},
		}
		for _, tc := range cases {
			if got := auth.Allowed(tc.who, tc.method, path); got != tc.want {
				t.Errorf("%s %s %s: %v", tc.name, tc.method, path, got)
			}
		}
	}
}

func TestTeamOpenAPIListsEveryRoute(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&Server{}).openapi(recorder, httptest.NewRequest("GET", "/api/openapi.json", nil))
	document := decodeTeam[struct {
		Paths map[string]map[string]any `json:"paths"`
	}](t, recorder.Body.Bytes())
	want := map[string][]string{
		"/api/v1/agent-definitions":                {"get", "post"},
		"/api/v1/agent-definitions/{id}":           {"get", "put", "delete"},
		"/api/v1/agent-definitions/{id}/revisions": {"get"},
		"/api/v1/agent-teams":                      {"get", "post"},
		"/api/v1/agent-teams/{id}":                 {"get", "put", "delete"},
		"/api/v1/agent-teams/{id}/revisions":       {"get"},
		"/api/v1/team-runs":                        {"get", "post"},
		"/api/v1/team-runs/{id}":                   {"get"},
		"/api/v1/team-runs/{id}/events":            {"get"},
		"/api/v1/team-runs/{id}/events/stream":     {"get"},
		"/api/v1/team-runs/{id}/workers":           {"get"},
		"/api/v1/team-runs/{id}/workers/{wid}":     {"get"},
		"/api/v1/team-runs/{id}/messages":          {"post"},
		"/api/v1/team-runs/{id}/cancel":            {"post"},
		"/api/v1/team-runs/{id}/resume":            {"post"},
	}
	for path, methods := range want {
		for _, method := range methods {
			if document.Paths[path][method] == nil {
				t.Errorf("openapi lacks %s %s", method, path)
			}
		}
	}
}

func TestTeamConsoleViewsAreServed(t *testing.T) {
	files := fstest.MapFS{}
	views := []string{"agent-definitions", "agent-teams", "team-runs"}
	for _, view := range views {
		files["console/"+view+".html"] = &fstest.MapFile{Data: []byte(view)}
	}
	handler := frontendHandler(fs.FS(files))
	for _, view := range views {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/console/"+view, nil))
		if response.Code != 200 || response.Body.String() != view {
			t.Errorf("%s: %d %s", view, response.Code, response.Body.String())
		}
	}
}
