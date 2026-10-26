package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kiongahq/platform/internal/agentgraph"
	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
)

// Native graph runs execute in agent-runtime's designer runner, never in the
// gateway. The gateway records the run, hands the runner a run-scoped
// credential, and the runner calls back through the internal listener for
// every model, tool, knowledge, dataset or API call. The credential carries
// the caller's identity confined to the design's project and to the
// resource-call routes below; it dies when the run ends or is cancelled, and
// the RBAC resolver re-reads grants on every call, so revoked access stops
// the next call of a running graph.
const designRunKind = "agent_design_run"

const (
	maxRunEvents        = 1000
	maxRunEventBytes    = 256 << 10
	maxRunsPerDesign    = 50
	maxRunInputChars    = 32000
	runFlushInterval    = 400 * time.Millisecond
	runCredentialGrace  = 30 * time.Second
	designRunCredential = "designer_run"
)

var designRunID = regexp.MustCompile(`^agr-[a-f0-9]{24}$`)

// runResourceRoutes are the only API calls a run credential may make.
var runResourceRoutes = []*regexp.Regexp{
	regexp.MustCompile(`^POST /api/v1/models/[^/]+/predict$`),
	regexp.MustCompile(`^POST /api/v1/functions/[^/]+/invoke$`),
	regexp.MustCompile(`^POST /api/v1/agent-api-connections/[^/]+/invoke$`),
	regexp.MustCompile(`^GET /api/v1/knowledge-bases$`),
	regexp.MustCompile(`^POST /api/v1/knowledge-bases/[^/]+/search$`),
	regexp.MustCompile(`^GET /api/v1/datasets/[^/]+/object$`),
}

type designTurn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}
type designRunUsage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}
type designRun struct {
	ID               string            `json:"id"`
	DesignID         string            `json:"design_id"`
	VersionID        string            `json:"version_id,omitempty"`
	EvaluationID     string            `json:"evaluation_id,omitempty"`
	ProjectID        string            `json:"project_id"`
	Subject          string            `json:"subject"`
	Roles            []string          `json:"roles,omitempty"`
	Mode             string            `json:"mode"`
	Revision         int               `json:"revision"`
	ContentSHA256    string            `json:"content_sha256"`
	Status           string            `json:"status"`
	TokenHash        string            `json:"token_hash,omitempty"`
	IdempotencyKey   string            `json:"idempotency_key,omitempty"`
	Input            string            `json:"input"`
	SessionID        string            `json:"session_id,omitempty"`
	StopAfter        string            `json:"stop_after,omitempty"`
	AllowSideEffects bool              `json:"allow_side_effects"`
	Events           []json.RawMessage `json:"events"`
	Output           string            `json:"output,omitempty"`
	Error            string            `json:"error,omitempty"`
	Usage            designRunUsage    `json:"usage"`
	DurationMS       int64             `json:"duration_ms"`
	CreatedAt        time.Time         `json:"created_at"`
	FinishedAt       *time.Time        `json:"finished_at,omitempty"`
	ExpiresAt        time.Time         `json:"expires_at"`
}

// view hides credentials and marks runs whose executor vanished (gateway
// restart) as failed instead of running forever.
func (run designRun) view() designRun {
	run.TokenHash, run.IdempotencyKey, run.Roles = "", "", nil
	if (run.Status == "running" || run.Status == "cancelling") && time.Now().After(run.ExpiresAt.Add(runCredentialGrace)) {
		run.Status, run.Error = "failed", "The run was interrupted before it finished."
	}
	if run.Events == nil {
		run.Events = []json.RawMessage{}
	}
	return run
}

func (run designRun) terminal() bool {
	return run.Status == "succeeded" || run.Status == "failed" || run.Status == "cancelled"
}

type runRequest struct {
	Revision         int          `json:"revision"`
	Input            string       `json:"input"`
	SessionID        string       `json:"session_id"`
	History          []designTurn `json:"history"`
	AllowSideEffects bool         `json:"allow_side_effects"`
	StopAfter        string       `json:"stop_after"`
}

// designRunner holds runner configuration and the cancel functions of runs
// executing on this replica.
type designRunner struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	client  *http.Client
}

var runner = &designRunner{cancels: map[string]context.CancelFunc{}, client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

func runnerURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("KIONGA_AGENT_DESIGNER_RUNNER_URL")), "/")
}

// runnerCallbackURL is the internal listener the runner calls for resources.
func runnerCallbackURL() string {
	if value := strings.TrimSpace(os.Getenv("KIONGA_AGENT_DESIGNER_CALLBACK_URL")); value != "" {
		return strings.TrimRight(value, "/")
	}
	return "http://gateway:8087/api/v1"
}

func newOpaqueID(prefix string) (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ownedDesign loads a design the caller may see.
func (s *Server) ownedDesign(r *http.Request) (agentDesign, bool) {
	item, err := store.GetDoc[agentDesign](s.store, agentDesignKind, r.PathValue("id"))
	if err != nil || !projectAllowed(s.store, principal(r), item.ProjectID) {
		return agentDesign{}, false
	}
	return item, true
}

// designRevisionGraph returns the normalized graph of the current revision
// or of a retained earlier one.
func designRevisionGraph(design agentDesign, revision int) (agentgraph.Graph, int, bool) {
	graph := design.graph()
	if revision != 0 && revision != design.Revision {
		found := false
		for _, old := range design.History {
			if old.Revision == revision {
				graph = agentgraph.Graph{Name: old.Name, Description: old.Description, Nodes: old.Nodes, Edges: old.Edges, Settings: old.Settings}
				found = true
				break
			}
		}
		if !found {
			return agentgraph.Graph{}, 0, false
		}
	} else {
		revision = design.Revision
	}
	graph.Nodes = cloneNodes(graph.Nodes)
	graph.Edges = append([]designEdge(nil), graph.Edges...)
	agentgraph.Default().Normalize(&graph)
	return graph, revision, true
}

func (s *Server) createDesignRun(w http.ResponseWriter, r *http.Request) {
	design, ok := s.ownedDesign(r)
	if !ok {
		writeError(w, 404, "not_found", "design not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var req runRequest
	if err := decode(r, &req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	graph, revision, found := designRevisionGraph(design, req.Revision)
	if !found {
		writeError(w, 404, "not_found", "revision not found")
		return
	}
	if req.StopAfter != "" {
		exists := false
		for _, node := range graph.Nodes {
			exists = exists || node.ID == req.StopAfter
		}
		if !exists {
			writeError(w, 422, "validation_error", "stop_after must name a node in the graph")
			return
		}
	}
	run, status, err := s.startDesignRun(r, design, graph, revision, req, "playground", "", "")
	if err != nil {
		writeRunError(w, status, err)
		return
	}
	writeJSON(w, status, run.view())
}

type runStartError struct {
	code    string
	message string
	issues  []agentgraph.Issue
}

func (e *runStartError) Error() string { return e.message }

func writeRunError(w http.ResponseWriter, status int, err error) {
	var startErr *runStartError
	if errors.As(err, &startErr) {
		body := map[string]any{"error": startErr.code, "message": startErr.message}
		if startErr.issues != nil {
			body["issues"] = startErr.issues
		}
		writeJSON(w, status, body)
		return
	}
	writeError(w, status, "run_failed", err.Error())
}

// runCaller is who starts a run: the principal whose access every resource
// call is checked against, and the audit actor.
type runCaller struct {
	principal      auth.Principal
	actor          string
	idempotencyKey string
}

func callerOf(r *http.Request) runCaller {
	return runCaller{principal: principal(r), actor: actor(r), idempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key"))}
}

// startDesignRun validates, records and launches a run. It returns the
// stored run and the HTTP status to report (202 new, 200 idempotent replay).
func (s *Server) startDesignRun(r *http.Request, design agentDesign, graph agentgraph.Graph, revision int, req runRequest, mode, versionID, evaluationID string) (designRun, int, error) {
	run, token, status, err := s.prepareDesignRun(callerOf(r), design, graph, revision, req, mode, versionID, evaluationID)
	if err != nil || status != 202 {
		return run, status, err
	}
	timeout := time.Duration(graph.Settings.TimeoutSeconds)*time.Second + 10*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	runner.track(run.ID, cancel)
	go func() {
		defer runner.untrack(run.ID, cancel)
		s.executeDesignRun(ctx, run, graph, req.History, token)
	}()
	return run, 202, nil
}

// runSync records and executes a run on the calling goroutine (evaluations).
func (s *Server) runSync(parent context.Context, caller runCaller, design agentDesign, graph agentgraph.Graph, revision int, req runRequest, mode, versionID, evaluationID string) (designRun, error) {
	run, token, _, err := s.prepareDesignRun(caller, design, graph, revision, req, mode, versionID, evaluationID)
	if err != nil {
		return designRun{}, err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(graph.Settings.TimeoutSeconds)*time.Second+10*time.Second)
	runner.track(run.ID, cancel)
	defer runner.untrack(run.ID, cancel)
	return s.executeDesignRun(ctx, run, graph, req.History, token), nil
}

func (s *Server) prepareDesignRun(caller runCaller, design agentDesign, graph agentgraph.Graph, revision int, req runRequest, mode, versionID, evaluationID string) (designRun, string, int, error) {
	if runnerURL() == "" {
		return designRun{}, "", 503, &runStartError{code: "runner_unavailable", message: "The agent designer runner is not configured (KIONGA_AGENT_DESIGNER_RUNNER_URL)."}
	}
	if !auth.Allowed(caller.principal, http.MethodPost, "/api/v1/agent-designs/"+design.ID+"/runs") {
		return designRun{}, "", 403, &runStartError{code: "access_denied", message: "Running agents is not assigned to this user."}
	}
	if strings.TrimSpace(req.Input) == "" || len([]rune(req.Input)) > maxRunInputChars {
		return designRun{}, "", 422, &runStartError{code: "validation_error", message: fmt.Sprintf("input must contain 1–%d characters", maxRunInputChars)}
	}
	if len(req.History) > 50 || len(req.SessionID) > 128 {
		return designRun{}, "", 422, &runStartError{code: "validation_error", message: "Send at most 50 history turns and a session ID of at most 128 characters."}
	}
	for _, turn := range req.History {
		if (turn.Role != "user" && turn.Role != "assistant") || len([]rune(turn.Text)) > maxRunInputChars {
			return designRun{}, "", 422, &runStartError{code: "validation_error", message: "History turns need a user or assistant role and at most 32000 characters."}
		}
	}
	issues := agentgraph.Default().Validate(graph, s.resourceCheckerFor(caller.principal, design.ProjectID))
	if !agentgraph.Runnable(issues) {
		return designRun{}, "", 422, &runStartError{code: "graph_not_runnable", message: "Fix the validation errors before running.", issues: issues}
	}
	if len(caller.idempotencyKey) > 128 {
		return designRun{}, "", 422, &runStartError{code: "validation_error", message: "Idempotency-Key can have at most 128 characters."}
	}
	subject := caller.principal.Subject
	if caller.idempotencyKey != "" {
		for _, existing := range store.ListDocs[designRun](s.store, designRunKind) {
			if existing.DesignID == design.ID && existing.Subject == subject && existing.IdempotencyKey == caller.idempotencyKey {
				return existing, "", 200, nil
			}
		}
	}
	hash, err := agentgraph.Hash(graph)
	if err != nil {
		return designRun{}, "", 500, err
	}
	id, err := newOpaqueID("agr-")
	if err != nil {
		return designRun{}, "", 500, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return designRun{}, "", 500, err
	}
	token := hex.EncodeToString(secret)
	now := time.Now().UTC()
	timeout := time.Duration(graph.Settings.TimeoutSeconds) * time.Second
	run := designRun{ID: id, DesignID: design.ID, VersionID: versionID, EvaluationID: evaluationID, ProjectID: design.ProjectID, Subject: subject, Roles: caller.principal.Roles,
		Mode: mode, Revision: revision, ContentSHA256: hash, Status: "running", TokenHash: hashToken(token), IdempotencyKey: caller.idempotencyKey,
		Input: req.Input, SessionID: req.SessionID, StopAfter: req.StopAfter, AllowSideEffects: req.AllowSideEffects,
		Events: []json.RawMessage{}, CreatedAt: now, ExpiresAt: now.Add(timeout + 10*time.Second)}
	saved, err := store.UpdateDoc(s.store, designRunKind, id, func(_ designRun, exists bool) (designRun, error) {
		if exists {
			return designRun{}, store.ErrConflict
		}
		return run, nil
	}, "agent_design_run.started", caller.actor)
	if err != nil {
		return designRun{}, "", 500, err
	}
	if mode == "playground" {
		s.pruneDesignRuns(design.ID, caller.actor)
	}
	return saved, token, 202, nil
}

func (d *designRunner) track(id string, cancel context.CancelFunc) {
	d.mu.Lock()
	d.cancels[id] = cancel
	d.mu.Unlock()
}

func (d *designRunner) untrack(id string, cancel context.CancelFunc) {
	cancel()
	d.mu.Lock()
	delete(d.cancels, id)
	d.mu.Unlock()
}

// pruneDesignRuns keeps the newest finished runs of a design (bounded retention).
func (s *Server) pruneDesignRuns(designID, actorName string) {
	runs := []designRun{}
	for _, run := range store.ListDocs[designRun](s.store, designRunKind) {
		if run.DesignID == designID && run.Mode == "playground" {
			runs = append(runs, run)
		}
	}
	if len(runs) <= maxRunsPerDesign {
		return
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })
	for _, run := range runs[maxRunsPerDesign:] {
		if run.view().terminal() {
			_ = s.store.DeleteDocument(designRunKind, run.ID, "agent_design_run.pruned", actorName)
		}
	}
}

type runnerPayload struct {
	RunID            string           `json:"run_id"`
	Graph            agentgraph.Graph `json:"graph"`
	Input            string           `json:"input"`
	SessionID        string           `json:"session_id"`
	History          []designTurn     `json:"history"`
	AllowSideEffects bool             `json:"allow_side_effects"`
	StopAfter        string           `json:"stop_after,omitempty"`
	Gateway          struct {
		URL      string `json:"url"`
		RunToken string `json:"run_token"`
	} `json:"gateway"`
}

type runEventHead struct {
	Seq    int    `json:"seq"`
	Type   string `json:"type"`
	Output string `json:"output"`
	Usage  *struct {
		InputTokens  int     `json:"input_tokens"`
		OutputTokens int     `json:"output_tokens"`
		CostUSD      float64 `json:"cost_usd"`
	} `json:"usage"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// executeDesignRun streams the runner's NDJSON events into the run record.
// It returns the final run so evaluations can score it synchronously.
func (s *Server) executeDesignRun(ctx context.Context, run designRun, graph agentgraph.Graph, history []designTurn, token string) designRun {
	started := time.Now()
	var mu, flushMu sync.Mutex
	pending := []json.RawMessage{}
	final := run
	lastSeq := -1
	finish := func(status, output, message string, usage designRunUsage) {
		final.Status, final.Output, final.Error, final.Usage = status, output, message, usage
	}
	flush := func(done bool) {
		flushMu.Lock()
		defer flushMu.Unlock()
		mu.Lock()
		events := pending
		pending = []json.RawMessage{}
		snapshot := final
		mu.Unlock()
		action := ""
		if done {
			action = "agent_design_run.finished"
		}
		updated, err := store.UpdateDoc(s.store, designRunKind, run.ID, func(current designRun, exists bool) (designRun, error) {
			if !exists {
				return designRun{}, store.ErrNotFound
			}
			if len(events) == 0 && !done {
				return designRun{}, store.ErrSkipWrite
			}
			room := maxRunEvents - len(current.Events)
			if room < len(events) {
				events = events[:max(room, 0)]
			}
			current.Events = append(current.Events, events...)
			if done {
				now := time.Now().UTC()
				current.Status, current.Output, current.Error, current.Usage = snapshot.Status, snapshot.Output, snapshot.Error, snapshot.Usage
				current.DurationMS, current.FinishedAt = time.Since(started).Milliseconds(), &now
			}
			return current, nil
		}, action, "system:agent-designer")
		if err == nil && done {
			mu.Lock()
			final = updated
			mu.Unlock()
		}
	}
	// Watch for cancellation requested on any replica.
	stopWatch := make(chan struct{})
	go func() {
		ticker := time.NewTicker(runFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				flush(false)
				if current, err := store.GetDoc[designRun](s.store, designRunKind, run.ID); err == nil && current.Status == "cancelling" {
					runner.cancel(run.ID)
				}
			}
		}
	}()
	record := func(raw []byte) {
		var head runEventHead
		if len(raw) > maxRunEventBytes || json.Unmarshal(raw, &head) != nil || head.Seq <= lastSeq {
			return
		}
		lastSeq = head.Seq
		mu.Lock()
		defer mu.Unlock()
		pending = append(pending, append(json.RawMessage(nil), raw...))
		switch head.Type {
		case "run.completed":
			usage := designRunUsage{}
			if head.Usage != nil {
				usage = designRunUsage{InputTokens: head.Usage.InputTokens, OutputTokens: head.Usage.OutputTokens, CostUSD: head.Usage.CostUSD}
			}
			finish("succeeded", head.Output, "", usage)
		case "run.failed":
			message := "The run failed."
			if head.Error != nil {
				message = head.Error.Message
			}
			finish("failed", "", message, final.Usage)
		case "run.cancelled":
			finish("cancelled", "", "The run was cancelled.", final.Usage)
		}
	}
	synthesize := func(kind, code, message string) {
		event := map[string]any{"seq": lastSeq + 1, "type": kind, "run_id": run.ID, "ts": time.Now().UTC().Format(time.RFC3339Nano),
			"error": map[string]string{"code": code, "message": message}}
		raw, _ := json.Marshal(event)
		record(raw)
	}
	err := s.streamFromRunner(ctx, run, graph, history, token, record)
	close(stopWatch)
	mu.Lock()
	finished := final.Status != "running"
	mu.Unlock()
	if !finished {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			synthesize("run.cancelled", "cancelled", "The run was cancelled.")
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			synthesize("run.failed", "timeout", "The run exceeded its time limit.")
		case err != nil:
			log.Printf("agent designer run %s: %v", run.ID, err)
			var rejected *runnerRejection
			if errors.As(err, &rejected) {
				synthesize("run.failed", rejected.code, rejected.message)
			} else {
				synthesize("run.failed", "resource_unavailable", "The agent designer runner is unavailable.")
			}
		default:
			synthesize("run.failed", "node_error", "The runner ended without a result.")
		}
	}
	flush(true)
	mu.Lock()
	defer mu.Unlock()
	return final
}

func (s *Server) streamFromRunner(ctx context.Context, run designRun, graph agentgraph.Graph, history []designTurn, token string, emit func([]byte)) error {
	payload := runnerPayload{RunID: run.ID, Graph: graph, Input: run.Input, SessionID: run.SessionID, History: history, AllowSideEffects: run.AllowSideEffects, StopAfter: run.StopAfter}
	if payload.History == nil {
		payload.History = []designTurn{}
	}
	payload.Gateway.URL, payload.Gateway.RunToken = runnerCallbackURL(), token
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, runnerURL()+"/runs", bytes.NewReader(body)) // #nosec G704 -- runner URL is operator configuration
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/x-ndjson")
	request.Header.Set("Authorization", "Bearer "+os.Getenv("KIONGA_AGENT_DESIGNER_RUNNER_TOKEN"))
	response, err := runner.client.Do(request) // #nosec G704 -- fixed operator-configured runner, redirects disabled
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		switch response.StatusCode {
		case http.StatusTooManyRequests:
			return &runnerRejection{code: "budget_exceeded", message: "The agent runner is at capacity; try again shortly."}
		case http.StatusUnprocessableEntity:
			return &runnerRejection{code: "invalid_graph", message: "The agent runner rejected this graph; validate it and try again."}
		}
		return fmt.Errorf("runner returned %d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), maxRunEventBytes+1)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) > 0 {
			emit(line)
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// runnerRejection is a runner refusal that the user can act on.
type runnerRejection struct{ code, message string }

func (e *runnerRejection) Error() string { return e.message }

func (d *designRunner) cancel(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cancel := d.cancels[id]; cancel != nil {
		cancel()
		return true
	}
	return false
}

func (s *Server) designRunFor(r *http.Request) (designRun, bool) {
	design, ok := s.ownedDesign(r)
	runID := r.PathValue("run_id")
	if !ok || !designRunID.MatchString(runID) {
		return designRun{}, false
	}
	run, err := store.GetDoc[designRun](s.store, designRunKind, runID)
	if err != nil || run.DesignID != design.ID {
		return designRun{}, false
	}
	return run, true
}

func (s *Server) designRuns(w http.ResponseWriter, r *http.Request) {
	design, ok := s.ownedDesign(r)
	if !ok {
		writeError(w, 404, "not_found", "design not found")
		return
	}
	items := []designRun{}
	for _, run := range store.ListDocs[designRun](s.store, designRunKind) {
		if run.DesignID == design.ID && run.Mode == "playground" {
			run = run.view()
			run.Events = nil
			items = append(items, run)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) designRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.designRunFor(r)
	if !ok {
		writeError(w, 404, "not_found", "run not found")
		return
	}
	writeJSON(w, 200, run.view())
}

func (s *Server) cancelDesignRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.designRunFor(r)
	if !ok {
		writeError(w, 404, "not_found", "run not found")
		return
	}
	if !auth.Allowed(principal(r), http.MethodPost, r.URL.Path) {
		writeError(w, 403, "access_denied", "Running agents is not assigned to this user.")
		return
	}
	if run.view().terminal() {
		writeJSON(w, 200, run.view())
		return
	}
	updated, err := store.UpdateDoc(s.store, designRunKind, run.ID, func(current designRun, exists bool) (designRun, error) {
		if !exists {
			return designRun{}, store.ErrNotFound
		}
		if current.terminal() {
			return designRun{}, store.ErrSkipWrite
		}
		current.Status = "cancelling"
		return current, nil
	}, "agent_design_run.cancelled", actor(r))
	if err != nil && !errors.Is(err, store.ErrSkipWrite) {
		writeError(w, 500, "cancel_failed", err.Error())
		return
	}
	runner.cancel(run.ID)
	if updated.ID == "" {
		updated = run
	}
	writeJSON(w, 202, updated.view())
}

// designRunEvents serves events after a sequence number: as Server-Sent
// Events when the client asks for text/event-stream (resuming from
// Last-Event-ID), otherwise as a JSON page for polling clients.
func (s *Server) designRunEvents(w http.ResponseWriter, r *http.Request) {
	run, ok := s.designRunFor(r)
	if !ok {
		writeError(w, 404, "not_found", "run not found")
		return
	}
	after := -1
	if value, err := strconv.Atoi(r.URL.Query().Get("after")); err == nil {
		after = value
	}
	if value, err := strconv.Atoi(r.Header.Get("Last-Event-ID")); err == nil && value > after {
		after = value
	}
	page := func(current designRun) ([]json.RawMessage, int) {
		out := []json.RawMessage{}
		last := after
		for _, raw := range current.Events {
			var head runEventHead
			if json.Unmarshal(raw, &head) == nil && head.Seq > after {
				out = append(out, raw)
				last = head.Seq
			}
		}
		return out, last
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		events, last := page(run)
		view := run.view()
		writeJSON(w, 200, map[string]any{"status": view.Status, "events": events, "next": last, "done": view.terminal()})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 501, "streaming_unavailable", "Event streaming is not available.")
		return
	}
	if controller := http.NewResponseController(w); controller != nil {
		_ = controller.SetWriteDeadline(time.Time{})
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	deadline := time.Now().Add(5 * time.Minute)
	heartbeat := time.Now()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := store.GetDoc[designRun](s.store, designRunKind, run.ID)
		if err != nil {
			fmt.Fprint(w, "event: end\ndata: {\"status\":\"missing\"}\n\n")
			flusher.Flush()
			return
		}
		events, last := page(current)
		for _, raw := range events {
			var head runEventHead
			_ = json.Unmarshal(raw, &head)
			fmt.Fprintf(w, "id: %d\nevent: run\ndata: %s\n\n", head.Seq, raw) // #nosec G705 -- stored JSON event
			heartbeat = time.Now()
		}
		after = last
		view := current.view()
		if view.terminal() {
			status, _ := json.Marshal(map[string]string{"status": view.Status})
			fmt.Fprintf(w, "event: end\ndata: %s\n\n", status) // #nosec G705 -- JSON-encoded status
			flusher.Flush()
			return
		}
		if time.Since(heartbeat) > 15*time.Second {
			fmt.Fprint(w, ": keep-alive\n\n")
			heartbeat = time.Now()
		}
		flusher.Flush()
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// DesignerRunIdentity authenticates the agent designer runner's resource
// calls on the internal listener and passes every other request to next
// (the workspace identity handler). A request carrying run headers never
// falls through: it is authenticated as the run or rejected.
func DesignerRunIdentity(next, api http.Handler, docs store.Documents) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runID, token := r.Header.Get("X-Kionga-Run-ID"), r.Header.Get("X-Kionga-Run-Token")
		if runID == "" && token == "" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || !designRunID.MatchString(runID) || len(token) < 40 || len(token) > 200 {
			http.Error(w, "run credential rejected", http.StatusUnauthorized)
			return
		}
		allowed := false
		for _, route := range runResourceRoutes {
			allowed = allowed || route.MatchString(r.Method+" "+r.URL.Path)
		}
		if !allowed {
			http.Error(w, "run credential cannot call this API", http.StatusForbidden)
			return
		}
		run, err := store.GetDoc[designRun](docs, designRunKind, runID)
		if err != nil || subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(run.TokenHash)) != 1 {
			http.Error(w, "run credential rejected", http.StatusUnauthorized)
			return
		}
		if run.Status != "running" || time.Now().After(run.ExpiresAt) {
			http.Error(w, "run is no longer active", http.StatusForbidden)
			return
		}
		r.Header.Del("X-Kionga-Run-ID")
		r.Header.Del("X-Kionga-Run-Token")
		// Roles from run start apply only when the subject has no stored
		// access profile (SSO group or local bootstrap administrators); the
		// RBAC resolver replaces them with current grants whenever one exists.
		roles := run.Roles
		if len(roles) == 0 {
			roles = []string{auth.RoleUser}
		}
		value := auth.Principal{Subject: run.Subject, Roles: roles, Credential: designRunCredential, WorkspaceProjectID: run.ProjectID}
		api.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), value)))
	})
}
