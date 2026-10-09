package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// Scaffold jobs run `kionga scaffold` inside the caller's workspace.
//
// Trust boundary: the browser sends only a project id and an optional
// workspace choice. The gateway builds the argv from the stored project and
// the versioned template catalog, validates every token against a strict
// character class, and posts it as a JSON array (never a shell string) to
// the workspace's loopback runner. That runner is reachable only through the
// workspace's own authenticated proxy (Jupyter: /proxy/8890 with the
// notebook token; code-server: /proxy/8890 with the IDE session) and
// additionally requires the shared job token. It re-validates the argv
// against its own allowlist and runs it without a shell inside
// /workspace/projects. The gateway never receives the Docker socket and
// never executes user-influenced commands itself.
func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/projects/{id}/scaffold-plan", s.scaffoldPlan)
		mux.HandleFunc("GET /api/v1/projects/{id}/scaffold-jobs", s.scaffoldJobs)
		mux.HandleFunc("POST /api/v1/projects/{id}/scaffold-jobs", s.createScaffoldJob)
		mux.HandleFunc("GET /api/v1/projects/{id}/scaffold-jobs/{job}", s.scaffoldJob)
	})
}

// scaffoldTimeout bounds one execution end to end.
var scaffoldTimeout = 90 * time.Second

// scaffoldToken is the only character class allowed in argv values.
var scaffoldToken = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)

// scaffoldArgv builds the exact argv for a project. Every value comes from
// the stored project, normalized and checked against the template catalog.
func scaffoldArgv(project api.Project) ([]string, projectPath, error) {
	folder, err := resolveProjectPath(project.Namespace)
	if err != nil {
		return nil, folder, err
	}
	req := api.CreateProjectRequest{Name: project.Name, Template: project.Template, TemplateVersion: project.TemplateVersion,
		Framework: project.Framework, Accelerator: project.Accelerator, RequestedProfile: project.RequestedProfile}
	if _, err := api.NormalizeProjectRequest(&req); err != nil {
		return nil, folder, fmt.Errorf("project template settings are no longer valid: %w", err)
	}
	argv := []string{"kionga", "scaffold", project.Namespace,
		"--template", req.Template, "--template-version", req.TemplateVersion,
		"--framework", req.Framework, "--accelerator", req.Accelerator, "--profile", req.RequestedProfile}
	for _, value := range argv[2:] {
		if strings.HasPrefix(value, "--") {
			continue
		}
		if !scaffoldToken.MatchString(value) {
			return nil, folder, fmt.Errorf("project setting %q cannot be passed to the scaffolder", value)
		}
	}
	return argv, folder, nil
}

func (s *Server) scaffoldProject(w http.ResponseWriter, r *http.Request) (api.Project, bool) {
	projectID := r.PathValue("id")
	project, err := s.store.Project(projectID)
	if err != nil || !projectAllowed(s.store, principal(r), projectID) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return api.Project{}, false
	}
	return project, true
}

// scaffoldWorkspace picks the executing workspace and explains why none can.
func scaffoldWorkspace(r *http.Request, requested string) (string, *workspaceDestination, string) {
	kinds := []string{"workbench", "ide"}
	if requested != "" {
		kinds = []string{requested}
	}
	reason := "No workspace is assigned to you. Request JupyterLab or the IDE from My access."
	for _, kind := range kinds {
		if !auth.Allowed(principal(r), http.MethodPost, "/api/v1/workspaces/"+kind+"/launch") {
			continue
		}
		target, err := workspaceTarget(kind, r)
		if err != nil {
			reason = err.Error()
			continue
		}
		if workspaceJobToken(target) == "" {
			reason = "Scaffold jobs need KIONGA_WORKSPACE_JOB_TOKEN set on the gateway and the workspace."
			continue
		}
		return kind, target, ""
	}
	return "", nil, reason
}

// workspaceJobToken is the shared job token. Discovered per-user Kubernetes
// workspaces use their own generated credential (the sidecar falls back to
// JUPYTER_TOKEN/PASSWORD), so one gateway-wide token never spans tenants.
func workspaceJobToken(target *workspaceDestination) string {
	if target.name != "" {
		return target.token
	}
	if token := os.Getenv("KIONGA_WORKSPACE_JOB_TOKEN"); token != "" {
		return token
	}
	return target.token
}

func (s *Server) scaffoldPlan(w http.ResponseWriter, r *http.Request) {
	project, ok := s.scaffoldProject(w, r)
	if !ok {
		return
	}
	argv, folder, err := scaffoldArgv(project)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}
	kind, _, reason := scaffoldWorkspace(r, r.URL.Query().Get("workspace"))
	writeJSON(w, http.StatusOK, api.ScaffoldPlan{ProjectID: project.ID, Namespace: project.Namespace, Workspace: kind, Argv: argv,
		Command: strings.Join(argv, " "), WorkingDir: folder.Parent, OutputDir: folder.Absolute, Available: kind != "", Reason: reason})
}

func (s *Server) scaffoldJobs(w http.ResponseWriter, r *http.Request) {
	project, ok := s.scaffoldProject(w, r)
	if !ok {
		return
	}
	items := store.ScaffoldJobs(s.store, project.ID, time.Now().UTC())
	writeJSON(w, http.StatusOK, api.Page[api.ScaffoldJob]{Items: items, Total: len(items)})
}

func (s *Server) scaffoldJob(w http.ResponseWriter, r *http.Request) {
	project, ok := s.scaffoldProject(w, r)
	if !ok {
		return
	}
	job, err := store.ScaffoldJob(s.store, r.PathValue("job"), time.Now().UTC())
	if err != nil || job.ProjectID != project.ID {
		writeError(w, http.StatusNotFound, "not_found", "scaffold job not found")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) createScaffoldJob(w http.ResponseWriter, r *http.Request) {
	project, ok := s.scaffoldProject(w, r)
	if !ok {
		return
	}
	var req api.CreateScaffoldJobRequest
	if r.ContentLength != 0 {
		if err := decode(r, &req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	requested := ""
	if req.Options != nil {
		requested = req.Options.Workspace
	}
	if requested != "" && requested != "workbench" && requested != "ide" {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "options.workspace must be workbench or ide")
		return
	}
	argv, folder, err := scaffoldArgv(project)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}
	kind, target, reason := scaffoldWorkspace(r, requested)
	if kind == "" {
		writeError(w, http.StatusServiceUnavailable, "workspace_unavailable", reason)
		return
	}
	job, err := store.CreateScaffoldJob(s.store, api.ScaffoldJob{ProjectID: project.ID, Namespace: project.Namespace, Workspace: kind,
		Argv: argv, Command: strings.Join(argv, " "), WorkingDir: folder.Parent, OutputDir: folder.Absolute, RequestedBy: actor(r)}, actor(r))
	if errors.Is(err, store.ErrJobActive) {
		writeError(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// The run outlives this request but keeps the caller's identity for
	// workspace discovery; it is bounded by scaffoldTimeout.
	ctx := context.WithoutCancel(r.Context())
	go s.runScaffoldJob(ctx, kind, target, job)
	writeJSON(w, http.StatusAccepted, job)
}

type workspaceJobRequest struct {
	JobID     string   `json:"job_id"`
	Namespace string   `json:"namespace"`
	Argv      []string `json:"argv"`
}

type workspaceJobResult struct {
	ExitCode   int      `json:"exit_code"`
	OutputTail string   `json:"output_tail"`
	Files      []string `json:"files"`
	GitStatus  []string `json:"git_status"`
	Error      string   `json:"error"`
}

func (s *Server) runScaffoldJob(ctx context.Context, kind string, target *workspaceDestination, job api.ScaffoldJob) {
	ctx, cancel := context.WithTimeout(ctx, scaffoldTimeout)
	defer cancel()
	if _, err := store.StartScaffoldJob(s.store, job.ID); err != nil {
		return
	}
	result := executeScaffold(ctx, kind, target, job)
	_, _ = store.FinishScaffoldJob(s.store, job.ID, result, job.RequestedBy)
}

// executeScaffold posts the job to the workspace runner and maps every
// outcome to an actionable terminal state.
func executeScaffold(ctx context.Context, kind string, target *workspaceDestination, job api.ScaffoldJob) api.ScaffoldJob {
	failed := func(message string) api.ScaffoldJob { return api.ScaffoldJob{Status: "failed", Error: message} }
	body, _ := json.Marshal(workspaceJobRequest{JobID: job.ID, Namespace: job.Namespace, Argv: job.Argv})
	endpoint := strings.TrimRight(target.String(), "/")
	if kind == "workbench" {
		endpoint += "/workspaces/workbench/proxy/8890/scaffold-jobs"
	} else {
		endpoint += "/proxy/8890/scaffold-jobs"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return failed(err.Error())
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Kionga-Workspace-Token", workspaceJobToken(target))
	if kind == "workbench" {
		request.Header.Set("Authorization", "token "+target.token)
	} else {
		cookie, err := ideSession(ctx, target)
		if err != nil {
			return failed(err.Error())
		}
		if cookie != "" {
			request.Header.Set("Cookie", cookie)
		}
	}
	client := &http.Client{Timeout: scaffoldTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return failed("The workspace did not answer. Make sure it is running, then run the job again.")
	}
	defer func() { _ = response.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	var result workspaceJobResult
	_ = json.Unmarshal(raw, &result)
	redact := func(text string) string { return redactTokens(text, workspaceJobToken(target), target.token) }
	switch {
	case response.StatusCode == http.StatusOK:
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusBadGateway:
		return failed("This workspace image has no scaffold runner. Rebuild it (make -C deploy local-rebuild) so it includes the Kionga workspace sidecar.")
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || (response.StatusCode >= 300 && response.StatusCode < 400):
		return failed("The workspace rejected the scaffold credential. KIONGA_WORKSPACE_JOB_TOKEN must match on the gateway and the workspace.")
	default:
		message := result.Error
		if message == "" {
			message = fmt.Sprintf("The workspace runner returned HTTP %d.", response.StatusCode)
		}
		return failed(redact(message))
	}
	code := result.ExitCode
	out := api.ScaffoldJob{Status: "succeeded", ExitCode: &code, OutputTail: tail(redact(result.OutputTail), 8000), Files: capList(result.Files, 500), GitStatus: capList(result.GitStatus, 500)}
	if code != 0 {
		out.Status = "failed"
		out.Error = fmt.Sprintf("kionga scaffold exited with code %d. See the output below.", code)
		if result.Error != "" {
			out.Error = redact(result.Error)
		}
	}
	return out
}

func redactTokens(text string, secrets ...string) string {
	for _, secret := range secrets {
		if len(secret) >= 4 {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return text
}

func tail(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return "…" + text[len(text)-limit:]
}

func capList(values []string, limit int) []string {
	out := []string{}
	for _, value := range values {
		if len(out) == limit {
			break
		}
		out = append(out, value)
	}
	return out
}
