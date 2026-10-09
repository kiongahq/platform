package httpapi

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ml-ai-ops/platform/internal/auth"
	"github.com/ml-ai-ops/platform/internal/policy"
	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

// iamFixture is a server with two projects, a run and a definition in each,
// and a normal user assigned only to the first project.
type iamFixture struct {
	t        *testing.T
	handler  http.Handler
	server   *Server
	repo     *store.Store
	p1, p2   string
	run1     string
	run2     string
	def1     string
	def2     string
	userName string
}

func newIAMFixture(t *testing.T) *iamFixture {
	t.Helper()
	repo := store.New()
	p1, err := repo.CreateProject(api.CreateProjectRequest{Name: "Alpha project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := repo.CreateProject(api.CreateProjectRequest{Name: "Bravo project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	f := &iamFixture{t: t, repo: repo, p1: p1.ID, p2: p2.ID, userName: "user-1"}
	for i, project := range []string{p1.ID, p2.ID} {
		run, err := repo.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: project, Name: "train"}, "admin")
		if err != nil {
			t.Fatal(err)
		}
		definition, err := repo.UpsertPipelineDefinition("", api.UpsertPipelineDefinitionRequest{ProjectID: project, Name: "flow", Version: "1", ExecutionMode: "prefect", Jobs: []api.PipelineJob{{Name: "a", Kind: "container", Image: "busybox"}}}, "admin")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			f.run1, f.def1 = run.ID, definition.ID
		} else {
			f.run2, f.def2 = run.ID, definition.ID
		}
	}
	f.provision("user-1", "user", []string{p1.ID}, "overview", "projects", "pipelines", "features", "workbench", "ide")
	static, _ := fs.Sub(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, ".")
	f.handler = New(repo, static)
	f.server = &Server{store: repo, realtime: map[string]map[string]any{}}
	return f
}

func (f *iamFixture) provision(subject, role string, projects []string, services ...string) {
	f.t.Helper()
	if _, err := f.repo.UpsertUserAccess(subject, api.UpsertUserAccessRequest{
		Email: subject + "@example.com", Role: role, Services: services, ProjectIDs: projects,
		Storage: api.StorageGrant{SizeGB: 10}, Compute: api.ComputeGrant{Profile: "starter"},
	}, "admin"); err != nil {
		f.t.Fatal(err)
	}
}

// as sends a request as subject with roles; the RBAC resolver replaces the
// roles with the subject's access profile when one exists.
func (f *iamFixture) as(subject string, roles []string, method, path, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: subject, Roles: roles}))
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func (f *iamFixture) admin(method, path, body string) *httptest.ResponseRecorder {
	return f.as("root", []string{auth.RoleAdmin}, method, path, body)
}

func (f *iamFixture) user(method, path, body string) *httptest.ResponseRecorder {
	return f.as(f.userName, []string{auth.RoleUser}, method, path, body)
}

func expectStatus(t *testing.T, response *httptest.ResponseRecorder, want int, label string) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("%s: status %d, want %d: %s", label, response.Code, want, response.Body.String())
	}
}

func decodeBody[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s: %v", response.Body.String(), err)
	}
	return value
}

type deniedBody struct {
	Error    string          `json:"error"`
	Message  string          `json:"message"`
	Decision policy.Decision `json:"decision"`
	Details  []policy.Issue  `json:"details"`
}

func TestNormalUserCrossProjectIsolation(t *testing.T) {
	f := newIAMFixture(t)
	runs := decodeBody[[]api.PipelineRun](t, f.user(http.MethodGet, "/api/v1/pipelines/runs", ""))
	for _, run := range runs {
		if run.ProjectID != f.p1 {
			t.Fatalf("user sees a run from %s", run.ProjectID)
		}
	}
	if len(runs) == 0 {
		t.Fatal("user must see their own project's runs")
	}
	expectStatus(t, f.user(http.MethodGet, "/api/v1/pipelines/runs/"+f.run1, ""), http.StatusOK, "own run")
	expectStatus(t, f.user(http.MethodGet, "/api/v1/pipelines/runs/"+f.run2, ""), http.StatusNotFound, "other project run")
	expectStatus(t, f.user(http.MethodPost, "/api/v1/pipelines/runs/"+f.run2+"/cancel", ""), http.StatusNotFound, "cancel other project run")
	expectStatus(t, f.user(http.MethodPost, "/api/v1/pipelines/runs/"+f.run2+"/retry", ""), http.StatusNotFound, "retry other project run")

	definitions := decodeBody[api.Page[api.PipelineDefinition]](t, f.user(http.MethodGet, "/api/v1/pipelines/definitions", ""))
	if len(definitions.Items) != 1 || definitions.Items[0].ID != f.def1 {
		t.Fatalf("definitions: %+v", definitions.Items)
	}
	expectStatus(t, f.user(http.MethodGet, "/api/v1/pipelines/definitions/"+f.def2, ""), http.StatusNotFound, "other definition")
	expectStatus(t, f.user(http.MethodGet, "/api/v1/pipelines/definitions/"+f.def2+"/revisions", ""), http.StatusNotFound, "other definition revisions")

	submit := f.user(http.MethodPost, "/api/v1/pipelines/submit", `{"project_id":"`+f.p2+`","name":"train"}`)
	expectStatus(t, submit, http.StatusForbidden, "submit to other project")
	body := decodeBody[deniedBody](t, submit)
	if body.Decision.DecidedBy != policy.DefaultDeny || body.Decision.Action != policy.PipelineRun || body.Error != "access_denied" {
		t.Fatalf("denial must carry the decision: %+v", body)
	}
	write := f.user(http.MethodPost, "/api/v1/pipelines/definitions", `{"project_id":"`+f.p2+`","name":"x","version":"1","execution_mode":"prefect","jobs":[{"name":"a","kind":"container","image":"busybox"}]}`)
	expectStatus(t, write, http.StatusForbidden, "definition in other project")

	t.Setenv("KIONGA_JUPYTER_UPSTREAM", "http://127.0.0.1:1/{subject}")
	launch := f.user(http.MethodPost, "/api/v1/workspaces/workbench/launch", `{"project_id":"`+f.p2+`"}`)
	expectStatus(t, launch, http.StatusForbidden, "workspace launch into other project")
	if decodeBody[deniedBody](t, launch).Decision.Action != policy.WorkspaceOpen {
		t.Fatalf("launch denial: %s", launch.Body.String())
	}
	if own := f.user(http.MethodPost, "/api/v1/workspaces/workbench/launch", `{"project_id":"`+f.p1+`"}`); own.Code == http.StatusForbidden {
		t.Fatalf("own project launch must pass authorization: %s", own.Body.String())
	}
}

func TestViewerCannotWriteAndScopedViewerSeesOnlyAssignedProjects(t *testing.T) {
	f := newIAMFixture(t)
	viewer := func(method, path, body string) *httptest.ResponseRecorder {
		return f.as("vera", []string{auth.RoleViewer}, method, path, body)
	}
	expectStatus(t, viewer(http.MethodPost, "/api/v1/pipelines/submit", `{"project_id":"`+f.p1+`","name":"train"}`), http.StatusForbidden, "viewer submit")
	expectStatus(t, viewer(http.MethodPost, "/api/v1/pipelines/runs/"+f.run1+"/cancel", ""), http.StatusForbidden, "viewer cancel")
	expectStatus(t, viewer(http.MethodPost, "/api/v1/features", `{"name":"f","entity":"e","fields":[]}`), http.StatusForbidden, "viewer feature write")
	if runs := decodeBody[[]api.PipelineRun](t, viewer(http.MethodGet, "/api/v1/pipelines/runs", "")); len(runs) < 2 {
		t.Fatalf("unscoped viewer reads every project: %d runs", len(runs))
	}
	f.provision("vera", "viewer", []string{f.p1})
	runs := decodeBody[[]api.PipelineRun](t, viewer(http.MethodGet, "/api/v1/pipelines/runs", ""))
	for _, run := range runs {
		if run.ProjectID != f.p1 {
			t.Fatalf("scoped viewer sees %s", run.ProjectID)
		}
	}
	projects := decodeBody[[]api.Project](t, viewer(http.MethodGet, "/api/v1/projects", ""))
	if len(projects) != 1 || projects[0].ID != f.p1 {
		t.Fatalf("scoped viewer projects: %+v", projects)
	}
	expectStatus(t, viewer(http.MethodGet, "/api/v1/pipelines/runs/"+f.run2, ""), http.StatusNotFound, "scoped viewer other run")
}

func TestScopedEngineerAndRunStepProjectCheck(t *testing.T) {
	f := newIAMFixture(t)
	f.provision("eve", "engineer", []string{f.p1})
	engineer := func(method, path, body string) *httptest.ResponseRecorder {
		return f.as("eve", []string{auth.RoleEngineer}, method, path, body)
	}
	step := `{"step":"train","status":"running"}`
	expectStatus(t, engineer(http.MethodPost, "/api/v1/pipelines/runs/"+f.run2+"/steps", step), http.StatusNotFound, "step report on another project's run")
	if own := engineer(http.MethodPost, "/api/v1/pipelines/runs/"+f.run1+"/steps", step); own.Code == http.StatusNotFound || own.Code == http.StatusForbidden {
		t.Fatalf("own run step must pass authorization: %d %s", own.Code, own.Body.String())
	}
	expectStatus(t, engineer(http.MethodPost, "/api/v1/pipelines/submit", `{"project_id":"`+f.p2+`","name":"train"}`), http.StatusForbidden, "scoped engineer submit elsewhere")
	if runs := decodeBody[[]api.PipelineRun](t, engineer(http.MethodGet, "/api/v1/pipelines/runs", "")); len(runs) == 0 || runs[0].ProjectID != f.p1 {
		t.Fatalf("scoped engineer runs: %+v", runs)
	}
	// Services may still report steps for any run.
	t.Setenv("MLAIOPS_INTERNAL_TOKEN", "svc-token")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/runs/"+f.run2+"/steps", strings.NewReader(step))
	request.Header.Set("Authorization", "Bearer svc-token")
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	if response.Code == http.StatusNotFound || response.Code == http.StatusForbidden {
		t.Fatalf("service step report: %d %s", response.Code, response.Body.String())
	}
}

func createPolicy(t *testing.T, f *iamFixture, body string) policy.Policy {
	t.Helper()
	response := f.admin(http.MethodPost, "/api/v1/admin/iam/policies", body)
	expectStatus(t, response, http.StatusCreated, "create policy")
	return decodeBody[policy.Policy](t, response)
}

func TestPolicyCRUDVersionsAndValidation(t *testing.T) {
	f := newIAMFixture(t)
	created := createPolicy(t, f, `{"name":"Read alpha","statements":[{"sid":"Read","effect":"allow","actions":["pipeline:Read"],"resources":["kionga:project/`+f.p1+`/*"]}]}`)
	if created.ID != "read-alpha" || created.Version != 1 {
		t.Fatalf("created: %+v", created)
	}
	updated := f.admin(http.MethodPut, "/api/v1/admin/iam/policies/read-alpha", `{"name":"Read alpha","description":"v2","version":1,"statements":[{"sid":"Read","effect":"allow","actions":["pipeline:Read","logs:Read"],"resources":["kionga:project/`+f.p1+`/*"]}]}`)
	expectStatus(t, updated, http.StatusOK, "update")
	if decodeBody[policy.Policy](t, updated).Version != 2 {
		t.Fatalf("update must bump version: %s", updated.Body.String())
	}
	expectStatus(t, f.admin(http.MethodPut, "/api/v1/admin/iam/policies/read-alpha", `{"name":"Read alpha","version":1,"statements":[{"effect":"allow","actions":["*"],"resources":["*"]}]}`), http.StatusConflict, "stale version")
	revisions := decodeBody[struct {
		Items []store.PolicyRevision `json:"items"`
	}](t, f.admin(http.MethodGet, "/api/v1/admin/iam/policies/read-alpha/revisions", ""))
	if len(revisions.Items) != 2 || revisions.Items[1].Policy.Description != "" {
		t.Fatalf("revisions: %+v", revisions.Items)
	}
	expectStatus(t, f.admin(http.MethodGet, "/api/v1/admin/iam/policies/read-alpha/revisions/1", ""), http.StatusOK, "revision 1")

	invalid := f.admin(http.MethodPost, "/api/v1/admin/iam/policies", `{"name":"Bad","statements":[{"effect":"permit","actions":["pipeline:Launch"],"resources":["project/x"]}]}`)
	expectStatus(t, invalid, http.StatusUnprocessableEntity, "invalid policy")
	details := decodeBody[deniedBody](t, invalid).Details
	fields := map[string]bool{}
	for _, issue := range details {
		fields[issue.Field] = true
	}
	for _, want := range []string{"statements[0].effect", "statements[0].actions[0]", "statements[0].resources[0]"} {
		if !fields[want] {
			t.Errorf("missing field detail %s in %+v", want, details)
		}
	}
	expectStatus(t, f.admin(http.MethodPost, "/api/v1/admin/iam/policies", `{"id":"kionga-admin","name":"Hijack","statements":[{"effect":"allow","actions":["*"],"resources":["*"]}]}`), http.StatusUnprocessableEntity, "reserved id")
	expectStatus(t, f.admin(http.MethodPost, "/api/v1/admin/iam/policies", `{"name":"x","bogus":1}`), http.StatusBadRequest, "unknown field")

	list := decodeBody[struct {
		Items []policy.Policy `json:"items"`
	}](t, f.admin(http.MethodGet, "/api/v1/admin/iam/policies", ""))
	managed := 0
	for _, item := range list.Items {
		if item.Managed {
			managed++
		}
	}
	if managed != 6 || len(list.Items) != 7 {
		t.Fatalf("list must include six built-ins and one custom policy: %d/%d", managed, len(list.Items))
	}
	expectStatus(t, f.admin(http.MethodDelete, "/api/v1/admin/iam/policies/kionga-admin", ""), http.StatusConflict, "delete built-in")
	expectStatus(t, f.admin(http.MethodPost, "/api/v1/admin/iam/attachments", `{"policy_id":"kionga-viewer","principal_type":"user","principal_id":"user-1"}`), http.StatusUnprocessableEntity, "attach built-in")
	expectStatus(t, f.admin(http.MethodPost, "/api/v1/admin/iam/attachments", `{"policy_id":"read-alpha","principal_type":"user","principal_id":"ghost"}`), http.StatusUnprocessableEntity, "attach to unprovisioned user")
	expectStatus(t, f.admin(http.MethodPost, "/api/v1/admin/iam/attachments", `{"policy_id":"read-alpha","principal_type":"user","principal_id":"user-1"}`), http.StatusCreated, "attach")
	expectStatus(t, f.admin(http.MethodDelete, "/api/v1/admin/iam/policies/read-alpha", ""), http.StatusConflict, "delete attached policy")
	expectStatus(t, f.admin(http.MethodDelete, "/api/v1/admin/iam/attachments/read-alpha:user:user-1", ""), http.StatusNoContent, "detach")
	expectStatus(t, f.admin(http.MethodDelete, "/api/v1/admin/iam/policies/read-alpha", ""), http.StatusNoContent, "delete")

	audit := map[string]bool{}
	for _, event := range f.repo.Audit() {
		audit[event.Action] = true
	}
	for _, want := range []string{"iam.policy.created", "iam.policy.updated", "iam.policy.attached", "iam.policy.detached", "iam.policy.deleted"} {
		if !audit[want] {
			t.Errorf("missing audit %s", want)
		}
	}
}

func TestNormalUserCannotManagePolicies(t *testing.T) {
	f := newIAMFixture(t)
	for _, test := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/admin/iam/policies", ""},
		{http.MethodPost, "/api/v1/admin/iam/policies", `{"name":"Mine","statements":[{"effect":"allow","actions":["*"],"resources":["*"]}]}`},
		{http.MethodPut, "/api/v1/admin/iam/policies/any", `{"name":"Mine","statements":[{"effect":"allow","actions":["*"],"resources":["*"]}]}`},
		{http.MethodPost, "/api/v1/admin/iam/attachments", `{"policy_id":"x","principal_type":"user","principal_id":"user-1"}`},
		{http.MethodPost, "/api/v1/admin/iam/groups", `{"name":"mine","members":["user-1"]}`},
	} {
		expectStatus(t, f.user(test.method, test.path, test.body), http.StatusForbidden, test.method+" "+test.path)
	}
	// A viewer or engineer cannot either; only administrators and operators.
	expectStatus(t, f.as("eve", []string{auth.RoleEngineer}, http.MethodPost, "/api/v1/admin/iam/policies", `{"name":"Mine","statements":[{"effect":"allow","actions":["*"],"resources":["*"]}]}`), http.StatusForbidden, "engineer")
}

func TestGroupPolicyGrantsAndExplicitDenyWins(t *testing.T) {
	f := newIAMFixture(t)
	expectStatus(t, f.user(http.MethodGet, "/api/v1/pipelines/runs/"+f.run2, ""), http.StatusNotFound, "before grant")
	createPolicy(t, f, `{"name":"Read bravo","statements":[{"sid":"ReadBravo","effect":"allow","actions":["project:Read","pipeline:Read"],"resources":["kionga:project/`+f.p2+`","kionga:project/`+f.p2+`/*"]}]}`)
	expectStatus(t, f.admin(http.MethodPost, "/api/v1/admin/iam/groups", `{"name":"Analysts","members":["user-1"]}`), http.StatusCreated, "group")
	expectStatus(t, f.admin(http.MethodPost, "/api/v1/admin/iam/attachments", `{"policy_id":"read-bravo","principal_type":"group","principal_id":"analysts"}`), http.StatusCreated, "attach to group")
	expectStatus(t, f.user(http.MethodGet, "/api/v1/pipelines/runs/"+f.run2, ""), http.StatusOK, "granted through group")
	expectStatus(t, f.user(http.MethodGet, "/api/v1/projects/"+f.p2, ""), http.StatusOK, "project read granted")
	// Read does not imply run.
	expectStatus(t, f.user(http.MethodPost, "/api/v1/pipelines/runs/"+f.run2+"/cancel", ""), http.StatusForbidden, "read-only grant cannot cancel")

	createPolicy(t, f, `{"name":"No alpha runs","statements":[{"sid":"FreezeAlpha","effect":"deny","actions":["pipeline:Run"],"resources":["kionga:project/`+f.p1+`/*"]}]}`)
	expectStatus(t, f.admin(http.MethodPost, "/api/v1/admin/iam/attachments", `{"policy_id":"no-alpha-runs","principal_type":"user","principal_id":"user-1"}`), http.StatusCreated, "attach deny")
	denied := f.user(http.MethodPost, "/api/v1/pipelines/submit", `{"project_id":"`+f.p1+`","name":"train"}`)
	expectStatus(t, denied, http.StatusForbidden, "explicit deny overrides baseline")
	body := decodeBody[deniedBody](t, denied)
	if body.Decision.DecidedBy != "no-alpha-runs@v1#FreezeAlpha" || !strings.Contains(body.Message, "Explicit deny") {
		t.Fatalf("denial must name the statement: %+v", body)
	}
	// Removing the group member removes the grant.
	expectStatus(t, f.admin(http.MethodPut, "/api/v1/admin/iam/groups/analysts", `{"name":"Analysts","members":[]}`), http.StatusOK, "remove member")
	expectStatus(t, f.user(http.MethodGet, "/api/v1/pipelines/runs/"+f.run2, ""), http.StatusNotFound, "after membership removal")
}

func TestRunOverridesNeedOverrideActions(t *testing.T) {
	f := newIAMFixture(t)
	createPolicy(t, f, `{"name":"No image overrides","statements":[{"sid":"NoImages","effect":"deny","actions":["pipeline:OverrideImage"],"resources":["*"]}]}`)
	expectStatus(t, f.admin(http.MethodPost, "/api/v1/admin/iam/attachments", `{"policy_id":"no-image-overrides","principal_type":"user","principal_id":"user-1"}`), http.StatusCreated, "attach")
	response := f.user(http.MethodPost, "/api/v1/pipelines/submit", `{"project_id":"`+f.p1+`","name":"train","overrides":{"nodes":{"a":{"image":"evil:latest"}}}}`)
	expectStatus(t, response, http.StatusForbidden, "image override")
	if decodeBody[deniedBody](t, response).Decision.Action != policy.PipelineOverrideImage {
		t.Fatalf("override denial: %s", response.Body.String())
	}
}

func TestAntiEscalationAndSelfLockout(t *testing.T) {
	f := newIAMFixture(t)
	operator := func(method, path, body string) *httptest.ResponseRecorder {
		return f.as("olga", []string{auth.RoleOperator}, method, path, body)
	}
	f.provision("olga", "operator", nil)
	createPolicy(t, f, `{"name":"No secrets","statements":[{"sid":"NoSecrets","effect":"deny","actions":["secret:Read"],"resources":["*"]}]}`)
	expectStatus(t, f.admin(http.MethodPost, "/api/v1/admin/iam/attachments", `{"policy_id":"no-secrets","principal_type":"user","principal_id":"olga"}`), http.StatusCreated, "restrict operator")
	createPolicy(t, f, `{"name":"Everything","statements":[{"sid":"All","effect":"allow","actions":["*"],"resources":["*"]}]}`)
	createPolicy(t, f, `{"name":"Pipelines","statements":[{"sid":"P","effect":"allow","actions":["pipeline:*"],"resources":["kionga:project/`+f.p1+`/*"]}]}`)

	escalate := operator(http.MethodPost, "/api/v1/admin/iam/attachments", `{"policy_id":"everything","principal_type":"user","principal_id":"user-1"}`)
	expectStatus(t, escalate, http.StatusForbidden, "attach a policy granting more than the actor holds")
	body := decodeBody[deniedBody](t, escalate)
	if body.Error != "privilege_escalation" || len(body.Details) != 1 || !strings.Contains(body.Details[0].Message, "secret:Read") {
		t.Fatalf("escalation detail: %+v", body)
	}
	expectStatus(t, operator(http.MethodPost, "/api/v1/admin/iam/attachments", `{"policy_id":"pipelines","principal_type":"user","principal_id":"user-1"}`), http.StatusCreated, "attach within own access")
	// Editing an attached policy is held to the same rule.
	expectStatus(t, operator(http.MethodPut, "/api/v1/admin/iam/policies/pipelines", `{"name":"Pipelines","statements":[{"effect":"allow","actions":["*"],"resources":["*"]}]}`), http.StatusForbidden, "widen attached policy")
	// Adding members to a group with attachments is too.
	expectStatus(t, f.admin(http.MethodPost, "/api/v1/admin/iam/groups", `{"name":"Power","members":[]}`), http.StatusCreated, "group")
	expectStatus(t, f.admin(http.MethodPost, "/api/v1/admin/iam/attachments", `{"policy_id":"everything","principal_type":"group","principal_id":"power"}`), http.StatusCreated, "admin attaches everything")
	expectStatus(t, operator(http.MethodPut, "/api/v1/admin/iam/groups/power", `{"name":"Power","members":["user-1"]}`), http.StatusForbidden, "add member to powerful group")

	// Self-lockout: an administrator cannot attach to themselves a deny that
	// removes their ability to detach it.
	f.provision("root", "admin", nil)
	createPolicy(t, f, `{"name":"Lock","statements":[{"sid":"Lock","effect":"deny","actions":["policy:Attach"],"resources":["*"]}]}`)
	lockout := f.admin(http.MethodPost, "/api/v1/admin/iam/attachments", `{"policy_id":"lock","principal_type":"user","principal_id":"root"}`)
	expectStatus(t, lockout, http.StatusConflict, "self lockout")
	if !strings.Contains(lockout.Body.String(), "self_lockout") {
		t.Fatalf("lockout body: %s", lockout.Body.String())
	}
}

func TestLastAdminProtection(t *testing.T) {
	f := newIAMFixture(t)
	t.Setenv("OIDC_ISSUER", "https://idp.example.com") // no bootstrap administrator
	f.provision("ada", "admin", nil)
	f.provision("olga", "operator", nil)
	operator := func(method, path, body string) *httptest.ResponseRecorder {
		return f.as("olga", []string{auth.RoleOperator}, method, path, body)
	}
	demote := `{"email":"ada@example.com","role":"user","services":[],"storage":{"size_gb":0},"compute":{"profile":"custom"}}`
	suspend := `{"email":"ada@example.com","role":"admin","services":[],"disabled":true,"storage":{"size_gb":0},"compute":{"profile":"custom"}}`
	expectStatus(t, operator(http.MethodPut, "/api/v1/admin/users/ada", demote), http.StatusConflict, "demote last admin")
	expectStatus(t, operator(http.MethodPut, "/api/v1/admin/users/ada", suspend), http.StatusConflict, "suspend last admin")
	expectStatus(t, operator(http.MethodDelete, "/api/v1/admin/users/ada", ""), http.StatusConflict, "delete last admin")
	f.provision("bea", "admin", nil)
	expectStatus(t, operator(http.MethodPut, "/api/v1/admin/users/ada", demote), http.StatusOK, "demote with another admin")
	expectStatus(t, operator(http.MethodDelete, "/api/v1/admin/users/bea", ""), http.StatusConflict, "now bea is last")
}

func TestExplainAndEffective(t *testing.T) {
	f := newIAMFixture(t)
	self := f.user(http.MethodGet, "/api/v1/iam/explain?action=pipeline:Run&resource="+policy.PipelineResource(f.p2, "x"), "")
	expectStatus(t, self, http.StatusOK, "self explain")
	result := decodeBody[struct {
		Principal map[string]any  `json:"principal"`
		Decision  policy.Decision `json:"decision"`
	}](t, self)
	if result.Decision.Allowed || result.Decision.DecidedBy != policy.DefaultDeny || len(result.Decision.EvaluatedPolicies) != 1 {
		t.Fatalf("self explain: %+v", result.Decision)
	}
	expectStatus(t, f.user(http.MethodGet, "/api/v1/iam/explain?principal=root&action=pipeline:Run&resource=*", ""), http.StatusForbidden, "user explains another principal")
	other := f.admin(http.MethodGet, "/api/v1/iam/explain?principal=user-1&action=pipeline:Run&resource="+policy.PipelineResource(f.p1, "x"), "")
	expectStatus(t, other, http.StatusOK, "admin explains user")
	if !strings.Contains(other.Body.String(), `"allowed":true`) || !strings.Contains(other.Body.String(), "kionga-user@v1#ServicePipelines") {
		t.Fatalf("admin explain: %s", other.Body.String())
	}
	invalid := f.admin(http.MethodGet, "/api/v1/iam/explain?action=pipeline:Fly&resource=nope&at=yesterday", "")
	expectStatus(t, invalid, http.StatusUnprocessableEntity, "invalid explain")
	if details := decodeBody[deniedBody](t, invalid).Details; len(details) != 3 {
		t.Fatalf("explain details: %+v", details)
	}
	effective := f.user(http.MethodGet, "/api/v1/iam/effective", "")
	expectStatus(t, effective, http.StatusOK, "effective")
	if !strings.Contains(effective.Body.String(), `"source":"role:user"`) {
		t.Fatalf("effective: %s", effective.Body.String())
	}
	expectStatus(t, f.user(http.MethodGet, "/api/v1/iam/catalog", ""), http.StatusOK, "catalog")
	// Conditions are simulated from query parameters.
	createPolicy(t, f, `{"name":"Office only","statements":[{"sid":"Office","effect":"deny","actions":["*"],"resources":["*"],"conditions":{"ip_cidr":["203.0.113.0/24"]}}]}`)
	expectStatus(t, f.admin(http.MethodPost, "/api/v1/admin/iam/attachments", `{"policy_id":"office-only","principal_type":"user","principal_id":"user-1"}`), http.StatusCreated, "attach conditional deny")
	simulated := f.admin(http.MethodGet, "/api/v1/iam/explain?principal=user-1&action=pipeline:Run&ip=203.0.113.5&resource="+policy.PipelineResource(f.p1, "x"), "")
	if !strings.Contains(simulated.Body.String(), "office-only@v1#Office") {
		t.Fatalf("conditional explain: %s", simulated.Body.String())
	}
}

func TestEventsDigestReflectsOnlyReadableProjects(t *testing.T) {
	f := newIAMFixture(t)
	read := func(subject string, roles []string) map[string]any {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(auth.WithPrincipal(ctx, auth.Principal{Subject: subject, Roles: roles}))
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, request)
		line, _, _ := strings.Cut(strings.TrimPrefix(response.Body.String(), "data: "), "\n")
		var digest map[string]any
		if err := json.Unmarshal([]byte(line), &digest); err != nil {
			t.Fatalf("digest %q: %v", response.Body.String(), err)
		}
		return digest
	}
	all := read("root", []string{auth.RoleAdmin})
	mine := read(f.userName, []string{auth.RoleUser})
	userRuns := 0
	for _, run := range f.repo.Runs() {
		if run.ProjectID == f.p1 {
			userRuns++
		}
	}
	if int(mine["runs"].(float64)) != userRuns || int(all["runs"].(float64)) != len(f.repo.Runs()) || mine["runs"] == all["runs"] {
		t.Fatalf("digest runs: user %v admin %v (want user %d)", mine["runs"], all["runs"], userRuns)
	}
	if mine["connections"].(float64) != 0 {
		t.Fatalf("user without the platform service must not see connection counts: %v", mine)
	}
	// Activity in another project must not change the user's digest.
	before := mine["latest_run"]
	if _, err := f.repo.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: f.p2, Name: "secret"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if after := read(f.userName, []string{auth.RoleUser}); after["latest_run"] != before || after["runs"] != mine["runs"] {
		t.Fatalf("digest leaked other-project activity: %v -> %v", mine, after)
	}
}

func TestLogsHonorLogsReadPolicy(t *testing.T) {
	f := newIAMFixture(t)
	run, err := f.repo.Run(f.run1)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.AppendLogs([]api.LogEntry{{ProjectID: run.ProjectID, RunID: run.ID, Node: "train", Source: "runner", Severity: "info", Message: "visible line", Timestamp: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	before := f.user(http.MethodGet, "/api/v1/logs?run_id="+run.ID, "")
	if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), "visible line") {
		t.Fatalf("assigned user cannot read logs: %d %s", before.Code, before.Body)
	}
	createPolicy(t, f, `{"name":"No alpha logs","statements":[{"sid":"HideLogs","effect":"deny","actions":["logs:Read"],"resources":["kionga:project/`+run.ProjectID+`/*"]}]}`)
	expectStatus(t, f.admin(http.MethodPost, "/api/v1/admin/iam/attachments", `{"policy_id":"no-alpha-logs","principal_type":"user","principal_id":"`+f.userName+`"}`), http.StatusCreated, "attach deny")
	for _, path := range []string{"/api/v1/logs?run_id=" + run.ID, "/api/v1/logs"} {
		after := f.user(http.MethodGet, path, "")
		if after.Code != http.StatusOK || strings.Contains(after.Body.String(), "visible line") {
			t.Fatalf("%s: logs:Read deny not honored: %d %s", path, after.Code, after.Body)
		}
	}
}
