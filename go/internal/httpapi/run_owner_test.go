package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/pkg/api"
)

// ownerFixture adds runs owned by different people to the first project:
// user-1 (profile email user-1@example.com), a subject with no access
// profile, and a run whose owner_subject is already a token email.
type ownerFixture struct {
	*iamFixture
	byOwner map[string]string
}

func newOwnerFixture(t *testing.T) *ownerFixture {
	t.Helper()
	f := &ownerFixture{iamFixture: newIAMFixture(t), byOwner: map[string]string{}}
	f.provision("user-2", "user", []string{f.p1}, "overview", "projects", "pipelines")
	for _, owner := range []string{"user-1", "ghost", "someone@corp.example"} {
		run, err := f.repo.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: f.p1, Name: "train"}, owner)
		if err != nil {
			t.Fatal(err)
		}
		f.byOwner[owner] = run.ID
	}
	return f
}

// labels returns owner_label by owner_subject, from the list and (checked to
// agree) from each run's detail response.
func (f *ownerFixture) labels(caller auth.Principal) map[string]string {
	f.t.Helper()
	call := func(path string) *httptest.ResponseRecorder {
		request := newTestRequest(http.MethodGet, path, nil)
		request = request.WithContext(auth.WithPrincipal(request.Context(), caller))
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, request)
		expectStatus(f.t, response, http.StatusOK, caller.Subject+" "+path)
		return response
	}
	out := map[string]string{}
	for _, run := range decodeBody[[]pipelineRunView](f.t, call("/api/v1/pipelines/runs")) {
		id, known := f.byOwner[run.OwnerSubject]
		if !known || id != run.ID {
			continue
		}
		detail := decodeBody[pipelineRunView](f.t, call("/api/v1/pipelines/runs/"+run.ID))
		if detail.OwnerLabel != run.OwnerLabel || detail.OwnerSubject != run.OwnerSubject {
			f.t.Fatalf("%s: list says %q, detail says %q", run.ID, run.OwnerLabel, detail.OwnerLabel)
		}
		out[run.OwnerSubject] = run.OwnerLabel
	}
	if len(out) != len(f.byOwner) {
		f.t.Fatalf("%s sees %d of %d fixture runs", caller.Subject, len(out), len(f.byOwner))
	}
	return out
}

func TestRunOwnerLabelFollowsWhoMayReadTheUserDirectory(t *testing.T) {
	f := newOwnerFixture(t)
	f.provision("ops-1", "operator", nil, "pipelines")
	f.provision("eng-1", "engineer", nil, "pipelines")
	f.provision("view-1", "viewer", nil, "pipelines")

	resolved := map[string]string{"user-1": "user-1@example.com", "ghost": "ghost", "someone@corp.example": "someone@corp.example"}
	subjectOnly := map[string]string{"user-1": "user-1", "ghost": "ghost", "someone@corp.example": "someone@corp.example"}
	for _, tc := range []struct {
		name   string
		caller auth.Principal
		want   map[string]string
	}{
		{"administrator", auth.Principal{Subject: "root", Roles: []string{auth.RoleAdmin}}, resolved},
		{"operator", auth.Principal{Subject: "ops-1", Roles: []string{auth.RoleUser}}, resolved},
		{"project co-member", auth.Principal{Subject: "user-2", Roles: []string{auth.RoleUser}}, subjectOnly},
		{"engineer", auth.Principal{Subject: "eng-1", Roles: []string{auth.RoleUser}}, subjectOnly},
		{"viewer", auth.Principal{Subject: "view-1", Roles: []string{auth.RoleUser}}, subjectOnly},
		{"machine identity", auth.Principal{Subject: "runner", Roles: []string{auth.RoleService}}, subjectOnly},
		{"owner without a token email", auth.Principal{Subject: "user-1", Roles: []string{auth.RoleUser}}, resolved},
		{"owner with a token email", auth.Principal{Subject: "user-1", Email: "u1@idp.example", Roles: []string{auth.RoleUser}},
			map[string]string{"user-1": "u1@idp.example", "ghost": "ghost", "someone@corp.example": "someone@corp.example"}},
	} {
		got := f.labels(tc.caller)
		for owner, want := range tc.want {
			if got[owner] != want {
				t.Errorf("%s: owner %q labelled %q, want %q", tc.name, owner, got[owner], want)
			}
		}
	}

	// Nobody but directory readers and the owner receives user-1's email
	// anywhere in the response.
	for _, caller := range []string{"user-2", "eng-1", "view-1"} {
		response := f.as(caller, []string{auth.RoleUser}, http.MethodGet, "/api/v1/pipelines/runs", "")
		if strings.Contains(response.Body.String(), "user-1@example.com") {
			t.Fatalf("%s received another person's email: %s", caller, response.Body.String())
		}
	}
}

func TestRunOwnerLabelEdges(t *testing.T) {
	f := newOwnerFixture(t)
	admin := auth.Principal{Subject: "root", Roles: []string{auth.RoleAdmin}}

	// A profile without an email resolves to the subject.
	if _, err := f.repo.UpsertUserAccess("user-1", api.UpsertUserAccessRequest{Role: "user", Services: []string{"pipelines"}, ProjectIDs: []string{f.p1}, Compute: api.ComputeGrant{Profile: "starter"}}, "admin"); err != nil {
		t.Fatal(err)
	}
	if got := f.labels(admin)["user-1"]; got != "user-1" {
		t.Fatalf("profile without an email: %q", got)
	}

	// The owner matched by token email (owner_subject holds the email) sees it.
	self := f.labels(auth.Principal{Subject: "sub-9", Email: "someone@corp.example", Roles: []string{auth.RoleAdmin}})
	if self["someone@corp.example"] != "someone@corp.example" {
		t.Fatalf("owner matched by email: %+v", self)
	}

	// A run with no recorded owner has no label, and the label is not stored.
	labels := &ownerLabels{store: f.repo, caller: admin, directory: true}
	if view := labels.run(api.PipelineRun{ID: "old"}); view.OwnerLabel != "" {
		t.Fatalf("run without an owner got label %q", view.OwnerLabel)
	}
	stored, err := f.repo.Run(f.byOwner["ghost"])
	if err != nil || stored.OwnerSubject != "ghost" {
		t.Fatalf("stored run changed: %+v %v", stored, err)
	}

	// An empty caller subject never matches an empty or unrelated owner.
	anonymous := &ownerLabels{store: f.repo, caller: auth.Principal{}}
	if got := anonymous.label("user-2"); got != "user-2" {
		t.Fatalf("anonymous caller: %q", got)
	}
}
