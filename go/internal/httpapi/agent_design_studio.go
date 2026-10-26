package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/agentgraph"
	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// Published native agent versions are immutable artifacts (contracts
// agent-graph/artifact.v1.schema.json). Publishing identical behavior twice
// returns the existing version; deploying reuses the managed agent path the
// Langflow snapshots use, with the native adapter instead of LFX.
const (
	agentVersionKind      = "agent_version"
	agentFixturesKind     = "agent_design_fixtures"
	nativeGraphModule     = "agents.native_graph:build_native"
	maxFixtureCases       = 100
	maxArtifactGraphBytes = 512 << 10
)

type versionRuntime struct {
	GraphModule string `json:"graph_module"`
	Image       string `json:"image,omitempty"`
}
type versionEvaluation struct {
	ID       string  `json:"id"`
	PassRate float64 `json:"pass_rate"`
	Cases    int     `json:"cases"`
	Gate     float64 `json:"gate"`
	Passed   bool    `json:"passed"`
}
type agentVersion struct {
	ArtifactVersion int                `json:"artifact_version"`
	ID              string             `json:"id"`
	DesignID        string             `json:"design_id"`
	ProjectID       string             `json:"project_id"`
	Version         int                `json:"version"`
	ContentSHA256   string             `json:"content_sha256"`
	RegistryVersion string             `json:"registry_version"`
	Graph           agentgraph.Graph   `json:"graph"`
	SourceRevision  int                `json:"source_revision"`
	Author          string             `json:"author"`
	CreatedAt       time.Time          `json:"created_at"`
	Notes           string             `json:"notes,omitempty"`
	Runtime         versionRuntime     `json:"runtime"`
	Evaluation      *versionEvaluation `json:"evaluation,omitempty"`
	// Subflows embeds every nested version, pinned at publish time.
	Subflows map[string]resolvedFlow `json:"subflows,omitempty"`
}

type fixtureCase struct {
	ID       string            `json:"id"`
	Input    string            `json:"input"`
	Expected fixtureExpected   `json:"expected"`
	Tags     []string          `json:"tags,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
}
type fixtureExpected struct {
	Contains     []string `json:"contains,omitempty"`
	NotContains  []string `json:"not_contains,omitempty"`
	Equals       string   `json:"equals,omitempty"`
	Matches      string   `json:"matches,omitempty"`
	MaxLatencyMS int      `json:"max_latency_ms,omitempty"`
	// Calls lists tool or API node IDs the run must complete (tool accuracy).
	Calls []string          `json:"calls,omitempty"`
	Judge *judgeExpectation `json:"judge,omitempty"`
}
type designFixtures struct {
	DesignID  string        `json:"design_id"`
	ProjectID string        `json:"project_id"`
	Revision  int           `json:"revision"`
	Cases     []fixtureCase `json:"cases"`
	UpdatedAt time.Time     `json:"updated_at"`
}

func (s *Server) designerComponents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, agentgraph.Default())
}

func (s *Server) designerTemplates(w http.ResponseWriter, _ *http.Request) {
	items := agentgraph.Templates()
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

// designResourceChecker reports whether a resource is usable by the caller
// inside the design's project. Tools are a platform catalog; every other
// resource must belong to the project.
func (s *Server) designResourceChecker(r *http.Request, projectID string) agentgraph.ResourceChecker {
	return s.resourceCheckerFor(principal(r), projectID)
}

func (s *Server) resourceCheckerFor(value auth.Principal, projectID string) agentgraph.ResourceChecker {
	return func(kind, id string) bool {
		if !projectAllowed(s.store, value, projectID) {
			return false
		}
		switch kind {
		case "model":
			for _, model := range s.store.Models() {
				if model.ID == id {
					return model.ProjectID == projectID
				}
			}
		case "tool":
			for _, tool := range s.store.Tools() {
				if tool.Name == id {
					return true
				}
			}
		case "knowledge_base":
			base, err := store.GetDoc[knowledgeBase](s.store, knowledgeKind, id)
			return err == nil && base.ProjectID == projectID
		case "dataset":
			dataset, err := store.GetDoc[projectDataset](s.store, datasetKind, id)
			return err == nil && dataset.ProjectID == projectID
		case "connection":
			connection, err := store.GetDoc[agentAPIConnection](s.store, agentConnectionKind, id)
			return err == nil && connection.ProjectID == projectID
		case "agent_version":
			version, err := store.GetDoc[agentVersion](s.store, agentVersionKind, id)
			return err == nil && version.ProjectID == projectID
		}
		return false
	}
}

type validateDesignRequest struct {
	ProjectID string               `json:"project_id"`
	Nodes     []designNode         `json:"nodes"`
	Edges     []designEdge         `json:"edges"`
	Settings  *agentgraph.Settings `json:"settings,omitempty"`
}

func (s *Server) validationResult(r *http.Request, projectID string, graph agentgraph.Graph) map[string]any {
	checker := s.designResourceChecker(r, projectID)
	issues := agentgraph.Default().Validate(graph, checker)
	_, flowIssues := s.resolveFlows(projectID, graph, checker)
	issues = append(issues, flowIssues...)
	hash, _ := agentgraph.Hash(graph)
	return map[string]any{"runnable": agentgraph.Runnable(issues), "issues": issues, "content_sha256": hash, "graph": graph}
}

// validateUnsavedDesign checks a canvas that has not been saved yet.
func (s *Server) validateUnsavedDesign(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var req validateDesignRequest
	if err := decode(r, &req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	if req.ProjectID == "" || !projectAllowed(s.store, principal(r), req.ProjectID) {
		writeError(w, 403, "access_denied", "project is not assigned to this user")
		return
	}
	graph := agentgraph.Graph{Nodes: req.Nodes, Edges: req.Edges, Settings: req.Settings}
	agentgraph.Default().Normalize(&graph)
	writeJSON(w, 200, s.validationResult(r, req.ProjectID, graph))
}

// validateDesign checks the stored revision, or the canvas in the body.
func (s *Server) validateDesign(w http.ResponseWriter, r *http.Request) {
	design, ok := s.ownedDesign(r)
	if !ok {
		writeError(w, 404, "not_found", "design not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var req validateDesignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	graph, _, _ := designRevisionGraph(design, 0)
	if len(req.Nodes) > 0 {
		graph = agentgraph.Graph{Nodes: req.Nodes, Edges: req.Edges, Settings: req.Settings}
		agentgraph.Default().Normalize(&graph)
	}
	writeJSON(w, 200, s.validationResult(r, design.ProjectID, graph))
}

func (s *Server) designVersions(w http.ResponseWriter, r *http.Request) {
	design, ok := s.ownedDesign(r)
	if !ok {
		writeError(w, 404, "not_found", "design not found")
		return
	}
	items := versionsOf(s.store, design.ID)
	deployments := map[string][]api.Agent{}
	for _, agent := range s.store.Agents() {
		if agent.GraphModule == nativeGraphModule && agent.SnapshotID != "" {
			agent.FlowJSON = ""
			deployments[agent.SnapshotID] = append(deployments[agent.SnapshotID], agent)
		}
	}
	type versionView struct {
		agentVersion
		Deployments []api.Agent `json:"deployments"`
	}
	views := make([]versionView, 0, len(items))
	for _, item := range items {
		item.Graph = agentgraph.Graph{} // the list carries metadata; GET a version for its graph
		views = append(views, versionView{agentVersion: item, Deployments: append([]api.Agent{}, deployments[item.ID]...)})
	}
	writeJSON(w, 200, map[string]any{"items": views, "total": len(views)})
}

func versionsOf(docs store.Documents, designID string) []agentVersion {
	items := []agentVersion{}
	for _, item := range store.ListDocs[agentVersion](docs, agentVersionKind) {
		if item.DesignID == designID {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Version > items[j].Version })
	return items
}

func (s *Server) ownedVersion(r *http.Request) (agentVersion, bool) {
	id := r.PathValue("id")
	if !strings.HasPrefix(id, "agv-") {
		return agentVersion{}, false
	}
	item, err := store.GetDoc[agentVersion](s.store, agentVersionKind, id)
	if err != nil || !projectAllowed(s.store, principal(r), item.ProjectID) {
		return agentVersion{}, false
	}
	return item, true
}

func (s *Server) agentVersionDetail(w http.ResponseWriter, r *http.Request) {
	item, ok := s.ownedVersion(r)
	if !ok {
		writeError(w, 404, "not_found", "version not found")
		return
	}
	writeJSON(w, 200, item)
}

type publishRequest struct {
	Revision     int    `json:"revision"`
	Notes        string `json:"notes"`
	EvaluationID string `json:"evaluation_id"`
}

func (s *Server) publishDesign(w http.ResponseWriter, r *http.Request) {
	design, ok := s.ownedDesign(r)
	if !ok {
		writeError(w, 404, "not_found", "design not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req publishRequest
	if err := decode(r, &req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	if req.Revision != design.Revision {
		writeError(w, 409, "revision_conflict", fmt.Sprintf("Publish the saved revision r%d; reload to see the latest changes.", design.Revision))
		return
	}
	if len(req.Notes) > 1000 {
		writeError(w, 422, "validation_error", "Notes can have at most 1000 characters.")
		return
	}
	graph, revision, _ := designRevisionGraph(design, 0)
	graph.Name, graph.Description = design.Name, design.Description
	checker := s.designResourceChecker(r, design.ProjectID)
	issues := agentgraph.Default().Validate(graph, checker)
	flows, flowIssues := s.resolveFlows(design.ProjectID, graph, checker)
	issues = append(issues, flowIssues...)
	if !agentgraph.Runnable(issues) {
		writeJSON(w, 422, map[string]any{"error": "graph_not_runnable", "message": "Fix the validation errors before publishing.", "issues": issues})
		return
	}
	if encoded, _ := json.Marshal(graph); len(encoded) > maxArtifactGraphBytes {
		writeError(w, 422, "validation_error", "The graph exceeds the 512 KiB deployment limit.")
		return
	}
	hash, err := agentgraph.Hash(graph)
	if err != nil {
		writeError(w, 500, "publish_failed", err.Error())
		return
	}
	var evaluation *versionEvaluation
	if req.EvaluationID != "" {
		result, err := store.GetDoc[agentEvaluation](s.store, agentEvaluationKind, req.EvaluationID)
		if err != nil || result.DesignID != design.ID {
			writeError(w, 404, "not_found", "evaluation not found")
			return
		}
		if result.Status != "completed" || result.ContentSHA256 != hash {
			writeError(w, 422, "quality_gate_failed", "The evaluation must be completed and must have evaluated exactly this graph.")
			return
		}
		if !result.Passed {
			writeError(w, 422, "quality_gate_failed", fmt.Sprintf("The evaluation passed %.0f%% of cases; the gate requires %.0f%%.", result.PassRate*100, result.Gate*100))
			return
		}
		evaluation = &versionEvaluation{ID: result.ID, PassRate: result.PassRate, Cases: len(result.Cases), Gate: result.Gate, Passed: true}
	}
	for _, existing := range versionsOf(s.store, design.ID) {
		if existing.ContentSHA256 == hash {
			writeJSON(w, 200, existing)
			return
		}
	}
	id, err := newOpaqueID("agv-")
	if err != nil {
		writeError(w, 500, "publish_failed", err.Error())
		return
	}
	next := 1
	if existing := versionsOf(s.store, design.ID); len(existing) > 0 {
		next = existing[0].Version + 1
	}
	item := agentVersion{ArtifactVersion: 1, ID: id, DesignID: design.ID, ProjectID: design.ProjectID, Version: next, ContentSHA256: hash,
		RegistryVersion: agentgraph.Default().Version, Graph: graph, SourceRevision: revision, Author: principal(r).Subject,
		CreatedAt: time.Now().UTC(), Notes: strings.TrimSpace(req.Notes), Runtime: versionRuntime{GraphModule: nativeGraphModule, Image: nativeAgentImage()}, Evaluation: evaluation}
	if len(flows) > 0 {
		item.Subflows = flows
	}
	saved, err := store.UpdateDoc(s.store, agentVersionKind, id, func(_ agentVersion, exists bool) (agentVersion, error) {
		if exists {
			return agentVersion{}, store.ErrConflict
		}
		return item, nil
	}, "agent_version.published", actor(r))
	writeMutation(w, saved, err, 201)
}

func nativeAgentImage() string {
	return strings.TrimSpace(os.Getenv("KIONGA_NATIVE_AGENT_IMAGE"))
}

type deployVersionRequest struct {
	Autoscaling       api.AgentAutoscaling `json:"autoscaling"`
	Resources         api.AgentResources   `json:"resources"`
	RequireEvaluation bool                 `json:"require_evaluation"`
}

// deployVersion creates a managed agent from an immutable version. Deploying
// an earlier version is the rollback path; canary weights use the existing
// agent traffic API.
func (s *Server) deployVersion(w http.ResponseWriter, r *http.Request) {
	version, ok := s.ownedVersion(r)
	if !ok {
		writeError(w, 404, "not_found", "version not found")
		return
	}
	if !auth.Allowed(principal(r), http.MethodPost, "/api/v1/agents") {
		writeError(w, 403, "access_denied", "Agent deployment service is not assigned to this user")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req deployVersionRequest
	if err := decode(r, &req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	if req.RequireEvaluation && (version.Evaluation == nil || !version.Evaluation.Passed) {
		writeError(w, 422, "quality_gate_failed", "This version has no passing evaluation; publish it with an evaluation that meets the gate.")
		return
	}
	if os.Getenv("AGENT_RUNTIME_URL") != "" || strings.TrimSpace(os.Getenv("KIONGA_WORKSPACE_NAMESPACE")) == "" {
		writeError(w, 503, "managed_runtime_unavailable", "Deploying native agents requires a Kubernetes managed agent runtime. Run the published version from the designer instead.")
		return
	}
	image := nativeAgentImage()
	if image == "" {
		writeError(w, 503, "managed_runtime_unavailable", "KIONGA_NATIVE_AGENT_IMAGE is not configured")
		return
	}
	checker := s.designResourceChecker(r, version.ProjectID)
	issues := agentgraph.Default().Validate(version.Graph, checker)
	for id, flow := range version.Subflows {
		for _, issue := range agentgraph.Default().Validate(flow.Graph, checker) {
			if issue.Severity == agentgraph.SeverityError {
				issue.Message = id + ": " + issue.Message
				issues = append(issues, issue)
			}
		}
	}
	if !agentgraph.Runnable(issues) {
		writeJSON(w, 422, map[string]any{"error": "graph_not_runnable", "message": "A resource this version uses is no longer available to the project.", "issues": issues})
		return
	}
	artifact, err := json.Marshal(version)
	if err != nil {
		writeError(w, 500, "deploy_failed", err.Error())
		return
	}
	request := api.DeployAgentRequest{
		ProjectID: version.ProjectID, OwnerSubject: principal(r).Subject,
		Name: "native-" + strings.TrimPrefix(version.DesignID, "agd-"), Version: fmt.Sprintf("v%d", version.Version), Image: image,
		GraphModule: nativeGraphModule, FlowJSON: string(artifact), SnapshotID: version.ID, SnapshotRev: version.Version,
		Autoscaling: req.Autoscaling, Resources: req.Resources,
	}
	if err := store.NormalizeAgentRequest(&request); err != nil {
		writeError(w, 422, "validation_error", err.Error())
		return
	}
	if err := enforceAgentResources(s.store, principal(r), request); err != nil {
		writeError(w, 403, "resource_not_provisioned", err.Error())
		return
	}
	agent, err := s.store.DeployAgent(request, actor(r))
	if err != nil {
		writeMutation(w, agent, err, 202)
		return
	}
	agent.FlowJSON = ""
	writeJSON(w, 202, agent)
}

// runVersion executes an immutable published version through the runner,
// independent of the design's current draft.
func (s *Server) runVersion(w http.ResponseWriter, r *http.Request) {
	version, ok := s.ownedVersion(r)
	if !ok {
		writeError(w, 404, "not_found", "version not found")
		return
	}
	design, err := store.GetDoc[agentDesign](s.store, agentDesignKind, version.DesignID)
	if err != nil {
		writeError(w, 404, "not_found", "design not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var req runRequest
	if err := decode(r, &req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	req.StopAfter = ""
	run, status, err := s.startDesignRun(r, design, version.Graph, version.SourceRevision, req, "version", version.ID, "")
	if err != nil {
		writeRunError(w, status, err)
		return
	}
	writeJSON(w, status, run.view())
}

func (s *Server) designFixtures(w http.ResponseWriter, r *http.Request) {
	design, ok := s.ownedDesign(r)
	if !ok {
		writeError(w, 404, "not_found", "design not found")
		return
	}
	fixtures, err := store.GetDoc[designFixtures](s.store, agentFixturesKind, design.ID)
	if err != nil {
		fixtures = designFixtures{DesignID: design.ID, ProjectID: design.ProjectID, Cases: []fixtureCase{}}
	}
	writeJSON(w, 200, fixtures)
}

// saveDesignFixtures replaces the design's regression cases with optimistic
// concurrency on the fixture revision.
func (s *Server) saveDesignFixtures(w http.ResponseWriter, r *http.Request) {
	design, ok := s.ownedDesign(r)
	if !ok {
		writeError(w, 404, "not_found", "design not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Revision int           `json:"revision"`
		Cases    []fixtureCase `json:"cases"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	if issues := validateFixtureCases(req.Cases); len(issues) > 0 {
		writeJSON(w, 422, map[string]any{"error": "validation_error", "issues": issues})
		return
	}
	saved, err := store.UpdateDoc(s.store, agentFixturesKind, design.ID, func(current designFixtures, exists bool) (designFixtures, error) {
		if current.Revision != req.Revision {
			return designFixtures{}, store.ErrConflict
		}
		return designFixtures{DesignID: design.ID, ProjectID: design.ProjectID, Revision: current.Revision + 1, Cases: req.Cases, UpdatedAt: time.Now().UTC()}, nil
	}, "agent_design_fixtures.updated", actor(r))
	if errors.Is(err, store.ErrConflict) {
		writeError(w, 409, "revision_conflict", "Test cases changed elsewhere; reload before saving.")
		return
	}
	writeMutation(w, saved, err, 200)
}

func validateFixtureCases(cases []fixtureCase) []string {
	issues := []string{}
	if len(cases) > maxFixtureCases {
		issues = append(issues, fmt.Sprintf("A suite can have at most %d cases.", maxFixtureCases))
	}
	seen := map[string]bool{}
	for index, item := range cases {
		label := item.ID
		if label == "" {
			label = fmt.Sprintf("#%d", index+1)
		}
		if !designIDPattern.MatchString(item.ID) || seen[item.ID] {
			issues = append(issues, fmt.Sprintf("Case %s needs a unique lowercase ID.", label))
		}
		seen[item.ID] = true
		if strings.TrimSpace(item.Input) == "" || len([]rune(item.Input)) > maxRunInputChars {
			issues = append(issues, fmt.Sprintf("Case %s needs an input of 1–%d characters.", label, maxRunInputChars))
		}
		expected := item.Expected
		if len(expected.Contains)+len(expected.NotContains) > 20 || len(expected.Equals) > 4000 || len(expected.Matches) > 200 || expected.MaxLatencyMS < 0 || expected.MaxLatencyMS > 300000 {
			issues = append(issues, fmt.Sprintf("Case %s has oversized expectations.", label))
		}
		if expected.Matches != "" {
			if _, err := compileExpectation(expected.Matches); err != nil {
				issues = append(issues, fmt.Sprintf("Case %s has an invalid pattern: %v.", label, err))
			}
		}
		issues = append(issues, validateJudgeExpectations(label, expected)...)
		if len(item.Tags) > 10 || len(item.Meta) > 10 {
			issues = append(issues, fmt.Sprintf("Case %s has too many tags or metadata entries.", label))
		}
	}
	return issues
}

// designerOverview reports catalog KPIs from stored records only, so empty
// projects show zeros rather than illustrative numbers.
func (s *Server) designerOverview(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	if projectID != "" && !projectAllowed(s.store, principal(r), projectID) {
		writeError(w, 403, "access_denied", "project is not assigned to this user")
		return
	}
	hours := 24
	since := time.Now().Add(-time.Duration(hours) * time.Hour)
	include := func(id string) bool {
		return projectAllowed(s.store, principal(r), id) && (projectID == "" || projectID == id)
	}
	designs, versions, deployments := 0, 0, 0
	designIDs := map[string]bool{}
	for _, design := range store.ListDocs[agentDesign](s.store, agentDesignKind) {
		if include(design.ProjectID) {
			designs++
			designIDs[design.ID] = true
		}
	}
	for _, version := range store.ListDocs[agentVersion](s.store, agentVersionKind) {
		if designIDs[version.DesignID] {
			versions++
		}
	}
	for _, agent := range s.store.Agents() {
		if agent.GraphModule == nativeGraphModule && include(agent.ProjectID) {
			deployments++
		}
	}
	runs, succeeded := 0, 0
	cost := 0.0
	durations := []int64{}
	for _, run := range store.ListDocs[designRun](s.store, designRunKind) {
		if !designIDs[run.DesignID] || run.CreatedAt.Before(since) {
			continue
		}
		run = run.view()
		if !run.terminal() {
			continue
		}
		runs++
		if run.Status == "succeeded" {
			succeeded++
		}
		cost += run.Usage.CostUSD
		durations = append(durations, run.DurationMS)
	}
	var successRate, p95 any
	if runs > 0 {
		successRate = float64(succeeded) / float64(runs)
		p95 = percentile(durations, 0.95)
	}
	writeJSON(w, 200, map[string]any{"window_hours": hours, "designs": designs, "versions": versions, "deployments": deployments,
		"runs": runs, "success_rate": successRate, "p95_latency_ms": p95, "cost_usd": math.Round(cost*1e6) / 1e6})
}

// percentile uses the nearest-rank method.
func percentile(values []int64, q float64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(math.Ceil(q*float64(len(sorted)))) - 1
	return sorted[max(0, min(rank, len(sorted)-1))]
}
