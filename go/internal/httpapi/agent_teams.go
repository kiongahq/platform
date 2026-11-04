package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kiongahq/platform/internal/agentteam"
	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/redact"
	"github.com/kiongahq/platform/internal/store"
)

// Team runs: the gateway is the authority for state, RBAC and tool dispatch. The
// scheduler (agent_teams_scheduler.go) does the work; these routes record intent.
const (
	teamRunKind    = "team_run"
	teamWorkerKind = "team_worker"
	teamIndexKind  = "team_run_index"
	teamIndexID    = "active"
)

var (
	teamRunID    = regexp.MustCompile(`^atr-[a-f0-9]{24}$`)
	teamWorkerID = regexp.MustCompile(`^atw-[a-f0-9]{24}$`)
)

// teamIndex names runs the scheduler still has to look at, so a scan does not
// read every run ever started.
type teamIndex struct {
	Runs map[string]bool `json:"runs"`
}

func teamIndexSet(docs store.Documents, runID string, active bool) error {
	_, err := store.UpdateDoc(docs, teamIndexKind, teamIndexID, func(current teamIndex, _ bool) (teamIndex, error) {
		if current.Runs == nil {
			current.Runs = map[string]bool{}
		}
		if active {
			current.Runs[runID] = true
		} else {
			delete(current.Runs, runID)
		}
		return current, nil
	}, "", "")
	return err
}

func hashedID(prefix string, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return prefix + hex.EncodeToString(sum[:12])
}

func (s *Server) teamSandbox() teamSandbox {
	if s.teamSandboxClient != nil {
		return s.teamSandboxClient
	}
	return defaultTeamSandbox
}

func (s *Server) teamRunnerFor() teamRunner {
	if s.teamRunnerClient != nil {
		return s.teamRunnerClient
	}
	return httpTeamRunner{}
}

// teamRunFor loads a run the caller may see.
func (s *Server) teamRunFor(r *http.Request) (agentteam.Run, bool) {
	id := r.PathValue("id")
	if !teamRunID.MatchString(id) {
		return agentteam.Run{}, false
	}
	run, err := store.GetDoc[agentteam.Run](s.store, teamRunKind, id)
	if err != nil || !projectAllowed(s.store, principal(r), run.ProjectID) {
		return agentteam.Run{}, false
	}
	return run, true
}

// teamWorkers loads a run's workers. A reserved ID whose document was never
// written (a crash inside spawn_worker) is skipped.
func teamWorkers(docs store.Documents, run agentteam.Run) []agentteam.Worker {
	workers := make([]agentteam.Worker, 0, len(run.WorkerIDs))
	for _, id := range run.WorkerIDs {
		if worker, err := store.GetDoc[agentteam.Worker](docs, teamWorkerKind, id); err == nil {
			workers = append(workers, worker)
		}
	}
	return workers
}

func sumUsage(workers []agentteam.Worker) agentteam.Usage {
	total := agentteam.Usage{}
	for _, worker := range workers {
		total = total.Add(worker.Usage)
	}
	return total
}

// buildSnapshot copies a team and its members. Names must be unique inside a
// snapshot, because a model addresses members by name.
func (s *Server) buildSnapshot(team agentTeamDoc) (agentteam.Snapshot, []string) {
	snapshot := agentteam.Snapshot{TeamID: team.ID, TeamRevision: team.Revision, Team: team.Content}
	issues := []string{}
	names := map[string]bool{}
	for _, id := range team.Content.Members {
		member, err := store.GetDoc[agentDefinitionDoc](s.store, agentDefinitionKind, id)
		if err != nil || member.ProjectID != team.ProjectID {
			issues = append(issues, "member "+id+" no longer exists in this project")
			continue
		}
		if names[member.Content.Name] {
			issues = append(issues, "member name "+member.Content.Name+" appears twice")
		}
		names[member.Content.Name] = true
		snapshot.Members = append(snapshot.Members, agentteam.MemberSnapshot{ID: member.ID, Revision: member.Revision, DefinitionContent: member.Content})
	}
	return snapshot, issues
}

type teamRunView struct {
	agentteam.Run
	Sandbox *sandboxStatus `json:"sandbox,omitempty"`
}

func (s *Server) createTeamRun(w http.ResponseWriter, r *http.Request) {
	caller := principal(r)
	if machineCredential(caller) {
		writeError(w, 403, "access_denied", "Team runs are started by people.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	var req struct {
		TeamID           string `json:"team_id"`
		Input            string `json:"input"`
		AllowSideEffects bool   `json:"allow_side_effects"`
		IdempotencyKey   string `json:"idempotency_key"`
	}
	if err := decode(r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	if !agentTeamID.MatchString(req.TeamID) {
		writeError(w, 404, "not_found", "team not found")
		return
	}
	team, err := store.GetDoc[agentTeamDoc](s.store, agentTeamKind, req.TeamID)
	if err != nil || !projectAllowed(s.store, caller, team.ProjectID) {
		writeError(w, 404, "not_found", "team not found")
		return
	}
	if strings.TrimSpace(req.Input) == "" || utf8.RuneCountInString(req.Input) > 64000 || len(req.IdempotencyKey) > 200 {
		writeError(w, 422, "validation_error", "Send input of at most 64000 characters and an idempotency key of at most 200.")
		return
	}
	snapshot, issues := s.buildSnapshot(team)
	if len(issues) > 0 {
		writeTeamIssues(w, issues)
		return
	}
	digest, err := agentteam.ContentSHA256(snapshot)
	if err != nil {
		writeError(w, 500, "internal_error", "could not hash the team snapshot")
		return
	}
	var runID string
	if req.IdempotencyKey != "" {
		runID = hashedID("atr-", caller.Subject, team.ID, req.IdempotencyKey)
	} else if runID, err = newOpaqueID("atr-"); err != nil {
		writeError(w, 500, "internal_error", "could not create a run ID")
		return
	}
	coordinatorID := hashedID("atw-", runID, agentteam.ReservedName)
	now := time.Now().UTC()
	_, profileErr := s.store.AccessFor(caller.Subject)
	run := agentteam.Run{
		ID: runID, ProjectID: team.ProjectID, TeamID: team.ID, Input: req.Input, AllowSideEffects: req.AllowSideEffects,
		IdempotencyKey: req.IdempotencyKey, Subject: caller.Subject, Roles: caller.Roles, HadProfile: profileErr == nil,
		Status: agentteam.RunRunning, Snapshot: snapshot, ContentSHA256: digest, CoordinatorID: coordinatorID,
		WorkerIDs: []string{coordinatorID}, CreatedAt: now, UpdatedAt: now,
	}
	coordinator := agentteam.Worker{ID: coordinatorID, RunID: runID, ProjectID: team.ProjectID, Role: agentteam.RoleCoordinator,
		Agent: agentteam.ReservedName, Task: redact.String(req.Input), Status: agentteam.WorkerQueued, Transcript: []agentteam.Message{}, CreatedAt: now, UpdatedAt: now}
	if err := agentteam.AddInbox(&coordinator, agentteam.InboxMessage{ID: runID + ":input", Source: "person", Kind: "task", Body: redact.String(req.Input)}); err != nil {
		writeError(w, 500, "internal_error", "could not queue the task")
		return
	}
	// Worker first, then run, then index: an interrupted create is invisible, and
	// retrying the same idempotency key completes it.
	if _, err := store.UpdateDoc(s.store, teamWorkerKind, coordinatorID, func(current agentteam.Worker, exists bool) (agentteam.Worker, error) {
		if exists {
			return current, store.ErrSkipWrite
		}
		return coordinator, nil
	}, "", ""); err != nil {
		writeError(w, 500, "save_failed", "could not save the run")
		return
	}
	replay := false
	saved, err := store.UpdateDoc(s.store, teamRunKind, runID, func(current agentteam.Run, exists bool) (agentteam.Run, error) {
		if exists {
			replay = true
			if current.Input != req.Input || current.AllowSideEffects != req.AllowSideEffects {
				return current, store.ErrConflict
			}
			return current, store.ErrSkipWrite
		}
		return run, nil
	}, "team_run.started", actor(r))
	if errors.Is(err, store.ErrConflict) {
		writeError(w, 409, "idempotency_conflict", "This idempotency key was used for a different request.")
		return
	}
	if err != nil {
		writeError(w, 500, "save_failed", "could not save the run")
		return
	}
	if err := teamIndexSet(s.store, runID, !saved.Terminal() || saved.SettledAt == nil); err != nil {
		writeError(w, 500, "save_failed", "could not schedule the run")
		return
	}
	status := 202
	if replay {
		status = 200
	} else {
		appendTeamEvent(s.store, teamEvent{Type: "run.started", RunID: runID, Status: agentteam.RunRunning, InputPreview: preview(req.Input)})
		appendTeamEvent(s.store, teamEvent{Type: "worker.spawned", RunID: runID, WorkerID: coordinatorID, Agent: agentteam.ReservedName, Role: agentteam.RoleCoordinator, Status: agentteam.WorkerQueued})
	}
	kickTeamScheduler()
	writeJSON(w, status, s.teamRunDetail(r, saved))
}

func (s *Server) teamRunDetail(r *http.Request, run agentteam.Run) teamRunView {
	if !run.Terminal() {
		run.Usage = sumUsage(teamWorkers(s.store, run))
	}
	view := teamRunView{Run: run}
	if run.Snapshot.UsesSandbox() {
		status := s.teamHealth.status(r.Context(), s.teamSandbox())
		view.Sandbox = &status
	}
	return view
}

func (s *Server) teamRuns(w http.ResponseWriter, r *http.Request) {
	teamID, projectID := r.URL.Query().Get("team_id"), r.URL.Query().Get("project_id")
	items := []agentteam.Run{}
	for _, run := range store.ListDocs[agentteam.Run](s.store, teamRunKind) {
		if !projectAllowed(s.store, principal(r), run.ProjectID) || (teamID != "" && run.TeamID != teamID) || (projectID != "" && run.ProjectID != projectID) {
			continue
		}
		items = append(items, run)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > 200 {
		items = items[:200]
	}
	for i := range items {
		if !items[i].Terminal() {
			items[i].Usage = sumUsage(teamWorkers(s.store, items[i]))
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) teamRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.teamRunFor(r)
	if !ok {
		writeError(w, 404, "not_found", "run not found")
		return
	}
	writeJSON(w, 200, s.teamRunDetail(r, run))
}

// workerView hides scheduler internals. withTranscript adds the redacted
// transcript and inbox; list responses stay small.
func workerView(worker agentteam.Worker, withTranscript bool) agentteam.Worker {
	worker.LeaseToken, worker.InboxSeen, worker.ActiveSince = "", nil, nil
	if !withTranscript {
		worker.Transcript, worker.Inbox = []agentteam.Message{}, []agentteam.InboxMessage{}
		return worker
	}
	transcript := make([]agentteam.Message, len(worker.Transcript))
	for i, message := range worker.Transcript {
		message.Content = redact.String(message.Content)
		if len(message.ToolCalls) > 0 {
			calls := make([]agentteam.ToolCall, len(message.ToolCalls))
			for j, call := range message.ToolCalls {
				call.Arguments = redactArguments(call.Arguments)
				calls[j] = call
			}
			message.ToolCalls = calls
		}
		transcript[i] = message
	}
	worker.Transcript = transcript
	if worker.Inbox == nil {
		worker.Inbox = []agentteam.InboxMessage{}
	}
	return worker
}

// redactArguments keeps arguments valid JSON: redacting raw text could cut a string.
func redactArguments(raw json.RawMessage) json.RawMessage {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return json.RawMessage(`{}`)
	}
	out, err := json.Marshal(redactValue(value))
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return out
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case string:
		return redact.String(typed)
	case []any:
		for i := range typed {
			typed[i] = redactValue(typed[i])
		}
	case map[string]any:
		for key := range typed {
			typed[key] = redactValue(typed[key])
		}
	}
	return value
}

func (s *Server) teamRunWorkers(w http.ResponseWriter, r *http.Request) {
	run, ok := s.teamRunFor(r)
	if !ok {
		writeError(w, 404, "not_found", "run not found")
		return
	}
	items := []agentteam.Worker{}
	for _, worker := range teamWorkers(s.store, run) {
		items = append(items, workerView(worker, false))
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) teamRunWorker(w http.ResponseWriter, r *http.Request) {
	run, ok := s.teamRunFor(r)
	if !ok {
		writeError(w, 404, "not_found", "run not found")
		return
	}
	id := r.PathValue("wid")
	if !teamWorkerID.MatchString(id) {
		writeError(w, 404, "not_found", "worker not found")
		return
	}
	worker, err := store.GetDoc[agentteam.Worker](s.store, teamWorkerKind, id)
	if err != nil || worker.RunID != run.ID {
		writeError(w, 404, "not_found", "worker not found")
		return
	}
	writeJSON(w, 200, workerView(worker, true))
}

// teamRunMessage takes a person's message. The audit event is written in the
// same transaction as the inbox change.
func (s *Server) teamRunMessage(w http.ResponseWriter, r *http.Request) {
	caller := principal(r)
	if machineCredential(caller) {
		writeError(w, 403, "access_denied", "source=\"person\" messages come from people, not agent identities.")
		return
	}
	run, ok := s.teamRunFor(r)
	if !ok {
		writeError(w, 404, "not_found", "run not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	if err := decode(r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(req.Text) == "" || utf8.RuneCountInString(req.Text) > 32000 {
		writeError(w, 422, "validation_error", "Send text of at most 32000 characters.")
		return
	}
	if run.Status != agentteam.RunRunning {
		writeError(w, 409, "run_not_running", "This run is not running.")
		return
	}
	target, code := agentteam.ResolveTarget(req.To, teamWorkers(s.store, run))
	switch code {
	case agentteam.ErrAmbiguousWorker:
		writeError(w, 409, code, "More than one worker has this name; use the worker ID.")
		return
	case agentteam.ErrUnknownWorker:
		writeError(w, 404, code, "No worker matches this address.")
		return
	}
	id, err := newOpaqueID("msg-")
	if err != nil {
		writeError(w, 500, "internal_error", "could not create a message ID")
		return
	}
	text := redact.String(req.Text)
	_, err = store.UpdateDoc(s.store, teamWorkerKind, target.ID, func(current agentteam.Worker, exists bool) (agentteam.Worker, error) {
		if !exists {
			return current, store.ErrNotFound
		}
		if err := agentteam.AddInbox(&current, agentteam.InboxMessage{ID: id, Source: "person", Kind: "message", Body: text}); err != nil {
			return current, err
		}
		current.UpdatedAt = time.Now().UTC()
		return current, nil
	}, "team_run.message", actor(r))
	if errors.Is(err, agentteam.ErrWorkerClosed) {
		writeError(w, 409, "worker_closed", "This worker cannot receive messages.")
		return
	}
	if err != nil {
		writeError(w, 500, "save_failed", "could not deliver the message")
		return
	}
	appendTeamEvent(s.store, teamEvent{Type: "worker.message", RunID: run.ID, From: "person", To: target.ID, WorkerID: target.ID, Agent: target.Agent, MessagePreview: preview(text)})
	kickTeamScheduler()
	writeJSON(w, 202, map[string]string{"to": target.ID, "id": id})
}

func (s *Server) cancelTeamRun(w http.ResponseWriter, r *http.Request) {
	if machineCredential(principal(r)) {
		writeError(w, 403, "access_denied", "Team runs are controlled by people.")
		return
	}
	run, ok := s.teamRunFor(r)
	if !ok {
		writeError(w, 404, "not_found", "run not found")
		return
	}
	saved, err := store.UpdateDoc(s.store, teamRunKind, run.ID, func(current agentteam.Run, exists bool) (agentteam.Run, error) {
		if !exists {
			return current, store.ErrNotFound
		}
		if current.Status != agentteam.RunRunning {
			return current, store.ErrSkipWrite
		}
		current.Status, current.UpdatedAt = agentteam.RunCancelling, time.Now().UTC()
		return current, nil
	}, "team_run.cancel_requested", actor(r))
	if err != nil {
		writeError(w, 500, "save_failed", "could not cancel the run")
		return
	}
	kickTeamScheduler()
	writeJSON(w, 202, s.teamRunDetail(r, saved))
}

// resumeTeamRun requeues failed workers after an operator fix. Budgets stay
// spent: a run or worker stopped by a limit does not come back.
func (s *Server) resumeTeamRun(w http.ResponseWriter, r *http.Request) {
	if machineCredential(principal(r)) {
		writeError(w, 403, "access_denied", "Team runs are controlled by people.")
		return
	}
	run, ok := s.teamRunFor(r)
	if !ok {
		writeError(w, 404, "not_found", "run not found")
		return
	}
	switch {
	case run.Status != agentteam.RunRunning && run.Status != agentteam.RunFailed:
		writeError(w, 409, "not_resumable", "Only a running or failed run can resume.")
		return
	case run.Error == agentteam.ErrBudgetExceeded:
		writeError(w, 409, "not_resumable", "This run stopped on a budget limit.")
		return
	case !time.Now().Before(run.Deadline()):
		writeError(w, 409, "not_resumable", "This run has used its max_seconds.")
		return
	}
	workers := teamWorkers(s.store, run)
	requeue := 0
	for _, worker := range workers {
		if worker.Status == agentteam.WorkerFailed && worker.LastErrorResumable {
			requeue++
		}
	}
	if run.Status == agentteam.RunRunning && requeue == 0 {
		writeError(w, 409, "not_resumable", "No worker in this run can be resumed.")
		return
	}
	note, err := newOpaqueID("msg-")
	if err != nil {
		writeError(w, 500, "internal_error", "could not create a message ID")
		return
	}
	saved, err := store.UpdateDoc(s.store, teamRunKind, run.ID, func(current agentteam.Run, exists bool) (agentteam.Run, error) {
		if !exists {
			return current, store.ErrNotFound
		}
		if current.Status != agentteam.RunRunning && current.Status != agentteam.RunFailed {
			return current, errors.New("run is no longer resumable")
		}
		current.Status, current.Error, current.ErrorMessage = agentteam.RunRunning, "", ""
		current.FinishedAt, current.SettledAt = nil, nil
		current.ResumeCount++
		current.UpdatedAt = time.Now().UTC()
		return current, nil
	}, "team_run.resumed", actor(r))
	if err != nil {
		writeError(w, 409, "not_resumable", "This run can no longer resume.")
		return
	}
	if err := teamIndexSet(s.store, run.ID, true); err != nil {
		writeError(w, 500, "save_failed", "could not schedule the run")
		return
	}
	for _, worker := range workers {
		_, err := store.UpdateDoc(s.store, teamWorkerKind, worker.ID, func(current agentteam.Worker, _ bool) (agentteam.Worker, error) {
			changed := agentteam.Requeue(&current)
			// The settle sweep cancels a coordinator when its run fails. Nothing
			// else can drive the run, so the person's resume restarts it.
			if current.Role == agentteam.RoleCoordinator && current.Status == agentteam.WorkerCancelled {
				current.Status = agentteam.WorkerQueued
				changed = true
			}
			if current.Role == agentteam.RoleCoordinator && changed {
				if err := agentteam.AddInbox(&current, agentteam.InboxMessage{ID: note, Source: "person", Kind: "message", Body: "A person resumed this run. Check your workers and continue."}); err != nil {
					return current, err
				}
			}
			if !changed {
				return current, store.ErrSkipWrite
			}
			current.UpdatedAt = time.Now().UTC()
			return current, nil
		}, "", "")
		if err != nil {
			writeError(w, 500, "save_failed", "could not requeue a worker")
			return
		}
	}
	count := saved.ResumeCount
	appendTeamEvent(s.store, teamEvent{Type: "run.resumed", RunID: run.ID, Status: agentteam.RunRunning, ResumeCount: &count})
	kickTeamScheduler()
	writeJSON(w, 202, s.teamRunDetail(r, saved))
}

// starterAccess re-resolves the person who started a run. lost means suspended,
// removed, out of the project or without agents write. A store failure is
// returned as err, so an outage does not fail the run.
func (s *Server) starterAccess(run agentteam.Run) (value auth.Principal, lost bool, err error) {
	value = auth.Principal{Subject: run.Subject, Roles: run.Roles}
	access, accessErr := s.store.AccessFor(run.Subject)
	switch {
	case accessErr == nil:
		value.Roles, value.Services, value.ProjectIDs = []string{access.Role}, access.Services, access.ProjectIDs
		value.Disabled, value.Provisioned = access.Disabled, true
	case !errors.Is(accessErr, store.ErrNotFound):
		return value, false, accessErr
	case run.HadProfile:
		return value, true, nil
	}
	if !auth.Allowed(value, http.MethodPost, "/api/v1/team-runs") || !projectAllowed(s.store, value, run.ProjectID) {
		return value, true, nil
	}
	return value, false, nil
}

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/team-runs", s.createTeamRun)
		mux.HandleFunc("GET /api/v1/team-runs", s.teamRuns)
		mux.HandleFunc("GET /api/v1/team-runs/{id}", s.teamRun)
		mux.HandleFunc("GET /api/v1/team-runs/{id}/events", s.teamRunEvents)
		mux.HandleFunc("GET /api/v1/team-runs/{id}/events/stream", s.teamRunEventStream)
		mux.HandleFunc("GET /api/v1/team-runs/{id}/workers", s.teamRunWorkers)
		mux.HandleFunc("GET /api/v1/team-runs/{id}/workers/{wid}", s.teamRunWorker)
		mux.HandleFunc("POST /api/v1/team-runs/{id}/messages", s.teamRunMessage)
		mux.HandleFunc("POST /api/v1/team-runs/{id}/cancel", s.cancelTeamRun)
		mux.HandleFunc("POST /api/v1/team-runs/{id}/resume", s.resumeTeamRun)
	})
	summary := func(text string) map[string]any { return map[string]any{"summary": text} }
	for route, ops := range map[string]map[string]any{
		"/api/v1/agent-definitions":                {"get": summary("List reusable agent definitions in assigned projects"), "post": summary("Create an agent definition")},
		"/api/v1/agent-definitions/{id}":           {"get": summary("Read an agent definition"), "put": summary("Save an agent definition; a stale revision returns 409 with latest"), "delete": summary("Delete an agent definition no team uses")},
		"/api/v1/agent-definitions/{id}/revisions": {"get": summary("List an agent definition's last 20 revisions")},
		"/api/v1/agent-teams":                      {"get": summary("List agent teams in assigned projects"), "post": summary("Create an agent team")},
		"/api/v1/agent-teams/{id}":                 {"get": summary("Read an agent team"), "put": summary("Save an agent team; a stale revision returns 409 with latest"), "delete": summary("Delete an agent team")},
		"/api/v1/agent-teams/{id}/revisions":       {"get": summary("List an agent team's last 20 revisions")},
		"/api/v1/team-runs":                        {"get": summary("List team runs, optionally by team_id"), "post": summary("Start a team run from an immutable snapshot of the team")},
		"/api/v1/team-runs/{id}":                   {"get": summary("Read a team run, its usage and sandbox availability")},
		"/api/v1/team-runs/{id}/events":            {"get": summary("Poll a team run's redacted event log after a sequence number")},
		"/api/v1/team-runs/{id}/events/stream":     {"get": summary("Stream a team run's event log as Server-Sent Events")},
		"/api/v1/team-runs/{id}/workers":           {"get": summary("List a team run's workers")},
		"/api/v1/team-runs/{id}/workers/{wid}":     {"get": summary("Read a worker with its redacted transcript")},
		"/api/v1/team-runs/{id}/messages":          {"post": summary("Send a person's message to a worker by ID or unique name")},
		"/api/v1/team-runs/{id}/cancel":            {"post": summary("Cancel a team run and its live workers")},
		"/api/v1/team-runs/{id}/resume":            {"post": summary("Requeue failed workers after an operator fix")},
	} {
		openapiPaths[route] = ops
	}
}
