package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/agentgraph"
	"github.com/kiongahq/platform/internal/store"
)

// Evaluations run a reproducible case suite (stored with the result) against
// one design revision or published version and score each reply with
// deterministic assertions. Judgment-based scoring belongs to the shared
// evaluation service once it exists; nothing here calls a model to grade.
const (
	agentEvaluationKind = "agent_evaluation"
	defaultQualityGate  = 0.8
)

type evaluationCase struct {
	fixtureCase
	RunID      string         `json:"run_id,omitempty"`
	Status     string         `json:"status"`
	Output     string         `json:"output,omitempty"`
	Passed     bool           `json:"passed"`
	Failures   []string       `json:"failures,omitempty"`
	DurationMS int64          `json:"duration_ms"`
	Usage      designRunUsage `json:"usage"`
}
type agentEvaluation struct {
	ID               string           `json:"id"`
	DesignID         string           `json:"design_id"`
	VersionID        string           `json:"version_id,omitempty"`
	ProjectID        string           `json:"project_id"`
	Subject          string           `json:"subject"`
	Revision         int              `json:"revision"`
	ContentSHA256    string           `json:"content_sha256"`
	Status           string           `json:"status"`
	Gate             float64          `json:"gate"`
	AllowSideEffects bool             `json:"allow_side_effects"`
	Cases            []evaluationCase `json:"cases"`
	CaseCount        int              `json:"case_count"`
	Completed        int              `json:"completed"`
	PassRate         float64          `json:"pass_rate"`
	Passed           bool             `json:"passed"`
	P95LatencyMS     int64            `json:"p95_latency_ms"`
	CostUSD          float64          `json:"cost_usd"`
	InputTokens      int              `json:"input_tokens"`
	OutputTokens     int              `json:"output_tokens"`
	FailureReasons   map[string]int   `json:"failure_reasons"`
	Error            string           `json:"error,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	FinishedAt       *time.Time       `json:"finished_at,omitempty"`
}

type evaluationRequest struct {
	Revision         int           `json:"revision"`
	VersionID        string        `json:"version_id"`
	Cases            []fixtureCase `json:"cases"`
	Gate             *float64      `json:"gate"`
	AllowSideEffects bool          `json:"allow_side_effects"`
}

func compileExpectation(pattern string) (*regexp.Regexp, error) {
	if len(pattern) > 200 {
		return nil, fmt.Errorf("pattern exceeds 200 characters")
	}
	return regexp.Compile(pattern)
}

// scoreCase applies a case's assertions to a finished run. Each failure is
// "<reason>: <detail>" so the dashboard can group by reason.
func scoreCase(item fixtureCase, run designRun) []string {
	failures := []string{}
	if run.Status != "succeeded" {
		message := run.Error
		if message == "" {
			message = run.Status
		}
		return append(failures, "run_failed: "+message)
	}
	output := strings.ToLower(run.Output)
	for _, text := range item.Expected.Contains {
		if !strings.Contains(output, strings.ToLower(text)) {
			failures = append(failures, "missing_text: "+text)
		}
	}
	for _, text := range item.Expected.NotContains {
		if text != "" && strings.Contains(output, strings.ToLower(text)) {
			failures = append(failures, "unexpected_text: "+text)
		}
	}
	if item.Expected.Equals != "" && !strings.EqualFold(strings.TrimSpace(run.Output), strings.TrimSpace(item.Expected.Equals)) {
		failures = append(failures, "not_equal: expected exact reply")
	}
	if item.Expected.Matches != "" {
		if pattern, err := compileExpectation(item.Expected.Matches); err != nil || !pattern.MatchString(run.Output) {
			failures = append(failures, "no_match: "+item.Expected.Matches)
		}
	}
	if item.Expected.MaxLatencyMS > 0 && run.DurationMS > int64(item.Expected.MaxLatencyMS) {
		failures = append(failures, fmt.Sprintf("too_slow: %d ms > %d ms", run.DurationMS, item.Expected.MaxLatencyMS))
	}
	return failures
}

// summarize recomputes aggregate metrics from the case results.
func (e *agentEvaluation) summarize() {
	e.CaseCount, e.Completed, e.CostUSD, e.InputTokens, e.OutputTokens = len(e.Cases), 0, 0, 0, 0
	e.FailureReasons = map[string]int{}
	passed := 0
	durations := []int64{}
	for _, item := range e.Cases {
		if item.Status == "pending" {
			continue
		}
		e.Completed++
		if item.Passed {
			passed++
		}
		durations = append(durations, item.DurationMS)
		e.CostUSD += item.Usage.CostUSD
		e.InputTokens += item.Usage.InputTokens
		e.OutputTokens += item.Usage.OutputTokens
		for _, failure := range item.Failures {
			reason, _, _ := strings.Cut(failure, ":")
			e.FailureReasons[reason]++
		}
	}
	e.CostUSD = math.Round(e.CostUSD*1e6) / 1e6
	e.PassRate = 0
	if len(e.Cases) > 0 {
		e.PassRate = float64(passed) / float64(len(e.Cases))
	}
	e.Passed = e.Status == "completed" && e.PassRate >= e.Gate
	e.P95LatencyMS = percentile(durations, 0.95)
}

func (s *Server) createEvaluation(w http.ResponseWriter, r *http.Request) {
	design, ok := s.ownedDesign(r)
	if !ok {
		writeError(w, 404, "not_found", "design not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req evaluationRequest
	if err := decode(r, &req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	gate := defaultQualityGate
	if req.Gate != nil {
		gate = *req.Gate
	}
	if gate < 0 || gate > 1 {
		writeError(w, 422, "validation_error", "gate must be between 0 and 1")
		return
	}
	cases := req.Cases
	if len(cases) == 0 {
		if fixtures, err := store.GetDoc[designFixtures](s.store, agentFixturesKind, design.ID); err == nil {
			cases = fixtures.Cases
		}
	}
	if len(cases) == 0 {
		writeError(w, 422, "validation_error", "Add test cases to the design or send cases with the evaluation.")
		return
	}
	if issues := validateFixtureCases(cases); len(issues) > 0 {
		writeJSON(w, 422, map[string]any{"error": "validation_error", "issues": issues})
		return
	}
	graph, revision := agentgraph.Graph{}, 0
	if req.VersionID != "" {
		version, err := store.GetDoc[agentVersion](s.store, agentVersionKind, req.VersionID)
		if err != nil || version.DesignID != design.ID {
			writeError(w, 404, "not_found", "version not found")
			return
		}
		graph, revision = version.Graph, version.SourceRevision
	} else {
		var found bool
		graph, revision, found = designRevisionGraph(design, req.Revision)
		if !found {
			writeError(w, 404, "not_found", "revision not found")
			return
		}
	}
	if runnerURL() == "" {
		writeError(w, 503, "runner_unavailable", "The agent designer runner is not configured (KIONGA_AGENT_DESIGNER_RUNNER_URL).")
		return
	}
	issues := agentgraph.Default().Validate(graph, s.designResourceChecker(r, design.ProjectID))
	if !agentgraph.Runnable(issues) {
		writeJSON(w, 422, map[string]any{"error": "graph_not_runnable", "message": "Fix the validation errors before evaluating.", "issues": issues})
		return
	}
	hash, _ := agentgraph.Hash(graph)
	id, err := newOpaqueID("age-")
	if err != nil {
		writeError(w, 500, "evaluation_failed", err.Error())
		return
	}
	results := make([]evaluationCase, len(cases))
	for index, item := range cases {
		results[index] = evaluationCase{fixtureCase: item, Status: "pending"}
	}
	evaluation := agentEvaluation{ID: id, DesignID: design.ID, VersionID: req.VersionID, ProjectID: design.ProjectID, Subject: principal(r).Subject,
		Revision: revision, ContentSHA256: hash, Status: "running", Gate: gate, AllowSideEffects: req.AllowSideEffects, Cases: results, CaseCount: len(results),
		FailureReasons: map[string]int{}, CreatedAt: time.Now().UTC()}
	saved, err := store.UpdateDoc(s.store, agentEvaluationKind, id, func(_ agentEvaluation, exists bool) (agentEvaluation, error) {
		if exists {
			return agentEvaluation{}, store.ErrConflict
		}
		return evaluation, nil
	}, "agent_evaluation.started", actor(r))
	if err != nil {
		writeError(w, 500, "evaluation_failed", err.Error())
		return
	}
	s.pruneEvaluations(design.ID, actor(r))
	caller := callerOf(r)
	caller.idempotencyKey = ""
	go s.executeEvaluation(caller, design, graph, revision, saved)
	writeJSON(w, 202, saved)
}

// executeEvaluation runs cases one at a time so a suite cannot monopolize
// the runner, persisting each scored case as it finishes.
func (s *Server) executeEvaluation(caller runCaller, design agentDesign, graph agentgraph.Graph, revision int, evaluation agentEvaluation) {
	mode := "evaluation"
	for index, item := range evaluation.Cases {
		current, err := store.GetDoc[agentEvaluation](s.store, agentEvaluationKind, evaluation.ID)
		if err == nil && current.Status == "cancelling" {
			break
		}
		run, err := s.runSync(context.Background(), caller, design, graph, revision, runRequest{Input: item.Input, AllowSideEffects: evaluation.AllowSideEffects}, mode, evaluation.VersionID, evaluation.ID)
		result := item
		if err != nil {
			var startErr *runStartError
			message := err.Error()
			if errors.As(err, &startErr) {
				message = startErr.message
			}
			result.Status, result.Failures = "failed", []string{"run_failed: " + message}
		} else {
			result.RunID, result.Status, result.Output, result.DurationMS, result.Usage = run.ID, run.Status, truncateRunes(run.Output, 4000), run.DurationMS, run.Usage
			result.Failures = scoreCase(item.fixtureCase, run)
			result.Passed = len(result.Failures) == 0
		}
		_, err = store.UpdateDoc(s.store, agentEvaluationKind, evaluation.ID, func(current agentEvaluation, exists bool) (agentEvaluation, error) {
			if !exists {
				return agentEvaluation{}, store.ErrNotFound
			}
			current.Cases[index] = result
			current.summarize()
			return current, nil
		}, "", "system:agent-designer")
		if err != nil {
			log.Printf("agent evaluation %s: %v", evaluation.ID, err)
			return
		}
	}
	_, err := store.UpdateDoc(s.store, agentEvaluationKind, evaluation.ID, func(current agentEvaluation, exists bool) (agentEvaluation, error) {
		if !exists {
			return agentEvaluation{}, store.ErrNotFound
		}
		now := time.Now().UTC()
		current.FinishedAt = &now
		current.Status = "completed"
		for _, item := range current.Cases {
			if item.Status == "pending" {
				current.Status = "cancelled"
			}
		}
		current.summarize()
		return current, nil
	}, "agent_evaluation.finished", "system:agent-designer")
	if err != nil {
		log.Printf("agent evaluation %s: %v", evaluation.ID, err)
	}
}

// pruneEvaluations keeps the newest finished evaluations of a design, and
// deletes the runs of the ones it removes (bounded retention). Evaluations a
// published version cites are kept.
func (s *Server) pruneEvaluations(designID, actorName string) {
	const keep = 20
	cited := map[string]bool{}
	for _, version := range versionsOf(s.store, designID) {
		if version.Evaluation != nil {
			cited[version.Evaluation.ID] = true
		}
	}
	items := []agentEvaluation{}
	for _, item := range store.ListDocs[agentEvaluation](s.store, agentEvaluationKind) {
		if item.DesignID == designID {
			items = append(items, item)
		}
	}
	if len(items) <= keep {
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	removed := map[string]bool{}
	for _, item := range items[keep:] {
		if item.Status == "running" || item.Status == "cancelling" || cited[item.ID] {
			continue
		}
		if s.store.DeleteDocument(agentEvaluationKind, item.ID, "agent_evaluation.pruned", actorName) == nil {
			removed[item.ID] = true
		}
	}
	for _, run := range store.ListDocs[designRun](s.store, designRunKind) {
		if removed[run.EvaluationID] {
			_ = s.store.DeleteDocument(designRunKind, run.ID, "agent_design_run.pruned", actorName)
		}
	}
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func (s *Server) designEvaluations(w http.ResponseWriter, r *http.Request) {
	design, ok := s.ownedDesign(r)
	if !ok {
		writeError(w, 404, "not_found", "design not found")
		return
	}
	items := []agentEvaluation{}
	for _, item := range store.ListDocs[agentEvaluation](s.store, agentEvaluationKind) {
		if item.DesignID == design.ID {
			item.Cases = nil
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) ownedEvaluation(r *http.Request) (agentEvaluation, bool) {
	id := r.PathValue("id")
	if !strings.HasPrefix(id, "age-") {
		return agentEvaluation{}, false
	}
	item, err := store.GetDoc[agentEvaluation](s.store, agentEvaluationKind, id)
	if err != nil || !projectAllowed(s.store, principal(r), item.ProjectID) {
		return agentEvaluation{}, false
	}
	return item, true
}

func (s *Server) evaluationDetail(w http.ResponseWriter, r *http.Request) {
	item, ok := s.ownedEvaluation(r)
	if !ok {
		writeError(w, 404, "not_found", "evaluation not found")
		return
	}
	writeJSON(w, 200, item)
}

func (s *Server) cancelEvaluation(w http.ResponseWriter, r *http.Request) {
	item, ok := s.ownedEvaluation(r)
	if !ok {
		writeError(w, 404, "not_found", "evaluation not found")
		return
	}
	updated, err := store.UpdateDoc(s.store, agentEvaluationKind, item.ID, func(current agentEvaluation, exists bool) (agentEvaluation, error) {
		if !exists {
			return agentEvaluation{}, store.ErrNotFound
		}
		if current.Status != "running" {
			return agentEvaluation{}, store.ErrSkipWrite
		}
		current.Status = "cancelling"
		return current, nil
	}, "agent_evaluation.cancelled", actor(r))
	if err != nil && !errors.Is(err, store.ErrSkipWrite) {
		writeError(w, 500, "cancel_failed", err.Error())
		return
	}
	if updated.ID == "" {
		updated = item
	}
	writeJSON(w, 202, updated)
}

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/agent-designs/components", s.designerComponents)
		mux.HandleFunc("GET /api/v1/agent-designs/templates", s.designerTemplates)
		mux.HandleFunc("GET /api/v1/agent-designs/overview", s.designerOverview)
		mux.HandleFunc("POST /api/v1/agent-designs/validate", s.validateUnsavedDesign)
		mux.HandleFunc("POST /api/v1/agent-designs/{id}/validate", s.validateDesign)
		mux.HandleFunc("GET /api/v1/agent-designs/{id}/runs", s.designRuns)
		mux.HandleFunc("POST /api/v1/agent-designs/{id}/runs", s.createDesignRun)
		mux.HandleFunc("GET /api/v1/agent-designs/{id}/runs/{run_id}", s.designRun)
		mux.HandleFunc("GET /api/v1/agent-designs/{id}/runs/{run_id}/events", s.designRunEvents)
		mux.HandleFunc("POST /api/v1/agent-designs/{id}/runs/{run_id}/cancel", s.cancelDesignRun)
		mux.HandleFunc("GET /api/v1/agent-designs/{id}/fixtures", s.designFixtures)
		mux.HandleFunc("PUT /api/v1/agent-designs/{id}/fixtures", s.saveDesignFixtures)
		mux.HandleFunc("GET /api/v1/agent-designs/{id}/evaluations", s.designEvaluations)
		mux.HandleFunc("POST /api/v1/agent-designs/{id}/evaluations", s.createEvaluation)
		mux.HandleFunc("POST /api/v1/agent-designs/{id}/publish", s.publishDesign)
		mux.HandleFunc("GET /api/v1/agent-designs/{id}/versions", s.designVersions)
		mux.HandleFunc("GET /api/v1/agent-versions/{id}", s.agentVersionDetail)
		mux.HandleFunc("POST /api/v1/agent-versions/{id}/deploy", s.deployVersion)
		mux.HandleFunc("POST /api/v1/agent-versions/{id}/runs", s.runVersion)
		mux.HandleFunc("GET /api/v1/agent-evaluations/{id}", s.evaluationDetail)
		mux.HandleFunc("POST /api/v1/agent-evaluations/{id}/cancel", s.cancelEvaluation)
	})
}
