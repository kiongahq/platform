package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/kiongahq/platform/internal/agentteam"
	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/redact"
	"github.com/kiongahq/platform/internal/store"
)

// Tool dispatch. A call runs only if its name is in the list derived from the run
// snapshot, the starter still has access to the resource, and, for side effects,
// a person approved it. started_at is committed under the lease before anything
// executes, which is what makes the crash rule hold.

var gitRef = regexp.MustCompile(`^[A-Za-z0-9_]+([./-][A-Za-z0-9_]+)*$`)

// toolOutcome is one call's result. failed marks it for the model and the console.
type toolOutcome struct {
	content string
	failed  bool
	code    string
}

func toolFailure(code, message string) toolOutcome {
	return toolOutcome{content: agentteam.ErrorResult(code, message), failed: true, code: code}
}

func toolJSON(value any) toolOutcome {
	raw, err := json.Marshal(value)
	if err != nil {
		return toolFailure("internal_error", "The result could not be encoded.")
	}
	return toolOutcome{content: string(raw)}
}

// settleCall stores a result that never executed anything.
func (e *teamEngine) settleCall(run agentteam.Run, worker agentteam.Worker, lease *teamLease, call agentteam.ToolCall, outcome toolOutcome) (agentteam.Worker, bool) {
	content := agentteam.Cap(redact.String(outcome.content), agentteam.MaxToolOutput)
	saved, err := e.apply(lease, func(current *agentteam.Worker) error {
		agentteam.ApplyResult(current, call.ID, content, randomToken(), outcome.failed)
		return nil
	})
	if err != nil {
		return worker, false
	}
	event := teamEvent{Type: "worker.tool_result", RunID: run.ID, WorkerID: saved.ID, Agent: saved.Agent, Tool: eventToolName(call.Name), ToolCallID: call.ID, OutputPreview: preview(content)}
	if outcome.failed {
		event.Error = eventError(outcome.code, outcome.code)
	}
	appendTeamEvent(e.docs(), event)
	return saved, true
}

var eventToolNameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// eventToolName fits a model-chosen name to the contract's pattern. An unknown
// tool name comes straight from the model.
func eventToolName(name string) string {
	name = eventToolNameUnsafe.ReplaceAllString(name, "_")
	if name == "" {
		return "unknown"
	}
	if len(name) > 64 {
		return name[:64]
	}
	return name
}

func (e *teamEngine) toolStep(ctx context.Context, run agentteam.Run, worker agentteam.Worker, lease *teamLease, starter auth.Principal) (agentteam.Worker, bool) {
	call, ok := agentteam.PendingCall(worker)
	if !ok {
		return worker, true
	}
	tool, granted := agentteam.FindTool(agentteam.ToolsFor(run.Snapshot, worker.Role, worker.Agent), call.Name)
	if !granted {
		return e.settleCall(run, worker, lease, call, toolFailure(agentteam.ErrUnknownTool, "You do not have this tool."))
	}
	var arguments map[string]any
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil || arguments == nil {
		return e.settleCall(run, worker, lease, call, toolFailure(agentteam.ErrInvalidArguments, "Tool arguments must be a JSON object."))
	}
	if call.Name == agentteam.ToolFinish {
		return e.finishTool(run, worker, lease, call, arguments)
	}
	if !e.grantHolds(starter, run, tool) {
		return e.settleCall(run, worker, lease, call, toolFailure(agentteam.ErrAccessDenied, "The person who started this run no longer has access to this resource."))
	}
	if tool.Class == agentteam.ClassSideEffect {
		if !run.AllowSideEffects {
			return e.settleCall(run, worker, lease, call, toolFailure(agentteam.ErrSideEffectsNotApproved, "This run does not allow side effects, so nothing ran."))
		}
		if worker, wait, done := e.gateApproval(run, worker, lease, call); wait || done {
			return worker, !wait
		}
	}
	started := e.now()
	marked, err := e.apply(lease, func(current *agentteam.Worker) error {
		if !agentteam.MarkStarted(current, call.ID, started) {
			return store.ErrNotFound
		}
		return nil
	})
	// Not marked means not leased: this call must not run.
	if err != nil {
		return worker, false
	}
	worker = marked
	appendTeamEvent(e.docs(), teamEvent{Type: "worker.tool_call", RunID: run.ID, WorkerID: worker.ID, Agent: worker.Agent, Tool: call.Name, ToolCallID: call.ID, InputPreview: preview(string(redactArguments(call.Arguments)))})
	outcome := e.execute(ctx, run, worker, lease, call, tool, arguments, starter)
	// A cancelled context leaves the call pending with started_at set, and the next
	// holder applies the crash rule. Nothing can claim it finished.
	if ctx.Err() != nil {
		return worker, false
	}
	content := agentteam.Cap(redact.String(outcome.content), agentteam.MaxToolOutput)
	saved, err := e.apply(lease, func(current *agentteam.Worker) error {
		agentteam.ApplyResult(current, call.ID, content, randomToken(), outcome.failed)
		return nil
	})
	if err != nil {
		return worker, false
	}
	duration := e.now().Sub(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	event := teamEvent{Type: "worker.tool_result", RunID: run.ID, WorkerID: saved.ID, Agent: saved.Agent, Tool: call.Name, ToolCallID: call.ID, DurationMS: &duration, OutputPreview: preview(content)}
	if outcome.failed {
		event.Error = eventError(outcome.code, outcome.code)
	}
	appendTeamEvent(e.docs(), event)
	return saved, true
}

// gateApproval holds a side-effect call until a person decides. wait parks the
// worker; done means a rejection or expiry was stored as the result. Neither
// means the approval is granted. The ID is derived from the call, so a resume
// finds the same approval rather than asking twice.
func (e *teamEngine) gateApproval(run agentteam.Run, worker agentteam.Worker, lease *teamLease, call agentteam.ToolCall) (saved agentteam.Worker, wait, done bool) {
	id := hashedID("apr-", run.ID, worker.ID, call.ID)
	now := e.now()
	item := agentApproval{ID: id, ProjectID: run.ProjectID, RunID: run.ID, NodeID: call.ID, WorkerID: worker.ID, ToolName: call.Name,
		Message: previewText(fmt.Sprintf("%s wants to run %s with %s", worker.Agent, call.Name, redactArguments(call.Arguments)), 2000),
		Source:  "team_run", RequestedBy: "team-run:" + run.ID, Status: "pending", CreatedAt: now, ExpiresAt: run.Deadline()}
	approval, err := store.UpdateDoc(e.docs(), agentApprovalKind, id, func(current agentApproval, exists bool) (agentApproval, error) {
		if exists {
			return current, store.ErrSkipWrite
		}
		return item, nil
	}, "agent_approval.requested", "team-run:"+run.ID)
	if err != nil {
		return worker, true, false
	}
	switch approval.view().Status {
	case "approved":
		return worker, false, false
	case "rejected":
		message := "A person rejected this call; it did not run."
		if approval.Comment != "" {
			message += " Comment: " + approval.Comment
		}
		saved, proceed := e.settleCall(run, worker, lease, call, toolFailure(agentteam.ErrApprovalRejected, message))
		return saved, !proceed, proceed
	case "expired":
		saved, proceed := e.settleCall(run, worker, lease, call, toolFailure(agentteam.ErrApprovalExpired, "The approval request expired; the call did not run."))
		return saved, !proceed, proceed
	}
	first := call.ApprovalID == ""
	saved, err = e.apply(lease, func(current *agentteam.Worker) error {
		agentteam.WaitForApproval(current, call.ID, id)
		return nil
	})
	if err != nil {
		return worker, true, false
	}
	if first {
		appendTeamEvent(e.docs(), teamEvent{Type: "worker.waiting_approval", RunID: run.ID, WorkerID: saved.ID, Agent: saved.Agent, Status: agentteam.WorkerWaitingApproval, Tool: call.Name, ToolCallID: call.ID, ApprovalID: id, InputPreview: preview(string(redactArguments(call.Arguments)))})
	}
	return saved, true, false
}

// grantHolds filters a snapshot grant by the starter's access at call time.
func (e *teamEngine) grantHolds(starter auth.Principal, run agentteam.Run, tool agentteam.Tool) bool {
	switch tool.Kind {
	case "tool", "connection", "knowledge", "dataset":
		return e.s.resourceCheckerFor(starter, run.ProjectID)(grantResourceKind(tool.Kind), tool.Ref)
	}
	return true
}

func stringArgument(arguments map[string]any, name string) string {
	value, _ := arguments[name].(string)
	return value
}

func (e *teamEngine) execute(ctx context.Context, run agentteam.Run, worker agentteam.Worker, lease *teamLease, call agentteam.ToolCall, tool agentteam.Tool, arguments map[string]any, starter auth.Principal) toolOutcome {
	switch tool.Kind {
	case "builtin":
		return e.executeBuiltin(ctx, run, worker, call, arguments)
	case "sandbox":
		return e.executeSandbox(ctx, run, worker, lease, call, arguments)
	case "git":
		return e.executeGit(ctx, run, worker, lease, starter, call, arguments)
	case "connection":
		raw, err := json.Marshal(arguments)
		if err != nil {
			return toolFailure(agentteam.ErrInvalidArguments, "The arguments could not be encoded.")
		}
		return internalCall(ctx, starter, e.s.invokeAgentAPIConnection, http.MethodPost, "/api/v1/agent-api-connections/"+tool.Ref+"/invoke", map[string]string{"id": tool.Ref}, nil, raw)
	case "knowledge":
		limit := 5
		if value, ok := arguments["limit"].(float64); ok {
			limit = int(value)
		}
		vector, ok := arguments["vector"].([]any)
		if !ok {
			return toolFailure("embedding_required", "This tool searches by embedding vector, and the gateway does not embed text.")
		}
		raw, err := json.Marshal(map[string]any{"vector": vector, "limit": limit})
		if err != nil {
			return toolFailure(agentteam.ErrInvalidArguments, "The vector could not be encoded.")
		}
		return internalCall(ctx, starter, e.s.searchKnowledge, http.MethodPost, "/api/v1/knowledge-bases/"+tool.Ref+"/search", map[string]string{"id": tool.Ref}, nil, raw)
	case "dataset":
		if key := stringArgument(arguments, "key"); key != "" {
			return internalCall(ctx, starter, e.s.datasetObject, http.MethodGet, "/api/v1/datasets/"+tool.Ref+"/object", map[string]string{"id": tool.Ref}, url.Values{"key": {key}}, nil)
		}
		return internalCall(ctx, starter, e.s.datasetObjects, http.MethodGet, "/api/v1/datasets/"+tool.Ref+"/objects", map[string]string{"id": tool.Ref}, url.Values{"prefix": {stringArgument(arguments, "prefix")}}, nil)
	case "tool":
		// The tool catalog stores metadata and has no execution endpoint yet.
		return toolFailure("tool_not_executable", "This registered tool has no execution endpoint on the gateway.")
	}
	return toolFailure(agentteam.ErrUnknownTool, "You do not have this tool.")
}

// bufferedResponse captures an existing handler's reply.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }
func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}
func (b *bufferedResponse) Write(raw []byte) (int, error) {
	if b.status == 0 {
		b.status = 200
	}
	if b.body.Len() > 2*agentteam.MaxToolOutput {
		return len(raw), nil
	}
	return b.body.Write(raw)
}

// internalCall runs an existing handler as the starter, so authorization, project
// scope and secret rules are the ones the API already enforces, with no second copy.
func internalCall(ctx context.Context, starter auth.Principal, handler http.HandlerFunc, method, path string, values map[string]string, query url.Values, body []byte) toolOutcome {
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(auth.WithPrincipal(ctx, starter), method, target, bytes.NewReader(body))
	if err != nil {
		return toolFailure(agentteam.ErrInvalidArguments, "The request could not be built.")
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range values {
		request.SetPathValue(name, value)
	}
	response := &bufferedResponse{header: http.Header{}}
	handler(response, request)
	if response.status >= 400 {
		failure := struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}{}
		_ = json.Unmarshal(response.body.Bytes(), &failure)
		if failure.Error == "" {
			failure.Error = "tool_failed"
		}
		return toolFailure(failure.Error, failure.Message)
	}
	return toolOutcome{content: response.body.String()}
}

func (e *teamEngine) executeBuiltin(ctx context.Context, run agentteam.Run, worker agentteam.Worker, call agentteam.ToolCall, arguments map[string]any) toolOutcome {
	switch call.Name {
	case agentteam.ToolSpawnWorker:
		return e.spawnWorker(run, stringArgument(arguments, "agent"), stringArgument(arguments, "task"))
	case agentteam.ToolSendMessage:
		return e.sendMessage(run, worker, call, stringArgument(arguments, "to"), stringArgument(arguments, "text"))
	case agentteam.ToolListWorkers:
		current, err := store.GetDoc[agentteam.Run](e.docs(), teamRunKind, run.ID)
		if err != nil {
			return toolFailure("unavailable", "The run could not be read.")
		}
		items := []map[string]string{}
		for _, item := range teamWorkers(e.docs(), current) {
			if item.Role != agentteam.RoleCoordinator {
				items = append(items, map[string]string{"id": item.ID, "name": item.Agent, "status": item.Status, "last_error": item.LastError})
			}
		}
		return toolJSON(map[string]any{"workers": items})
	case agentteam.ToolReadWorkerFile, agentteam.ToolWorkerDiff:
		current, err := store.GetDoc[agentteam.Run](e.docs(), teamRunKind, run.ID)
		if err != nil {
			return toolFailure("unavailable", "The run could not be read.")
		}
		target, code := agentteam.ResolveTarget(stringArgument(arguments, "worker"), teamWorkers(e.docs(), current))
		if code != "" {
			return toolFailure(code, "Use a worker ID, or a name that matches exactly one worker.")
		}
		if target.SandboxID == "" {
			return toolFailure(agentteam.ErrSandboxUnavailable, "This worker has no sandbox.")
		}
		if call.Name == agentteam.ToolWorkerDiff {
			diff, err := e.s.teamSandbox().Diff(ctx, target.SandboxID)
			if err != nil {
				return sandboxFailure(err)
			}
			return toolOutcome{content: diff.Diff}
		}
		return e.readFile(ctx, target.SandboxID, stringArgument(arguments, "path"))
	}
	return toolFailure(agentteam.ErrUnknownTool, "You do not have this tool.")
}

// spawnWorker reserves the worker on the run under its row lock, so
// max_workers_total holds across replicas, then writes the worker document.
func (e *teamEngine) spawnWorker(run agentteam.Run, agent, task string) toolOutcome {
	member, ok := run.Snapshot.Member(agent)
	if !ok {
		return toolFailure(agentteam.ErrUnknownWorker, "This agent is not a member of the team.")
	}
	if strings.TrimSpace(task) == "" || utf8.RuneCountInString(task) > 32000 {
		return toolFailure(agentteam.ErrInvalidArguments, "A task is required, at most 32000 characters.")
	}
	id, err := newOpaqueID("atw-")
	if err != nil {
		return toolFailure("internal_error", "A worker ID could not be created.")
	}
	limit := run.Snapshot.Team.Limits.MaxWorkersTotal
	refused := false
	_, err = store.UpdateDoc(e.docs(), teamRunKind, run.ID, func(current agentteam.Run, exists bool) (agentteam.Run, error) {
		if !exists || current.Status != agentteam.RunRunning {
			refused = true
			return current, store.ErrSkipWrite
		}
		if len(current.WorkerIDs)-1 >= limit {
			refused = true
			return current, store.ErrSkipWrite
		}
		current.WorkerIDs = append(current.WorkerIDs, id)
		current.UpdatedAt = e.now()
		return current, nil
	}, "", "")
	if err != nil {
		return toolFailure("unavailable", "The worker could not be started.")
	}
	if refused {
		return toolFailure(agentteam.ErrMaxWorkers, fmt.Sprintf("This team may start at most %d workers.", limit))
	}
	now := e.now()
	text := redact.String(task)
	worker := agentteam.Worker{ID: id, RunID: run.ID, ProjectID: run.ProjectID, Role: agentteam.RoleWorker, Agent: member.Name, Task: text, Status: agentteam.WorkerQueued, Transcript: []agentteam.Message{}, CreatedAt: now, UpdatedAt: now}
	if err := agentteam.AddInbox(&worker, agentteam.InboxMessage{ID: id + ":task", Source: "coordinator", Kind: "task", Body: text}); err != nil {
		return toolFailure("internal_error", "The task could not be queued.")
	}
	if _, err := store.UpdateDoc(e.docs(), teamWorkerKind, id, func(_ agentteam.Worker, _ bool) (agentteam.Worker, error) { return worker, nil }, "", ""); err != nil {
		return toolFailure("unavailable", "The worker could not be started.")
	}
	appendTeamEvent(e.docs(), teamEvent{Type: "worker.spawned", RunID: run.ID, WorkerID: id, Agent: member.Name, Role: agentteam.RoleWorker, Status: agentteam.WorkerQueued, InputPreview: preview(text)})
	kickTeamScheduler()
	return toolJSON(map[string]string{"id": id, "name": member.Name, "status": agentteam.WorkerQueued})
}

func (e *teamEngine) sendMessage(run agentteam.Run, from agentteam.Worker, call agentteam.ToolCall, to, text string) toolOutcome {
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 32000 {
		return toolFailure(agentteam.ErrInvalidArguments, "Text is required, at most 32000 characters.")
	}
	current, err := store.GetDoc[agentteam.Run](e.docs(), teamRunKind, run.ID)
	if err != nil {
		return toolFailure("unavailable", "The run could not be read.")
	}
	target, code := agentteam.ResolveTarget(to, teamWorkers(e.docs(), current))
	if code == agentteam.ErrAmbiguousWorker {
		return toolFailure(code, "More than one worker has this name; use the worker ID.")
	}
	if code != "" {
		return toolFailure(code, "No worker matches this address.")
	}
	if !agentteam.MessageAllowed(from, target) {
		return toolFailure(agentteam.ErrNotPermitted, "A worker may message only the coordinator.")
	}
	source := "coordinator"
	if from.Role != agentteam.RoleCoordinator {
		source = "worker:" + from.Agent
	}
	body := redact.String(text)
	_, err = store.UpdateDoc(e.docs(), teamWorkerKind, target.ID, func(item agentteam.Worker, exists bool) (agentteam.Worker, error) {
		if !exists {
			return item, store.ErrNotFound
		}
		if err := agentteam.AddInbox(&item, agentteam.InboxMessage{ID: from.ID + ":" + call.ID, Source: source, Kind: "message", Body: body}); err != nil {
			return item, err
		}
		item.UpdatedAt = e.now()
		return item, nil
	}, "", "")
	if errors.Is(err, agentteam.ErrWorkerClosed) {
		return toolFailure("worker_closed", "This worker cannot receive messages.")
	}
	if err != nil {
		return toolFailure("unavailable", "The message could not be delivered.")
	}
	appendTeamEvent(e.docs(), teamEvent{Type: "worker.message", RunID: run.ID, From: from.ID, To: target.ID, WorkerID: from.ID, Agent: from.Agent, MessagePreview: preview(body)})
	kickTeamScheduler()
	return toolJSON(map[string]string{"delivered_to": target.ID, "status": "delivered"})
}

// finishTool commits the result and the status change in one write, so finish is
// never left pending across a crash.
func (e *teamEngine) finishTool(run agentteam.Run, worker agentteam.Worker, lease *teamLease, call agentteam.ToolCall, arguments map[string]any) (agentteam.Worker, bool) {
	report := agentteam.Cap(redact.String(stringArgument(arguments, "report")), agentteam.MaxToolOutput)
	if strings.TrimSpace(report) == "" {
		return e.settleCall(run, worker, lease, call, toolFailure(agentteam.ErrInvalidArguments, "A report is required."))
	}
	if worker.Role == agentteam.RoleCoordinator {
		current, err := store.GetDoc[agentteam.Run](e.docs(), teamRunKind, run.ID)
		if err != nil {
			return worker, false
		}
		if live := agentteam.LiveWorkers(teamWorkers(e.docs(), current)); live > 0 {
			return e.settleCall(run, worker, lease, call, toolFailure(agentteam.ErrWorkersLive, fmt.Sprintf("%d workers are still live; the run cannot finish yet.", live)))
		}
	}
	refused := false
	saved, err := e.apply(lease, func(current *agentteam.Worker) error {
		if len(current.Inbox) > 0 {
			refused = true
			return nil
		}
		agentteam.ApplyResult(current, call.ID, `{"status":"finished"}`, randomToken(), false)
		agentteam.Complete(current, report)
		return nil
	})
	if err != nil {
		return worker, false
	}
	if refused {
		return e.settleCall(run, saved, lease, call, toolFailure(agentteam.ErrWorkersLive, "New messages arrived; read them before you finish."))
	}
	appendTeamEvent(e.docs(), teamEvent{Type: "worker.finished", RunID: run.ID, WorkerID: saved.ID, Agent: saved.Agent, Role: saved.Role, Status: agentteam.WorkerCompleted, OutputPreview: preview(report)})
	if saved.Role == agentteam.RoleCoordinator {
		e.finishRun(run.ID, report)
	} else {
		e.notifyCoordinator(run, saved)
	}
	return saved, false
}

// sandboxFailure maps a manager error to a tool result. Only the manager's own
// code and redacted message pass through.
func sandboxFailure(err error) toolOutcome {
	var failure *sandboxError
	if errors.As(err, &failure) {
		return toolFailure(failure.Code, failure.Message)
	}
	return toolFailure(agentteam.ErrSandboxUnavailable, "The sandbox could not be reached.")
}

// ensureSandbox creates a worker's sandbox on first use. Create is idempotent on
// run and worker, so a crash before the ID is stored finds the same sandbox.
func (e *teamEngine) ensureSandbox(ctx context.Context, run agentteam.Run, worker agentteam.Worker, lease *teamLease) (string, error) {
	if worker.SandboxID != "" {
		return worker.SandboxID, nil
	}
	id, err := e.s.teamSandbox().Create(ctx, run.ID, worker.ID, run.ProjectID)
	if err != nil {
		return "", err
	}
	if _, err := e.apply(lease, func(current *agentteam.Worker) error {
		current.SandboxID = id
		return nil
	}); err != nil {
		return "", &sandboxError{Code: agentteam.ErrSandboxUnavailable, Message: "The sandbox could not be recorded."}
	}
	return id, nil
}

// safePath rejects anything the manager's schema would reject, before the call.
func safePath(path string) bool {
	if path == "" || len(path) > 4096 || strings.ContainsRune(path, 0) {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return false
		}
	}
	return !strings.HasPrefix(path, "/") || path == "/workspace" || strings.HasPrefix(path, "/workspace/")
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }

func (e *teamEngine) readFile(ctx context.Context, sandboxID, path string) toolOutcome {
	if !safePath(path) {
		return toolFailure("path_rejected", "Use a path under /workspace.")
	}
	files, err := e.s.teamSandbox().Files(ctx, sandboxID, path)
	if err != nil {
		return sandboxFailure(err)
	}
	if files.Content == nil {
		return toolFailure("path_rejected", "This path is a directory; use list_files.")
	}
	content := *files.Content
	if files.Truncated {
		content += "\n...[truncated]..."
	}
	return toolOutcome{content: content}
}

func (e *teamEngine) executeSandbox(ctx context.Context, run agentteam.Run, worker agentteam.Worker, lease *teamLease, call agentteam.ToolCall, arguments map[string]any) toolOutcome {
	id, err := e.ensureSandbox(ctx, run, worker, lease)
	if err != nil {
		return sandboxFailure(err)
	}
	sandbox := e.s.teamSandbox()
	path := stringArgument(arguments, "path")
	switch call.Name {
	case agentteam.ToolExec:
		request := sandboxExecRequest{Command: stringArgument(arguments, "command"), Cwd: stringArgument(arguments, "cwd")}
		if value, ok := arguments["timeout_seconds"].(float64); ok {
			request.TimeoutSeconds = int(value)
		}
		if strings.TrimSpace(request.Command) == "" || request.TimeoutSeconds < 0 || request.TimeoutSeconds > 1800 || (request.Cwd != "" && !safePath(request.Cwd)) {
			return toolFailure(agentteam.ErrInvalidArguments, "exec needs a command, a timeout of at most 1800 seconds and a cwd under /workspace.")
		}
		out, err := sandbox.Exec(ctx, id, request)
		if err != nil {
			return sandboxFailure(err)
		}
		return toolJSON(out)
	case agentteam.ToolReadFile:
		return e.readFile(ctx, id, path)
	case agentteam.ToolWriteFile:
		if !safePath(path) {
			return toolFailure("path_rejected", "Use a path under /workspace.")
		}
		content := stringArgument(arguments, "content")
		if err := sandbox.Write(ctx, id, path, content); err != nil {
			return sandboxFailure(err)
		}
		return toolJSON(map[string]any{"path": path, "bytes_written": len(content)})
	case agentteam.ToolEditFile:
		if !safePath(path) {
			return toolFailure("path_rejected", "Use a path under /workspace.")
		}
		old, replacement := stringArgument(arguments, "old"), stringArgument(arguments, "new")
		if old == "" {
			return toolFailure(agentteam.ErrInvalidArguments, "old must not be empty.")
		}
		files, err := sandbox.Files(ctx, id, path)
		if err != nil {
			return sandboxFailure(err)
		}
		if files.Content == nil {
			return toolFailure("path_rejected", "This path is a directory.")
		}
		if files.Truncated {
			return toolFailure("file_too_large", "This file is too large to edit this way.")
		}
		count := strings.Count(*files.Content, old)
		if count != 1 {
			return toolFailure("no_unique_match", fmt.Sprintf("old matched %d times; it must match exactly once.", count))
		}
		if err := sandbox.Write(ctx, id, path, strings.Replace(*files.Content, old, replacement, 1)); err != nil {
			return sandboxFailure(err)
		}
		return toolJSON(map[string]string{"path": path, "status": "edited"})
	case agentteam.ToolListFiles:
		if !safePath(path) {
			return toolFailure("path_rejected", "Use a path under /workspace.")
		}
		files, err := sandbox.Files(ctx, id, path)
		if err != nil {
			return sandboxFailure(err)
		}
		if files.Content != nil {
			return toolFailure("path_rejected", "This path is a file; use read_file.")
		}
		return toolJSON(map[string]any{"entries": files.Entries, "truncated": files.Truncated})
	case agentteam.ToolSearchFiles:
		pattern := stringArgument(arguments, "pattern")
		if path == "" {
			path = "."
		}
		if pattern == "" || !safePath(path) {
			return toolFailure(agentteam.ErrInvalidArguments, "search_files needs a pattern and a path under /workspace.")
		}
		out, err := sandbox.Exec(ctx, id, sandboxExecRequest{Command: "grep -rnIE -e " + shellQuote(pattern) + " -- " + shellQuote(path), TimeoutSeconds: 120})
		if err != nil {
			return sandboxFailure(err)
		}
		return toolJSON(map[string]any{"matches": out.Stdout, "truncated": out.Truncated})
	}
	return toolFailure(agentteam.ErrUnknownTool, "You do not have this tool.")
}

// gitCredential reads a connection's secret for one call. It goes to the sandbox
// manager and nowhere else: it is never stored, logged or returned.
func (e *teamEngine) gitCredential(starter auth.Principal, run agentteam.Run, grant agentteam.ToolGrant) (string, *toolOutcome) {
	if grant.Connection == "" {
		return "", nil
	}
	denied := toolFailure(agentteam.ErrAccessDenied, "The connection for this repository is not available.")
	if !e.s.resourceCheckerFor(starter, run.ProjectID)("connection", grant.Connection) {
		return "", &denied
	}
	connection, err := store.GetDoc[agentAPIConnection](e.docs(), agentConnectionKind, grant.Connection)
	if err != nil {
		return "", &denied
	}
	if connection.SecretRef == "none" {
		return "", nil
	}
	secret := connectionSecrets.Value(connection.SecretRef)
	if secret == "" {
		unavailable := toolFailure("credential_unavailable", "The connection's credential is not available.")
		return "", &unavailable
	}
	return secret, nil
}

func (e *teamEngine) executeGit(ctx context.Context, run agentteam.Run, worker agentteam.Worker, lease *teamLease, starter auth.Principal, call agentteam.ToolCall, arguments map[string]any) toolOutcome {
	member, _ := run.Snapshot.Member(worker.Agent)
	grants := []agentteam.ToolGrant{}
	for _, grant := range member.Tools {
		if grant.Kind == "git" {
			grants = append(grants, grant)
		}
	}
	findGrant := func(repository string) (agentteam.ToolGrant, bool) {
		if repository == "" && len(grants) == 1 {
			return grants[0], true
		}
		for _, grant := range grants {
			if grant.Repository == repository {
				return grant, true
			}
		}
		return agentteam.ToolGrant{}, false
	}
	id, err := e.ensureSandbox(ctx, run, worker, lease)
	if err != nil {
		return sandboxFailure(err)
	}
	sandbox := e.s.teamSandbox()
	switch call.Name {
	case agentteam.ToolGitDiff:
		diff, err := sandbox.Diff(ctx, id)
		if err != nil {
			return sandboxFailure(err)
		}
		return toolJSON(diff)
	case agentteam.ToolGitClone:
		grant, ok := findGrant(stringArgument(arguments, "repository"))
		if !ok {
			return toolFailure(agentteam.ErrNotPermitted, "This repository is not granted to you.")
		}
		ref := stringArgument(arguments, "ref")
		if ref != "" && !gitRef.MatchString(ref) {
			return toolFailure(agentteam.ErrInvalidArguments, "ref is not valid.")
		}
		credential, failure := e.gitCredential(starter, run, grant)
		if failure != nil {
			return *failure
		}
		commit, err := sandbox.Clone(ctx, id, grant.Repository, ref, credential)
		if err != nil {
			return sandboxFailure(err)
		}
		return toolJSON(map[string]string{"repository": grant.Repository, "commit": commit})
	case agentteam.ToolGitPush:
		grant, ok := findGrant(stringArgument(arguments, "repository"))
		if !ok {
			return toolFailure(agentteam.ErrNotPermitted, "This repository is not granted to you. With several grants, name the repository.")
		}
		branch, valid := agentteam.BranchName(run.ID, stringArgument(arguments, "branch"))
		if !valid {
			return toolFailure("branch_rejected", "Give a short branch name without / or ..; the platform places it under kionga/<run-id>/.")
		}
		credential, failure := e.gitCredential(starter, run, grant)
		if failure != nil {
			return *failure
		}
		if credential == "" {
			return toolFailure("credential_unavailable", "This repository grant has no connection, so it cannot push.")
		}
		pushed, err := sandbox.Push(ctx, id, grant.Repository, branch, credential)
		if err != nil {
			return sandboxFailure(err)
		}
		return toolJSON(pushed)
	}
	return toolFailure(agentteam.ErrUnknownTool, "You do not have this tool.")
}
