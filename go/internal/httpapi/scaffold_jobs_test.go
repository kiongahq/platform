package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// domainServer wires every registered domain route plus workspace launch,
// authenticating each request as the given principal.
func domainServer(repository store.Repository, as auth.Principal) http.Handler {
	s := &Server{store: repository}
	mux := http.NewServeMux()
	for _, register := range routeRegistrars {
		register(s, mux)
	}
	mux.HandleFunc("POST /api/v1/workspaces/{kind}/launch", s.launchWorkspace)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), as)))
	})
}

var localAdmin = auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}}

func call(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body == "" {
		request = httptest.NewRequest(method, path, nil)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func scaffoldProjectFixture(t *testing.T, repository store.Repository) api.Project {
	t.Helper()
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Churn Model", Template: "production-ml", Framework: "xgboost", Accelerator: "cpu", RequestedProfile: "team"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func TestScaffoldArgvIsExactAndCatalogDerived(t *testing.T) {
	project := scaffoldProjectFixture(t, store.New())
	argv, folder, err := scaffoldArgv(project)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"kionga", "scaffold", "churn-model", "--template", "production-ml", "--template-version", "1.0.0", "--framework", "xgboost", "--accelerator", "cpu", "--profile", "team"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv:\n got %q\nwant %q", argv, want)
	}
	if strings.Join(argv, " ") != project.ScaffoldCommand {
		t.Fatalf("argv must match the displayed command %q", project.ScaffoldCommand)
	}
	if folder.Absolute != "/workspace/projects/churn-model" || folder.Parent != "/workspace/projects" {
		t.Fatalf("folder: %+v", folder)
	}
}

func TestScaffoldRejectsInjectionAttempts(t *testing.T) {
	base := scaffoldProjectFixture(t, store.New())
	for _, namespace := range []string{"a;rm -rf /", "$(id)", "`id`", "a\nb", "../etc", "a/b", "аbc", "ａｂｃ", "a b", "-rf", "a‮b", "a\x00b", "", strings.Repeat("a", 64)} {
		project := base
		project.Namespace = namespace
		if _, _, err := scaffoldArgv(project); err == nil {
			t.Fatalf("namespace %q must be rejected", namespace)
		}
	}
	for field, value := range map[string]string{"framework": "xgboost;id", "accelerator": "cpu$(id)", "profile": "team`id`", "template": "production-ml\n--agent", "version": "1.0.0 --agent codex"} {
		project := base
		switch field {
		case "framework":
			project.Framework = value
		case "accelerator":
			project.Accelerator = value
		case "profile":
			project.RequestedProfile = value
		case "template":
			project.Template = value
		case "version":
			project.TemplateVersion = value
		}
		if argv, _, err := scaffoldArgv(project); err == nil {
			t.Fatalf("%s=%q must be rejected, got %q", field, value, argv)
		}
	}
}

func TestProjectPathIsSharedByJupyterIDEAndScaffold(t *testing.T) {
	jupyterFolders := []string{}
	jupyter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			jupyterFolders = append(jupyterFolders, strings.TrimPrefix(r.URL.Path, "/workspaces/workbench/api/contents/"))
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer jupyter.Close()
	var ideFolder string
	ide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		ideFolder = "/workspace/projects/" + body["namespace"]
		w.WriteHeader(http.StatusOK)
	}))
	defer ide.Close()
	t.Setenv("KIONGA_JUPYTER_UPSTREAM", jupyter.URL)
	t.Setenv("KIONGA_IDE_UPSTREAM", ide.URL)
	t.Setenv("KIONGA_JUPYTER_TOKEN", "notebook-secret")
	repository := store.New()
	project := scaffoldProjectFixture(t, repository)
	handler := domainServer(repository, localAdmin)
	launch := func(kind string) string {
		response := call(t, handler, http.MethodPost, "/api/v1/workspaces/"+kind+"/launch", `{"project_id":"`+project.ID+`"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("%s launch: %d %s", kind, response.Code, response.Body.String())
		}
		var body map[string]string
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		return body["url"]
	}
	jupyterURL, ideURL := launch("workbench"), launch("ide")
	_, folder, _ := scaffoldArgv(project)
	if jupyterURL != "/workspaces/workbench/lab/tree/"+folder.Relative || jupyterFolders[len(jupyterFolders)-1] != folder.Relative {
		t.Fatalf("Jupyter opened %q and created %v, expected %s", jupyterURL, jupyterFolders, folder.Relative)
	}
	parsed, _ := url.Parse(ideURL)
	if parsed.Query().Get("folder") != folder.Absolute || ideFolder != folder.Absolute {
		t.Fatalf("IDE opened %q (prepared %q), expected %s", ideURL, ideFolder, folder.Absolute)
	}
	if workspaceRoot+"/"+folder.Relative != folder.Absolute {
		t.Fatal("relative and absolute paths disagree")
	}
	plan := call(t, handler, http.MethodGet, "/api/v1/projects/"+project.ID+"/scaffold-plan", "")
	if !strings.Contains(plan.Body.String(), `"output_dir":"`+folder.Absolute+`"`) {
		t.Fatalf("scaffold output must be the folder the tools open: %s", plan.Body.String())
	}
}

// fakeWorkspaceRunner is a Jupyter upstream whose /proxy/8890 runner
// records what it was asked to run.
func fakeWorkspaceRunner(t *testing.T, status int, result string, calls *atomic.Int32, received *workspaceJobRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workspaces/workbench/proxy/8890/scaffold-jobs" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "token notebook-secret" || r.Header.Get("X-Kionga-Workspace-Token") != "job-secret" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		calls.Add(1)
		if received != nil {
			_ = json.NewDecoder(r.Body).Decode(received)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(result))
	}))
}

func waitForJob(t *testing.T, handler http.Handler, projectID, jobID string) api.ScaffoldJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var job api.ScaffoldJob
		response := call(t, handler, http.MethodGet, "/api/v1/projects/"+projectID+"/scaffold-jobs/"+jobID, "")
		_ = json.Unmarshal(response.Body.Bytes(), &job)
		if !store.ActiveScaffoldStatus(job.Status) {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return api.ScaffoldJob{}
}

func TestScaffoldJobRunsInWorkspaceAndRecordsResult(t *testing.T) {
	var calls atomic.Int32
	var received workspaceJobRequest
	runner := fakeWorkspaceRunner(t, http.StatusOK, `{"exit_code":0,"output_tail":"Created /workspace/projects/churn-model with job-secret","files":["README.md","pyproject.toml"],"git_status":["?? README.md","?? pyproject.toml"]}`, &calls, &received)
	defer runner.Close()
	t.Setenv("KIONGA_JUPYTER_UPSTREAM", runner.URL)
	t.Setenv("KIONGA_JUPYTER_TOKEN", "notebook-secret")
	t.Setenv("KIONGA_WORKSPACE_JOB_TOKEN", "job-secret")
	repository := store.New()
	project := scaffoldProjectFixture(t, repository)
	handler := domainServer(repository, localAdmin)
	created := call(t, handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/scaffold-jobs", `{"options":{"workspace":"workbench"}}`)
	if created.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var job api.ScaffoldJob
	_ = json.Unmarshal(created.Body.Bytes(), &job)
	done := waitForJob(t, handler, project.ID, job.ID)
	if done.Status != "succeeded" || *done.ExitCode != 0 || len(done.Files) != 2 || done.GitStatus[0] != "?? README.md" || done.RequestedBy != "admin" {
		t.Fatalf("job result: %+v", done)
	}
	if strings.Contains(done.OutputTail, "job-secret") || !strings.Contains(done.OutputTail, "[redacted]") {
		t.Fatalf("tokens must be redacted from output: %q", done.OutputTail)
	}
	if received.Namespace != "churn-model" || strings.Join(received.Argv, " ") != project.ScaffoldCommand || received.JobID != job.ID {
		t.Fatalf("runner received %+v", received)
	}
	list := call(t, handler, http.MethodGet, "/api/v1/projects/"+project.ID+"/scaffold-jobs", "")
	if !strings.Contains(list.Body.String(), `"total":1`) {
		t.Fatalf("list: %s", list.Body.String())
	}
}

func TestScaffoldJobFailureModesAreActionable(t *testing.T) {
	cases := []struct {
		name, result, mention string
		status                int
	}{
		{"non-zero exit", `{"exit_code":1,"output_tail":"kionga: target already exists and is not empty"}`, "exited with code 1", http.StatusOK},
		{"old image", ``, "no scaffold runner", http.StatusNotFound},
		{"runner rejected argv", `{"error":"argument --agent is not allowed"}`, "--agent is not allowed", http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls atomic.Int32
			runner := fakeWorkspaceRunner(t, c.status, c.result, &calls, nil)
			defer runner.Close()
			t.Setenv("KIONGA_JUPYTER_UPSTREAM", runner.URL)
			t.Setenv("KIONGA_JUPYTER_TOKEN", "notebook-secret")
			t.Setenv("KIONGA_WORKSPACE_JOB_TOKEN", "job-secret")
			repository := store.New()
			project := scaffoldProjectFixture(t, repository)
			handler := domainServer(repository, localAdmin)
			var job api.ScaffoldJob
			_ = json.Unmarshal(call(t, handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/scaffold-jobs", "").Body.Bytes(), &job)
			done := waitForJob(t, handler, project.ID, job.ID)
			if done.Status != "failed" || !strings.Contains(done.Error, c.mention) {
				t.Fatalf("got %+v, want failure mentioning %q", done, c.mention)
			}
		})
	}
	t.Run("wrong token", func(t *testing.T) {
		var calls atomic.Int32
		runner := fakeWorkspaceRunner(t, http.StatusOK, `{}`, &calls, nil)
		defer runner.Close()
		t.Setenv("KIONGA_JUPYTER_UPSTREAM", runner.URL)
		t.Setenv("KIONGA_JUPYTER_TOKEN", "notebook-secret")
		t.Setenv("KIONGA_WORKSPACE_JOB_TOKEN", "other")
		repository := store.New()
		project := scaffoldProjectFixture(t, repository)
		handler := domainServer(repository, localAdmin)
		var job api.ScaffoldJob
		_ = json.Unmarshal(call(t, handler, http.MethodPost, "/api/v1/projects/"+project.ID+"/scaffold-jobs", `{}`).Body.Bytes(), &job)
		if done := waitForJob(t, handler, project.ID, job.ID); !strings.Contains(done.Error, "KIONGA_WORKSPACE_JOB_TOKEN") || calls.Load() != 0 {
			t.Fatalf("token mismatch: %+v", done)
		}
	})
}

func TestScaffoldJobAccessAndConcurrency(t *testing.T) {
	block := make(chan struct{})
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		_, _ = w.Write([]byte(`{"exit_code":0}`))
	}))
	defer runner.Close()
	defer close(block)
	t.Setenv("KIONGA_JUPYTER_UPSTREAM", runner.URL)
	t.Setenv("KIONGA_JUPYTER_TOKEN", "notebook-secret")
	repository := store.New()
	project := scaffoldProjectFixture(t, repository)
	admin := domainServer(repository, localAdmin)
	if response := call(t, admin, http.MethodPost, "/api/v1/projects/"+project.ID+"/scaffold-jobs", `{}`); response.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", response.Code, response.Body.String())
	}
	if response := call(t, admin, http.MethodPost, "/api/v1/projects/"+project.ID+"/scaffold-jobs", `{}`); response.Code != http.StatusConflict {
		t.Fatalf("second concurrent job must conflict: %d %s", response.Code, response.Body.String())
	}
	if response := call(t, admin, http.MethodPost, "/api/v1/projects/"+project.ID+"/scaffold-jobs", `{"command":"rm -rf /"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("free-text fields must be rejected: %d", response.Code)
	}
	if response := call(t, admin, http.MethodPost, "/api/v1/projects/"+project.ID+"/scaffold-jobs", `{"options":{"workspace":"../x"}}`); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown workspace: %d", response.Code)
	}
	outsider := domainServer(repository, auth.Principal{Subject: "bob", Roles: []string{auth.RoleUser}, Services: []string{"projects", "workbench"}, Provisioned: true})
	for _, path := range []string{"/scaffold-jobs", "/scaffold-plan"} {
		if response := call(t, outsider, http.MethodGet, "/api/v1/projects/"+project.ID+path, ""); response.Code != http.StatusNotFound {
			t.Fatalf("unassigned project %s must be hidden: %d", path, response.Code)
		}
	}
	if response := call(t, outsider, http.MethodPost, "/api/v1/projects/"+project.ID+"/scaffold-jobs", `{}`); response.Code != http.StatusNotFound {
		t.Fatalf("unassigned project create: %d", response.Code)
	}
	tokenUser := domainServer(repository, auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}, Credential: "api_token"})
	if response := call(t, tokenUser, http.MethodGet, "/api/v1/projects/"+project.ID+"/scaffold-plan", ""); !strings.Contains(response.Body.String(), `"available":false`) {
		t.Fatalf("API tokens cannot drive workspaces: %s", response.Body.String())
	}
}
