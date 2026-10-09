package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ml-ai-ops/platform/internal/auth"
	"github.com/ml-ai-ops/platform/internal/integrations"
	"github.com/ml-ai-ops/platform/internal/pipelinespec"
	"github.com/ml-ai-ops/platform/internal/platform"
	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

type Server struct {
	store  store.Repository
	static fs.FS

	realtimeMu sync.RWMutex
	realtime   map[string]map[string]any
}

func New(data store.Repository, static fs.FS) http.Handler {
	ensureSeedBlog(data)
	server := &Server{store: data, static: static, realtime: map[string]map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", server.health)
	mux.HandleFunc("GET /api/v1/ready", server.ready)
	mux.HandleFunc("GET /api/v1/me", server.me)
	mux.HandleFunc("GET /api/v1/settings/huggingface", server.huggingFaceAccount)
	mux.HandleFunc("PUT /api/v1/settings/huggingface", server.huggingFaceAccount)
	mux.HandleFunc("DELETE /api/v1/settings/huggingface", server.huggingFaceAccount)
	mux.HandleFunc("GET /api/v1/models/huggingface", server.huggingFaceModels)
	mux.HandleFunc("POST /api/v1/models/huggingface/import", server.importHuggingFaceModel)
	mux.HandleFunc("GET /api/v1/project-options", server.projectOptions)
	mux.HandleFunc("GET /api/v1/workspaces", server.workspaces)
	mux.HandleFunc("POST /api/v1/workspaces/{kind}/launch", server.launchWorkspace)
	mux.HandleFunc("/workspaces/{kind}/{path...}", server.workspaceProxy)
	mux.HandleFunc("GET /api/v1/admin/users", server.userAccess)
	mux.HandleFunc("GET /api/v1/admin/resource-profiles", server.resourceProfiles)
	mux.HandleFunc("PUT /api/v1/admin/users/{subject}", server.upsertUserAccess)
	mux.HandleFunc("DELETE /api/v1/admin/users/{subject}", server.deleteUserAccess)
	mux.HandleFunc("GET /api/v1/access-requests", server.myAccessRequests)
	mux.HandleFunc("POST /api/v1/access-requests", server.createAccessRequest)
	mux.HandleFunc("GET /api/v1/admin/access-requests", server.accessRequests)
	mux.HandleFunc("PATCH /api/v1/admin/access-requests/{id}", server.reviewAccessRequest)
	mux.HandleFunc("GET /api/v1/settings/tokens", server.apiTokens)
	mux.HandleFunc("POST /api/v1/settings/tokens", server.createAPIToken)
	mux.HandleFunc("DELETE /api/v1/settings/tokens/{id}", server.revokeAPIToken)
	mux.HandleFunc("GET /api/v1/blogs", server.blogPosts)
	mux.HandleFunc("GET /api/v1/blogs/{slug}", server.blogPost)
	mux.HandleFunc("GET /api/v1/admin/blogs", server.adminBlogPosts)
	mux.HandleFunc("POST /api/v1/admin/blogs", server.createBlogPost)
	mux.HandleFunc("PUT /api/v1/admin/blogs/{id}", server.updateBlogPost)
	mux.HandleFunc("DELETE /api/v1/admin/blogs/{id}", server.deleteBlogPost)
	mux.HandleFunc("GET /api/v1/dashboard", server.dashboard)
	mux.HandleFunc("GET /api/v1/onboarding/readiness", server.readiness)
	mux.HandleFunc("GET /api/v1/project-templates", server.projectTemplates)
	mux.HandleFunc("GET /api/v1/project-templates/{id}", server.projectTemplate)
	mux.HandleFunc("GET /api/v1/projects", server.projects)
	mux.HandleFunc("POST /api/v1/projects", server.createProject)
	mux.HandleFunc("GET /api/v1/projects/{id}", server.project)
	mux.HandleFunc("PUT /api/v1/projects/{id}/repository", server.setProjectRepository)
	mux.HandleFunc("GET /api/v1/pipelines/definitions", server.pipelineDefinitions)
	mux.HandleFunc("POST /api/v1/pipelines/definitions", server.upsertPipelineDefinition)
	mux.HandleFunc("GET /api/v1/pipelines/definitions/{id}", server.pipelineDefinition)
	mux.HandleFunc("PUT /api/v1/pipelines/definitions/{id}", server.upsertPipelineDefinition)
	mux.HandleFunc("GET /api/v1/pipelines/runs", server.runs)
	mux.HandleFunc("POST /api/v1/pipelines/submit", server.submitPipeline)
	mux.HandleFunc("GET /api/v1/pipelines/runs/{id}", server.run)
	mux.HandleFunc("POST /api/v1/pipelines/runs/{id}/cancel", server.cancelRun)
	mux.HandleFunc("POST /api/v1/pipelines/runs/{id}/retry", server.retryRun)
	mux.HandleFunc("POST /api/v1/pipelines/runs/{id}/steps", server.updateRunStep)
	mux.HandleFunc("GET /api/v1/components", server.components)
	mux.HandleFunc("GET /api/v1/catalog", server.catalog)
	mux.HandleFunc("GET /api/v1/features", server.features)
	mux.HandleFunc("POST /api/v1/features", server.applyFeatureView)
	mux.HandleFunc("POST /api/v1/features/{name}/materialized", server.reportMaterialization)
	mux.HandleFunc("GET /api/v1/storage/buckets", server.storageProxy("/buckets"))
	mux.HandleFunc("GET /api/v1/storage/objects", server.storageProxy("/objects"))
	mux.HandleFunc("GET /api/v1/storage/object", server.storageProxy("/object"))
	mux.HandleFunc("GET /api/v1/prompts", server.prompts)
	mux.HandleFunc("GET /api/v1/events", server.events)
	mux.HandleFunc("GET /api/v1/realtime", server.realtimeStats)
	mux.HandleFunc("POST /api/v1/realtime/{demo}", server.reportRealtime)
	mux.HandleFunc("GET /api/v1/functions", server.functions)
	mux.HandleFunc("POST /api/v1/functions", server.deployFunction)
	mux.HandleFunc("DELETE /api/v1/functions/{name}", server.deleteFunction)
	mux.HandleFunc("POST /api/v1/functions/{name}/invoke", server.invokeFunction)
	mux.HandleFunc("POST /api/v1/functions/{name}/invoke-async", server.invokeFunctionAsync)
	mux.HandleFunc("GET /api/v1/models", server.models)
	mux.HandleFunc("POST /api/v1/models", server.registerModel)
	mux.HandleFunc("POST /api/v1/models/{id}/promote", server.promoteModel)
	mux.HandleFunc("POST /api/v1/models/{id}/deploy", server.deployModel)
	mux.HandleFunc("POST /api/v1/models/{id}/rollback", server.rollbackModel)
	mux.HandleFunc("POST /api/v1/models/{id}/predict", server.predictModel)
	mux.HandleFunc("GET /api/v1/agents", server.agents)
	mux.HandleFunc("POST /api/v1/agents", server.deployAgent)
	mux.HandleFunc("PUT /api/v1/agents/{id}/traffic", server.agentTraffic)
	mux.HandleFunc("POST /api/v1/agents/{id}/invoke", server.invokeAgent)
	mux.HandleFunc("GET /api/v1/agents/{id}/sessions", server.agentSessions)
	mux.HandleFunc("GET /api/v1/agents/{id}/traces", server.agentTraces)
	mux.HandleFunc("GET /api/v1/agents/{id}/usage", server.agentUsage)
	mux.HandleFunc("POST /api/v1/traces", server.recordTrace)
	mux.HandleFunc("GET /api/v1/tools", server.tools)
	mux.HandleFunc("POST /api/v1/tools", server.registerTool)
	mux.HandleFunc("GET /api/v1/connections", server.connections)
	mux.HandleFunc("POST /api/v1/connections", server.createConnection)
	mux.HandleFunc("POST /api/v1/connections/{id}/test", server.testConnection)
	mux.HandleFunc("POST /api/v1/connections/{id}/activate", server.activateConnection)
	mux.HandleFunc("GET /api/v1/audit", server.audit)
	mux.HandleFunc("GET /api/openapi.json", server.openapi)
	for _, register := range routeRegistrars {
		register(server, mux)
	}
	mux.Handle("/", http.FileServer(http.FS(static)))
	resolver := func(subject string) (roles, services, projectIDs []string, disabled bool, ok bool) {
		access, err := data.AccessFor(subject)
		if err != nil {
			return nil, nil, nil, false, false
		}
		return []string{access.Role}, access.Services, access.ProjectIDs, access.Disabled, true
	}
	return logging(cors(auth.RBACWithResolver(mux, resolver)))
}

// me reports the caller's identity and effective permissions so the console
// renders exactly what the API will allow.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "no principal resolved")
		return
	}
	mode := "local"
	if os.Getenv("OIDC_ISSUER") != "" {
		mode = "oidc"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subject":      principal.Subject,
		"email":        principal.Email,
		"roles":        principal.Roles,
		"mode":         mode,
		"permissions":  auth.Permissions(principal),
		"services":     principal.Services,
		"project_ids":  principal.ProjectIDs,
		"provisioned":  principal.Provisioned,
		"entitlements": accessFor(s.store, principal),
	})
}

func (s *Server) userAccess(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.store.UserAccess()})
}

func (s *Server) resourceProfiles(w http.ResponseWriter, _ *http.Request) {
	profiles := store.ResourceProfiles()
	writeJSON(w, http.StatusOK, map[string]any{"items": profiles, "total": len(profiles)})
}

func (s *Server) upsertUserAccess(w http.ResponseWriter, r *http.Request) {
	var req api.UpsertUserAccessRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if r.PathValue("subject") == principal(r).Subject && (req.Role != auth.RoleAdmin || req.Disabled) {
		writeError(w, http.StatusBadRequest, "invalid_request", "administrators cannot demote or suspend themselves")
		return
	}
	access, err := s.store.UpsertUserAccess(r.PathValue("subject"), req, actor(r))
	if err != nil {
		writeMutation(w, access, err, http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, access)
}

func (s *Server) deleteUserAccess(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("subject") == principal(r).Subject {
		writeError(w, http.StatusBadRequest, "invalid_request", "administrators cannot remove their own access")
		return
	}
	if err := s.store.DeleteUserAccess(r.PathValue("subject"), actor(r)); err != nil {
		writeMutation(w, struct{}{}, err, http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) myAccessRequests(w http.ResponseWriter, r *http.Request) {
	items := s.store.AccessRequestsFor(principal(r).Subject)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) createAccessRequest(w http.ResponseWriter, r *http.Request) {
	var req api.CreateAccessRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	value := principal(r)
	request, err := s.store.CreateAccessRequest(value.Subject, value.Email, req)
	writeMutation(w, request, err, http.StatusCreated)
}

func (s *Server) accessRequests(w http.ResponseWriter, _ *http.Request) {
	items := s.store.AccessRequests()
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) reviewAccessRequest(w http.ResponseWriter, r *http.Request) {
	var req api.ReviewAccessRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request, err := s.store.ReviewAccessRequest(r.PathValue("id"), req, actor(r))
	writeMutation(w, request, err, http.StatusOK)
}

func (s *Server) apiTokens(w http.ResponseWriter, r *http.Request) {
	items := s.store.APITokensFor(principal(r).Subject)
	for i := range items {
		items[i].SecretHash = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) createAPIToken(w http.ResponseWriter, r *http.Request) {
	var req api.CreateAPITokenRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	value := principal(r)
	if !tokenScopesAllowed(s.store, value, req.Services, req.ProjectIDs) {
		writeError(w, http.StatusForbidden, "invalid_scope", "token scope exceeds your effective access")
		return
	}
	created, err := s.store.CreateAPIToken(value.Subject, req)
	created.Token.SecretHash = ""
	writeMutation(w, created, err, http.StatusCreated)
}

func (s *Server) revokeAPIToken(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RevokeAPIToken(principal(r).Subject, r.PathValue("id")); err != nil {
		writeMutation(w, struct{}{}, err, http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func tokenScopesAllowed(repository store.Repository, value auth.Principal, services, projectIDs []string) bool {
	if privileged(value) {
		return true
	}
	for _, requested := range services {
		if !slices.Contains(value.Services, requested) {
			return false
		}
	}
	for _, requested := range projectIDs {
		if !projectAllowed(repository, value, requested) {
			return false
		}
	}
	return true
}

func (s *Server) blogPosts(w http.ResponseWriter, _ *http.Request) {
	items := make([]api.BlogPost, 0)
	for _, post := range s.store.BlogPosts() {
		if post.Status == "published" {
			post.Content = ""
			items = append(items, post)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) blogPost(w http.ResponseWriter, r *http.Request) {
	post, err := s.store.BlogPost(r.PathValue("slug"))
	if err != nil || post.Status != "published" {
		writeError(w, http.StatusNotFound, "not_found", "blog post not found")
		return
	}
	writeJSON(w, http.StatusOK, post)
}

func (s *Server) adminBlogPosts(w http.ResponseWriter, _ *http.Request) {
	items := s.store.BlogPosts()
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) createBlogPost(w http.ResponseWriter, r *http.Request) {
	var req api.UpsertBlogPostRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	post, err := s.store.UpsertBlogPost("", req, actor(r))
	writeMutation(w, post, err, http.StatusCreated)
}

func (s *Server) updateBlogPost(w http.ResponseWriter, r *http.Request) {
	var req api.UpsertBlogPostRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	post, err := s.store.UpsertBlogPost(r.PathValue("id"), req, actor(r))
	writeMutation(w, post, err, http.StatusOK)
}

func (s *Server) deleteBlogPost(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteBlogPost(r.PathValue("id"), actor(r)); err != nil {
		writeMutation(w, struct{}{}, err, http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "mlaiops-gateway", "version": "0.1.0"})
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	value := principal(r)
	projects := filterProjects(s.store.Projects(), value)
	runs := filterRuns(s.store.Runs(), allowedProjectIDs(s.store, value))
	if projectID := r.URL.Query().Get("project_id"); projectID != "" {
		if !projectAllowed(s.store, value, projectID) {
			writeError(w, 403, "forbidden", "Project is not assigned to you")
			return
		}
		projects = slices.DeleteFunc(projects, func(p api.Project) bool { return p.ID != projectID })
		runs = filterRuns(runs, map[string]bool{projectID: true})
	}
	components := platform.Components(s.store.Connections())
	active, healthy := 0, 0
	for _, run := range runs {
		if run.Status == "running" || run.Status == "queued" {
			active++
		}
	}
	for _, component := range components {
		if component.Status == "healthy" {
			healthy++
		}
	}
	if len(runs) > 5 {
		runs = runs[:5]
	}
	writeJSON(w, http.StatusOK, api.Dashboard{
		Projects: len(projects), ActiveRuns: active, Healthy: healthy, Total: len(components),
		RecentRuns: runs, OnboardingPct: readinessFor(s.store).Percent,
	})
}

func (s *Server) readiness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, readinessFor(s.store))
}

func readinessFor(repository store.Repository) api.Readiness {
	connections := repository.Connections()
	healthy := map[string]bool{}
	for _, connection := range connections {
		if connection.Status == "healthy" {
			healthy[connection.Type] = true
		}
	}
	items := []api.ReadinessItem{
		{Key: "workspace", Label: "Create a workspace", Status: choose(len(repository.Projects()) > 0, "ready", "pending"), Description: "A project namespace and defaults are available.", Action: "projects"},
		{Key: "kubernetes", Label: "Connect Kubernetes", Status: choose(healthy["kubernetes"], "ready", "pending"), Description: "Required to schedule pipelines and serving workloads.", Action: "platform"},
		{Key: "tracking", Label: "Connect MLflow", Status: choose(healthy["mlflow"], "ready", "pending"), Description: "Tracks experiments, artifacts and model versions.", Action: "platform"},
		{Key: "storage", Label: "Connect object storage", Status: choose(healthy["s3"] || healthy["minio"], "ready", "pending"), Description: "Stores datasets, models and pipeline artifacts.", Action: "platform"},
		{Key: "events", Label: "Connect Kafka", Status: choose(healthy["kafka"], "ready", "pending"), Description: "Carries durable lifecycle and audit events.", Action: "platform"},
	}
	ready := 0
	for _, item := range items {
		if item.Status == "ready" {
			ready++
		}
	}
	percent := ready * 100 / len(items)
	return api.Readiness{Percent: percent, Ready: ready == len(items), Items: items}
}
func choose(condition bool, yes, no string) string {
	if condition {
		return yes
	}
	return no
}

func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, filterProjects(s.store.Projects(), principal(r)))
}

func (s *Server) project(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.Project(r.PathValue("id"))
	if err == nil && !projectAllowed(s.store, principal(r), item.ID) {
		err = store.ErrNotFound
	}
	writeMutation(w, item, err, http.StatusOK)
}

func (s *Server) setProjectRepository(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !projectAllowed(s.store, principal(r), projectID) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	var req api.SetProjectRepositoryRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := s.store.SetProjectRepository(projectID, req, actor(r))
	writeMutation(w, item, err, http.StatusOK)
}

func (s *Server) pipelineDefinitions(w http.ResponseWriter, r *http.Request) {
	items := filterPipelineDefinitions(s.store.PipelineDefinitions(), allowedProjectIDs(s.store, principal(r)))
	writeJSON(w, http.StatusOK, api.Page[api.PipelineDefinition]{Items: items, Total: len(items)})
}

func (s *Server) projectTemplates(w http.ResponseWriter, _ *http.Request) {
	items := api.ProjectTemplates()
	writeJSON(w, http.StatusOK, api.Page[api.ProjectTemplate]{Items: items, Total: len(items)})
}

func (s *Server) projectTemplate(w http.ResponseWriter, r *http.Request) {
	item, err := api.ResolveProjectTemplateVersion(r.PathValue("id"), r.URL.Query().Get("version"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "project template not found")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) pipelineDefinition(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.PipelineDefinition(r.PathValue("id"))
	if err == nil && !projectAllowed(s.store, principal(r), item.ProjectID) {
		err = store.ErrNotFound
	}
	writeMutation(w, item, err, http.StatusOK)
}

func (s *Server) upsertPipelineDefinition(w http.ResponseWriter, r *http.Request) {
	var req api.UpsertPipelineDefinitionRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	normalized, issues := pipelinespec.Validate(req)
	if len(issues) > 0 {
		writeIssues(w, issues)
		return
	}
	s.saveDefinition(w, r, normalized)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	if err := enforceProjectQuota(s.store, principal(r)); err != nil {
		writeError(w, http.StatusForbidden, "quota_exceeded", err.Error())
		return
	}
	var req api.CreateProjectRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if _, err := api.NormalizeProjectRequest(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}
	if req.RepositoryURL != "" && !auth.Allowed(principal(r), http.MethodPut, "/api/v1/projects/new/repository") {
		writeError(w, http.StatusForbidden, "access_denied", "Git service access is required to connect a repository")
		return
	}
	if err := enforceProjectTemplateAccess(s.store, principal(r), &req); err != nil {
		writeError(w, http.StatusForbidden, "resource_not_provisioned", err.Error())
		return
	}
	req.OwnerSubject = principal(r).Subject
	project, err := s.store.CreateProject(req, actor(r))
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "conflict", err.Error())
			return
		}
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, project)
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, filterRuns(s.store.Runs(), allowedProjectIDs(s.store, principal(r))))
}
func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.Run(r.PathValue("id"))
	if err == nil && !projectAllowed(s.store, principal(r), item.ProjectID) {
		err = store.ErrNotFound
	}
	writeMutation(w, item, err, http.StatusOK)
}
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	if run, err := s.store.Run(r.PathValue("id")); err != nil || !projectAllowed(s.store, principal(r), run.ProjectID) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	item, err := s.store.CancelRun(r.PathValue("id"), actor(r))
	if err == nil && item.EngineRunID != "" && s.prefectConfigured() {
		// Best effort: the control-plane cancellation is authoritative; the
		// engine cancellation stops the actual execution.
		prefect := s.prefectClient()
		if cancelErr := prefect.CancelFlowRun(r.Context(), item.EngineRunID); cancelErr != nil {
			log.Printf("prefect cancel failed for run %s (%s): %v", item.ID, item.EngineRunID, cancelErr)
		}
	}
	writeMutation(w, item, err, http.StatusOK)
}

// updateRunStep receives step transitions from the executing pipeline itself
// (the SDK step reporter inside each flow).
func (s *Server) updateRunStep(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateRunStepRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := s.store.UpdateRunStep(r.PathValue("id"), req, actor(r))
	writeMutation(w, item, err, http.StatusOK)
}
func (s *Server) retryRun(w http.ResponseWriter, r *http.Request) {
	if run, err := s.store.Run(r.PathValue("id")); err != nil || !projectAllowed(s.store, principal(r), run.ProjectID) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if err := enforceRunQuota(s.store, principal(r)); err != nil {
		writeError(w, http.StatusForbidden, "quota_exceeded", err.Error())
		return
	}
	item, err := s.store.RetryRun(r.PathValue("id"), actor(r))
	if err == nil {
		item = s.dispatchPipeline(r.Context(), item)
	}
	writeMutation(w, item, err, http.StatusAccepted)
}

func (s *Server) submitPipeline(w http.ResponseWriter, r *http.Request) {
	var req api.SubmitPipelineRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !projectAllowed(s.store, principal(r), req.ProjectID) {
		writeError(w, http.StatusForbidden, "access_denied", "project is not assigned to this user")
		return
	}
	if err := enforceRunQuota(s.store, principal(r)); err != nil {
		writeError(w, http.StatusForbidden, "quota_exceeded", err.Error())
		return
	}
	run, err := s.store.SubmitPipeline(req, actor(r))
	if err != nil {
		status := http.StatusUnprocessableEntity
		if err == store.ErrNotFound {
			status = http.StatusNotFound
		}
		writeError(w, status, "pipeline_submission_failed", err.Error())
		return
	}
	run = s.dispatchPipeline(r.Context(), run)
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) components(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, platform.Components(s.store.Connections()))
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	items, kind := platform.Catalog(scopedCatalog{s.store, allowedProjectIDs(s.store, principal(r))}), r.URL.Query().Get("kind")
	if kind == "" {
		writeJSON(w, http.StatusOK, items)
		return
	}
	filtered := make([]api.CatalogItem, 0)
	for _, item := range items {
		if item.Kind == kind {
			filtered = append(filtered, item)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

func (s *Server) features(w http.ResponseWriter, _ *http.Request) {
	items := s.store.FeatureViews()
	writeJSON(w, http.StatusOK, api.Page[api.FeatureView]{Items: items, Total: len(items)})
}

func (s *Server) applyFeatureView(w http.ResponseWriter, r *http.Request) {
	var req api.ApplyFeatureViewRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := s.store.ApplyFeatureView(req, actor(r))
	if err == nil {
		err = s.recordFeatureApply(item, actor(r))
	}
	writeMutation(w, item, err, http.StatusCreated)
}

func (s *Server) reportMaterialization(w http.ResponseWriter, r *http.Request) {
	var req api.MaterializationReport
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := s.store.ReportMaterialization(r.PathValue("name"), req.EntityCount, actor(r))
	writeMutation(w, item, err, http.StatusOK)
}

// storageProxy forwards Storage Explorer reads to the storage proxy, which is
// the sole holder of object-store credentials.
func (s *Server) storageProxy(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !storageAllowed(s.store, principal(r), r.URL.Query().Get("bucket")) {
			writeError(w, http.StatusForbidden, "access_denied", "storage bucket is not assigned to this user")
			return
		}
		base := os.Getenv("STORAGE_PROXY_URL")
		if base == "" {
			base = "http://localhost:8084"
		}
		target := strings.TrimRight(base, "/") + path
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
		if err != nil {
			writeError(w, http.StatusBadGateway, "storage_unreachable", err.Error())
			return
		}
		if token := os.Getenv("MLAIOPS_INTERNAL_TOKEN"); token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		client := &http.Client{Timeout: 20 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			writeError(w, http.StatusBadGateway, "storage_unreachable", err.Error())
			return
		}
		defer func() { _ = response.Body.Close() }()
		if path == "/buckets" && slices.Contains(principal(r).Roles, auth.RoleUser) {
			var payload struct {
				Buckets []map[string]any `json:"buckets"`
			}
			if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
				writeError(w, http.StatusBadGateway, "storage_unreachable", err.Error())
				return
			}
			access, _ := s.store.AccessFor(principal(r).Subject)
			filtered := make([]map[string]any, 0)
			for _, bucket := range payload.Buckets {
				name, _ := bucket["name"].(string)
				if slices.Contains(access.Storage.Buckets, name) {
					filtered = append(filtered, bucket)
				}
			}
			writeJSON(w, response.StatusCode, map[string]any{"buckets": filtered})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}
}

// prompts proxies Langfuse prompt management for the console's Prompt
// Library. Reports configured=false honestly instead of inventing data.
func (s *Server) prompts(w http.ResponseWriter, r *http.Request) {
	base := os.Getenv("LANGFUSE_URL")
	public := os.Getenv("LANGFUSE_PUBLIC_KEY")
	secret := os.Getenv("LANGFUSE_SECRET_KEY")
	if base == "" || public == "" || secret == "" {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "items": []any{}})
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimRight(base, "/")+"/api/public/v2/prompts", nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "langfuse_unreachable", err.Error())
		return
	}
	request.SetBasicAuth(public, secret)
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "langfuse_unreachable", err.Error())
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, "langfuse_error", response.Status)
		return
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadGateway, "langfuse_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "items": payload.Data})
}

// events streams a live state digest as Server-Sent Events so the console
// updates without polling storms. The digest carries enough for the client to
// know *what* changed; panels re-fetch their own data when it does.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer cannot stream")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	send := func() bool {
		digest := s.digest()
		raw, err := json.Marshal(digest)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}

// digest summarizes mutable state cheaply; identical digests mean no refresh.
func (s *Server) digest() map[string]any {
	runs := s.store.Runs()
	latestRun := ""
	active := 0
	for _, run := range runs {
		if run.UpdatedAt.Format(time.RFC3339Nano) > latestRun {
			latestRun = run.UpdatedAt.Format(time.RFC3339Nano)
		}
		if run.Status == "running" || run.Status == "queued" {
			active++
		}
	}
	sessions := s.store.AgentSessions("")
	latestSession := ""
	for _, session := range sessions {
		if session.UpdatedAt.Format(time.RFC3339Nano) > latestSession {
			latestSession = session.UpdatedAt.Format(time.RFC3339Nano)
		}
	}
	s.realtimeMu.RLock()
	realtimeEvents := 0.0
	for _, stats := range s.realtime {
		if events, ok := stats["events"].(float64); ok {
			realtimeEvents += events
		}
	}
	s.realtimeMu.RUnlock()
	return map[string]any{
		"runs":            len(runs),
		"active_runs":     active,
		"latest_run":      latestRun,
		"sessions":        len(sessions),
		"latest_session":  latestSession,
		"models":          len(s.store.Models()),
		"agents":          len(s.store.Agents()),
		"connections":     len(s.store.Connections()),
		"features":        len(s.store.FeatureViews()),
		"realtime_events": realtimeEvents,
	}
}

// realtimeStats exposes the live stream-processing statistics reported by the
// realtime consumer service. In-memory by design: these are live gauges, not
// durable state.
func (s *Server) realtimeStats(w http.ResponseWriter, _ *http.Request) {
	s.realtimeMu.RLock()
	defer s.realtimeMu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{"demos": s.realtime})
}

func (s *Server) reportRealtime(w http.ResponseWriter, r *http.Request) {
	demo := r.PathValue("demo")
	var payload map[string]any
	if err := decode(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	payload["updated_at"] = time.Now().UTC().Format(time.RFC3339)
	s.realtimeMu.Lock()
	s.realtime[demo] = payload
	s.realtimeMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// openfaas builds the serverless client from the environment; nil when the
// integration is not configured.
func (s *Server) openfaas() *integrations.OpenFaaS {
	base := os.Getenv("OPENFAAS_URL")
	password := os.Getenv("OPENFAAS_PASSWORD")
	if connection := s.activeConnection("openfaas"); connection != nil {
		base = connection.Endpoint
		var err error
		password, err = connectionToken(*connection)
		if err != nil {
			return nil
		}
	}
	if base == "" {
		return nil
	}
	user := os.Getenv("OPENFAAS_USER")
	if user == "" {
		user = "admin"
	}
	client := integrations.NewOpenFaaS(base, user, password)
	return &client
}

func (s *Server) functions(w http.ResponseWriter, r *http.Request) {
	persisted := filterFunctions(s.store.Functions(), allowedProjectIDs(s.store, principal(r)))
	client := s.openfaas()
	if client == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "items": persisted, "total": len(persisted)})
		return
	}
	live, err := client.ListFunctions(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "openfaas_unreachable", err.Error())
		return
	}
	byName := make(map[string]integrations.Function, len(live))
	for _, item := range live {
		byName[item.Name] = item
	}
	for i := range persisted {
		if item, ok := byName[persisted[i].Name]; ok {
			persisted[i].Replicas, persisted[i].Status = item.Replicas, "deployed"
			delete(byName, persisted[i].Name)
		} else {
			persisted[i].Status = "missing"
		}
	}
	if privileged(principal(r)) {
		for _, item := range byName {
			persisted = append(persisted, api.Function{Name: item.Name, Image: item.Image, Replicas: item.Replicas, Status: "unmanaged"})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "items": persisted, "total": len(persisted)})
}

func (s *Server) deployFunction(w http.ResponseWriter, r *http.Request) {
	client := s.openfaas()
	if client == nil {
		writeError(w, http.StatusConflict, "not_configured", "OPENFAAS_URL is not configured")
		return
	}
	var req api.DeployFunctionRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.ProjectID == "" && privileged(principal(r)) {
		req.ProjectID = "prj-demo"
	}
	if !projectAllowed(s.store, principal(r), req.ProjectID) {
		writeError(w, http.StatusForbidden, "access_denied", "project is not assigned to this user")
		return
	}
	if err := store.ValidateFunctionRequest(req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := enforceFunctionResources(s.store, principal(r), req); err != nil {
		writeError(w, http.StatusForbidden, "resource_not_provisioned", err.Error())
		return
	}
	if existing := functionAllowed(s.store, principal(r), req.Name); !existing {
		if err := enforceFunctionQuota(s.store, principal(r)); err != nil {
			writeError(w, http.StatusForbidden, "quota_exceeded", err.Error())
			return
		}
	}
	limits, requests := map[string]string{}, map[string]string{}
	if req.CPU != "" {
		limits["cpu"], requests["cpu"] = req.CPU, req.CPU
	}
	if req.Memory != "" {
		limits["memory"], requests["memory"] = req.Memory, req.Memory
	}
	deployment := integrations.Function{Name: req.Name, Image: req.Image, EnvVars: req.EnvVars, Labels: req.Labels, Annotations: req.Annotations, Limits: limits, Requests: requests}
	if err := client.DeployFunction(r.Context(), deployment); err != nil {
		writeError(w, http.StatusBadGateway, "deploy_failed", err.Error())
		return
	}
	item, err := s.store.UpsertFunction(req, principal(r).Subject, actor(r))
	writeMutation(w, item, err, http.StatusAccepted)
}

func (s *Server) deleteFunction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !functionAllowed(s.store, principal(r), name) {
		writeError(w, http.StatusNotFound, "not_found", "function not found")
		return
	}
	client := s.openfaas()
	if client == nil {
		writeError(w, http.StatusConflict, "not_configured", "OPENFAAS_URL is not configured")
		return
	}
	if err := client.DeleteFunction(r.Context(), name); err != nil {
		writeError(w, http.StatusBadGateway, "delete_failed", err.Error())
		return
	}
	if err := s.store.DeleteFunction(name, actor(r)); err != nil {
		writeMutation(w, struct{}{}, err, http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) invokeFunction(w http.ResponseWriter, r *http.Request) {
	client := s.openfaas()
	if client == nil {
		writeError(w, http.StatusConflict, "not_configured", "OPENFAAS_URL is not configured")
		return
	}
	if !functionAllowed(s.store, principal(r), r.PathValue("name")) {
		writeError(w, http.StatusNotFound, "not_found", "function not found")
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := client.Invoke(r.Context(), r.PathValue("name"), payload)
	if err != nil {
		writeError(w, http.StatusBadGateway, "invoke_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

func (s *Server) invokeFunctionAsync(w http.ResponseWriter, r *http.Request) {
	client := s.openfaas()
	if client == nil {
		writeError(w, http.StatusConflict, "not_configured", "OPENFAAS_URL is not configured")
		return
	}
	if !functionAllowed(s.store, principal(r), r.PathValue("name")) {
		writeError(w, http.StatusNotFound, "not_found", "function not found")
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	callID, err := client.InvokeAsync(r.Context(), r.PathValue("name"), payload)
	if err != nil {
		writeError(w, http.StatusBadGateway, "invoke_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "call_id": callID})
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	items := filterModels(s.store.Models(), allowedProjectIDs(s.store, principal(r)))
	writeJSON(w, http.StatusOK, api.Page[api.Model]{Items: items, Total: len(items)})
}

func (s *Server) registerModel(w http.ResponseWriter, r *http.Request) {
	var req api.RegisterModelRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !projectAllowed(s.store, principal(r), req.ProjectID) {
		writeError(w, http.StatusForbidden, "access_denied", "project is not assigned to this user")
		return
	}
	item, err := s.store.RegisterModel(req, actor(r))
	writeMutation(w, item, err, http.StatusCreated)
}

func (s *Server) promoteModel(w http.ResponseWriter, r *http.Request) {
	if !modelAllowed(s.store, principal(r), r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	}
	var req api.PromoteModelRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := s.store.PromoteModel(r.PathValue("id"), req.Stage, actor(r))
	writeMutation(w, item, err, http.StatusOK)
}
func (s *Server) deployModel(w http.ResponseWriter, r *http.Request) {
	if !modelAllowed(s.store, principal(r), r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	}
	var req api.DeployModelRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := s.store.DeployModel(r.PathValue("id"), req.CanaryWeight, actor(r))
	if err != nil {
		writeMutation(w, item, err, http.StatusAccepted)
		return
	}
	// With a serving manager configured, deployment is real: an mlflow serve
	// container starts and the live endpoint is recorded. Fail closed when the
	// serving manager rejects it.
	if managerURL := os.Getenv("SERVING_MANAGER_URL"); managerURL != "" {
		endpoint, deployErr := s.requestServing(r, managerURL, item)
		if deployErr != nil {
			item, _ = s.store.SetModelEndpoint(item.ID, "", "failed")
			writeError(w, http.StatusBadGateway, "serving_failed", deployErr.Error())
			return
		}
		if updated, setErr := s.store.SetModelEndpoint(item.ID, endpoint, "serving"); setErr == nil {
			item = updated
		}
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (s *Server) requestServing(r *http.Request, managerURL string, model api.Model) (string, error) {
	body, _ := json.Marshal(struct {
		Name         string `json:"name"`
		ArtifactURI  string `json:"artifact_uri"`
		ServingImage string `json:"serving_image,omitempty"`
	}{Name: model.Name, ArtifactURI: model.ArtifactURI, ServingImage: model.ServingImage})
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(managerURL, "/")+"/deployments", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	if token := os.Getenv("MLAIOPS_INTERNAL_TOKEN"); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 90 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return "", errors.New("serving manager: " + strings.TrimSpace(string(raw)))
	}
	var payload struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", err
	}
	return payload.Endpoint, nil
}

func (s *Server) rollbackModel(w http.ResponseWriter, r *http.Request) {
	if !modelAllowed(s.store, principal(r), r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	}
	item, err := s.store.RollbackModel(r.PathValue("id"), actor(r))
	if err == nil && os.Getenv("SERVING_MANAGER_URL") != "" {
		// Best effort: remove the serving container for the rolled-back model.
		managerURL := strings.TrimRight(os.Getenv("SERVING_MANAGER_URL"), "/")
		request, buildErr := http.NewRequestWithContext(r.Context(), http.MethodDelete, managerURL+"/deployments/"+url.PathEscape(item.Name), nil)
		if buildErr == nil {
			if token := os.Getenv("MLAIOPS_INTERNAL_TOKEN"); token != "" {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			client := &http.Client{Timeout: 30 * time.Second}
			if response, doErr := client.Do(request); doErr == nil {
				_ = response.Body.Close()
			} else {
				log.Printf("serving rollback undeploy failed for %s: %v", item.Name, doErr)
			}
		}
	}
	writeMutation(w, item, err, http.StatusOK)
}

// predictModel proxies a console test request to the model's live serving
// endpoint (mlflow serve /invocations contract).
func (s *Server) predictModel(w http.ResponseWriter, r *http.Request) {
	if !modelAllowed(s.store, principal(r), r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	}
	var model *api.Model
	for _, item := range s.store.Models() {
		if item.ID == r.PathValue("id") {
			value := item
			model = &value
			break
		}
	}
	if model == nil {
		writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	}
	if model.EndpointURL == "" || !strings.HasPrefix(model.EndpointURL, "http") {
		writeError(w, http.StatusConflict, "not_serving", "model has no live serving endpoint")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(model.EndpointURL, "/")+"/invocations", bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadGateway, "endpoint_unreachable", err.Error())
		return
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "endpoint_unreachable", err.Error())
		return
	}
	defer func() { _ = response.Body.Close() }()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (s *Server) agents(w http.ResponseWriter, r *http.Request) {
	items := filterAgents(s.store.Agents(), allowedProjectIDs(s.store, principal(r)))
	enrichAgentRuntimeReadiness(r.Context(), items)
	writeJSON(w, http.StatusOK, api.Page[api.Agent]{Items: items, Total: len(items)})
}

func (s *Server) deployAgent(w http.ResponseWriter, r *http.Request) {
	var req api.DeployAgentRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !projectAllowed(s.store, principal(r), req.ProjectID) {
		writeError(w, http.StatusForbidden, "access_denied", "project is not assigned to this user")
		return
	}
	if err := store.NormalizeAgentRequest(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}
	availableTools := make(map[string]bool, len(s.store.Tools()))
	for _, tool := range s.store.Tools() {
		availableTools[tool.Name] = true
	}
	for _, tool := range req.Tools {
		if !availableTools[tool] {
			writeError(w, http.StatusUnprocessableEntity, "validation_error", "agent references an unregistered tool: "+tool)
			return
		}
	}
	req.OwnerSubject = principal(r).Subject
	if err := enforceAgentResources(s.store, principal(r), req); err != nil {
		writeError(w, http.StatusForbidden, "resource_not_provisioned", err.Error())
		return
	}
	item, err := s.store.DeployAgent(req, actor(r))
	writeMutation(w, item, err, http.StatusAccepted)
}

func (s *Server) agentTraffic(w http.ResponseWriter, r *http.Request) {
	if !agentAllowed(s.store, principal(r), r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not_found", "agent not found")
		return
	}
	var req api.TrafficRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := s.store.SetAgentTraffic(r.PathValue("id"), req.CanaryWeight, actor(r))
	writeMutation(w, item, err, http.StatusOK)
}

// agentRuntimeEndpoint keeps Compose's shared runtime override while routing
// Kubernetes traffic to the immutable ID-derived Service reconciled for the
// selected agent.
func agentRuntimeEndpoint(agentID string) string {
	if override := strings.TrimSpace(os.Getenv("AGENT_RUNTIME_URL")); override != "" {
		return strings.TrimRight(override, "/")
	}
	namespace := strings.TrimSpace(os.Getenv("MLAIOPS_AGENT_NAMESPACE"))
	if namespace == "" {
		namespace = "default"
	}
	return fmt.Sprintf("http://%s.%s.svc", api.AgentDNSName(agentID), namespace)
}

func setAgentRuntimeHeaders(request *http.Request, agent *api.Agent) {
	request.Header.Set("X-MLAIOps-Graph-Module", agent.GraphModule)
	request.Header.Set("X-MLAIOps-Agent-ID", agent.ID)
	request.Header.Set("X-MLAIOps-Agent-Name", agent.Name)
}

func agentRuntimeReady(ctx context.Context, runtime string, agent *api.Agent, timeout time.Duration) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, runtime+"/healthz", nil)
	if err != nil {
		return err
	}
	setAgentRuntimeHeaders(request, agent)
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("health probe returned %d", response.StatusCode)
	}
	return nil
}

// enrichAgentRuntimeReadiness turns persisted deployment intent into a live
// list view. A small worker pool prevents large tenants from spawning an
// unbounded number of sockets, while a request-wide deadline caps list
// latency even when several runtimes are unavailable.
func enrichAgentRuntimeReadiness(parent context.Context, agents []api.Agent) {
	if len(agents) == 0 {
		return
	}
	for index := range agents {
		agents[index].EndpointURL = agentRuntimeEndpoint(agents[index].ID)
		agents[index].Status = "pending"
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	workerCount := min(len(agents), 8)
	jobs := make(chan int)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				if agentRuntimeReady(ctx, agents[index].EndpointURL, &agents[index], 1500*time.Millisecond) == nil {
					agents[index].Status = "ready"
				}
			}
		}()
	}
	for index := range agents {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
}

// invokeAgent proxies a test-console or SDK turn to the selected runtime,
// after a bounded health probe prevents traffic from reaching unavailable pods.
func (s *Server) invokeAgent(w http.ResponseWriter, r *http.Request) {
	if !agentAllowed(s.store, principal(r), r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not_found", "agent not found")
		return
	}
	agentID := r.PathValue("id")
	var agent *api.Agent
	for _, item := range s.store.Agents() {
		if item.ID == agentID {
			value := item
			agent = &value
			break
		}
	}
	if agent == nil {
		writeError(w, http.StatusNotFound, "not_found", "agent not found")
		return
	}
	var req api.InvokeAgentRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "message is required")
		return
	}
	runtime := agentRuntimeEndpoint(agent.ID)
	if err := agentRuntimeReady(r.Context(), runtime, agent, 3*time.Second); err != nil {
		writeError(w, http.StatusServiceUnavailable, "agent_not_ready", "agent runtime is not ready")
		return
	}
	body, _ := json.Marshal(req)
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, runtime+"/invoke", bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadGateway, "runtime_unreachable", err.Error())
		return
	}
	request.Header.Set("Content-Type", "application/json")
	setAgentRuntimeHeaders(request, agent)
	client := &http.Client{Timeout: 120 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "runtime_unreachable", err.Error())
		return
	}
	defer func() { _ = response.Body.Close() }()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (s *Server) agentSessions(w http.ResponseWriter, r *http.Request) {
	if !agentAllowed(s.store, principal(r), r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not_found", "agent not found")
		return
	}
	items := s.store.AgentSessions(r.PathValue("id"))
	writeJSON(w, http.StatusOK, api.Page[api.AgentSession]{Items: items, Total: len(items)})
}
func (s *Server) agentTraces(w http.ResponseWriter, r *http.Request) {
	if !agentAllowed(s.store, principal(r), r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not_found", "agent not found")
		return
	}
	items := s.store.AgentTraces(r.PathValue("id"))
	writeJSON(w, http.StatusOK, api.Page[api.AgentTrace]{Items: items, Total: len(items)})
}
func (s *Server) agentUsage(w http.ResponseWriter, r *http.Request) {
	if !agentAllowed(s.store, principal(r), r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not_found", "agent not found")
		return
	}
	sessions := s.store.AgentSessions(r.PathValue("id"))
	usage := api.AgentUsage{Sessions: len(sessions)}
	for _, session := range sessions {
		if session.Status == "running" {
			usage.Active++
		}
		usage.InputTokens += session.InputTokens
		usage.OutputTokens += session.OutputTokens
		usage.CostUSD += session.CostUSD
	}
	writeJSON(w, http.StatusOK, usage)
}
func (s *Server) recordTrace(w http.ResponseWriter, r *http.Request) {
	var req api.RecordTraceRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := s.store.RecordTrace(req)
	writeMutation(w, item, err, http.StatusCreated)
}

func (s *Server) tools(w http.ResponseWriter, _ *http.Request) {
	items := s.store.Tools()
	writeJSON(w, http.StatusOK, api.Page[api.Tool]{Items: items, Total: len(items)})
}

func (s *Server) registerTool(w http.ResponseWriter, r *http.Request) {
	var req api.RegisterToolRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := s.store.RegisterTool(req, actor(r))
	writeMutation(w, item, err, http.StatusCreated)
}

func (s *Server) connections(w http.ResponseWriter, _ *http.Request) {
	items := s.store.Connections()
	writeJSON(w, http.StatusOK, api.Page[api.Connection]{Items: items, Total: len(items)})
}

func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) {
	var req api.CreateConnectionRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := s.store.CreateConnection(req, actor(r))
	writeMutation(w, item, err, http.StatusCreated)
}
func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	var connection *api.Connection
	for _, item := range s.store.Connections() {
		if item.ID == r.PathValue("id") {
			value := item
			connection = &value
			break
		}
	}
	if connection == nil {
		writeError(w, http.StatusNotFound, "not_found", "connection not found")
		return
	}
	status, message := "healthy", "Connection succeeded"
	if checkErr := checkConnection(r, *connection); checkErr != nil {
		status, message = "unhealthy", checkErr.Error()
	}
	item, updateErr := s.store.UpdateConnectionStatus(connection.ID, status, message, actor(r))
	writeMutation(w, item, updateErr, http.StatusOK)
}

func (s *Server) audit(w http.ResponseWriter, _ *http.Request) {
	items := s.store.Audit()
	writeJSON(w, http.StatusOK, api.Page[api.AuditEvent]{Items: items, Total: len(items)})
}

func writeMutation[T any](w http.ResponseWriter, value T, err error, success int) {
	if err == nil {
		writeJSON(w, success, value)
		return
	}
	status, code := http.StatusUnprocessableEntity, "validation_error"
	if errors.Is(err, store.ErrNotFound) {
		status, code = http.StatusNotFound, "not_found"
	}
	if errors.Is(err, store.ErrConflict) {
		status, code = http.StatusConflict, "conflict"
	}
	writeError(w, status, code, err.Error())
}

func actor(r *http.Request) string {
	if principal, ok := auth.PrincipalFrom(r.Context()); ok {
		if principal.Email != "" {
			return principal.Email
		}
		return principal.Subject
	}
	if value := strings.TrimSpace(r.Header.Get("X-MLAIOps-Actor")); value != "" {
		return value
	}
	return "anonymous"
}

func decode(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, api.APIError{Error: code, Message: message})
}

// cors reflects the configured origin. MLAIOPS_ALLOWED_ORIGIN pins the
// console origin in public deployments; unset means local development where
// any origin is fine.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := os.Getenv("MLAIOPS_ALLOWED_ORIGIN")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if origin != "*" {
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		category := requestCategory(r.URL.Path)
		requestCount.WithLabelValues(r.Method, category).Inc()
		requestDuration.WithLabelValues(category).Observe(time.Since(start).Seconds())
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("method=%s path=%s duration=%s", r.Method, r.URL.Path, time.Since(start))
		}
	})
}
