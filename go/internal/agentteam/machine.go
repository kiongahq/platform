package agentteam

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var shortBranch = regexp.MustCompile(`^[A-Za-z0-9]+([._-][A-Za-z0-9]+)*$`)

// Retry timing.
const (
	BackoffMin          = 5 * time.Second
	BackoffMax          = 5 * time.Minute
	MaxRetryAfter       = time.Hour
	ProviderFailureSpan = 30 * time.Minute
	// CoordinatorMaxTurns bounds a coordinator, which has no definition limits.
	CoordinatorMaxTurns = 500
	// MaxTranscriptBytes bounds a worker document. A transcript this large has
	// long outgrown any model context.
	MaxTranscriptBytes = 16 << 20
)

// Step is what a worker holding a lease does next.
type Step int

const (
	// StepDeliver moves the inbox into the transcript.
	StepDeliver Step = iota
	// StepModel calls the runner.
	StepModel
	// StepTool executes the first unfinished call.
	StepTool
	// StepEnd means the worker has nothing to do: the model answered without a call.
	StepEnd
)

// PendingCall returns the first unfinished call, in transcript order.
func PendingCall(w Worker) (ToolCall, bool) {
	for _, message := range w.Transcript {
		for _, call := range message.ToolCalls {
			if call.Status == CallPending || call.Status == CallWaitingApproval {
				return call, true
			}
		}
	}
	return ToolCall{}, false
}

// NextStep is the turn loop: finish tool calls, then deliver the inbox, then
// call the model. Tool results therefore always directly follow their call.
func NextStep(w Worker) Step {
	if _, ok := PendingCall(w); ok {
		return StepTool
	}
	if len(w.Inbox) > 0 {
		return StepDeliver
	}
	if n := len(w.Transcript); n > 0 && w.Transcript[n-1].Role != "assistant" {
		return StepModel
	}
	return StepEnd
}

// Deliver frames the inbox into the transcript with a fresh nonce per message.
func Deliver(w *Worker, nonce func() string) {
	for _, message := range w.Inbox {
		w.Transcript = append(w.Transcript, Message{Role: "user", Content: Frame(message.Source, message.Kind, nonce(), message.Body)})
	}
	w.Inbox = nil
}

// Errors from AddInbox.
var (
	ErrWorkerClosed = errors.New("worker cannot receive messages")
)

// AddInbox queues a message, dropping a repeat of an ID already seen so a
// re-driven delivery cannot arrive twice. An idle coordinator and a completed
// worker wake up; this is the only thing that wakes them.
func AddInbox(w *Worker, message InboxMessage) error {
	if w.Status == WorkerFailed || w.Status == WorkerCancelled || (w.Status == WorkerCompleted && w.Role == RoleCoordinator) {
		return ErrWorkerClosed
	}
	for _, seen := range w.InboxSeen {
		if seen == message.ID {
			return nil
		}
	}
	w.InboxSeen = append(w.InboxSeen, message.ID)
	w.Inbox = append(w.Inbox, message)
	if w.Status == WorkerIdle || w.Status == WorkerCompleted {
		w.Status = WorkerQueued
		w.NextAttemptAt = nil
	}
	return nil
}

// ModelCall is a tool call as the runner returned it.
type ModelCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ModelReply is a successful runner response.
type ModelReply struct {
	Content   string
	ToolCalls []ModelCall
	Usage     Usage
}

// ApplyReply records a model turn and its call intents. Call IDs are assigned
// here rather than taken from the model, so they are unique across the
// transcript and an approval keyed by call ID cannot be reused.
func ApplyReply(w *Worker, reply ModelReply) {
	w.Turns++
	w.Usage = w.Usage.Add(reply.Usage)
	w.RetryAttempts, w.FailingSince, w.NextAttemptAt = 0, nil, nil
	message := Message{Role: "assistant", Content: reply.Content}
	for i, call := range reply.ToolCalls {
		arguments := call.Arguments
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		message.ToolCalls = append(message.ToolCalls, ToolCall{ID: fmt.Sprintf("call_%d_%d", w.Turns, i), Name: call.Name, Arguments: arguments, Status: CallPending})
	}
	w.Transcript = append(w.Transcript, message)
}

func updateCall(w *Worker, id string, update func(*ToolCall)) bool {
	for i := range w.Transcript {
		for j := range w.Transcript[i].ToolCalls {
			if w.Transcript[i].ToolCalls[j].ID == id {
				update(&w.Transcript[i].ToolCalls[j])
				return true
			}
		}
	}
	return false
}

// MarkStarted records that execution is about to begin. The gateway commits
// this before it runs anything, which is what the crash rule depends on.
func MarkStarted(w *Worker, id string, now time.Time) bool {
	return updateCall(w, id, func(call *ToolCall) { started := now; call.StartedAt = &started; call.Status = CallPending })
}

// WaitForApproval parks a call and the worker on a person's decision.
func WaitForApproval(w *Worker, id, approvalID string) bool {
	ok := updateCall(w, id, func(call *ToolCall) { call.Status, call.ApprovalID = CallWaitingApproval, approvalID })
	if ok {
		w.Status = WorkerWaitingApproval
	}
	return ok
}

// ApplyResult settles a call and appends its framed result.
func ApplyResult(w *Worker, id, content, nonce string, failed bool) bool {
	var name string
	ok := updateCall(w, id, func(call *ToolCall) {
		name = call.Name
		call.Status = CallDone
		if failed {
			call.Status = CallError
		}
	})
	if ok {
		w.Transcript = append(w.Transcript, Message{Role: "tool", ToolCallID: id, Content: Frame("tool:"+name, "tool-result", nonce, content)})
	}
	return ok
}

// ErrorResult is the JSON body of a failed tool call.
func ErrorResult(code, message string) string {
	raw, _ := json.Marshal(map[string]string{"error": code, "message": message})
	return string(raw)
}

// RecoverInterrupted applies the crash rule when a lease is taken. A call with
// started_at and no result may have happened. Only read-only tools run again; anything else gets outcome_unknown and is never executed blindly. An
// unknown tool name takes the safe branch.
func RecoverInterrupted(w *Worker, class func(name string) (Class, bool), nonce func() string) (rerun, unknown []string) {
	type interrupted struct{ id, name string }
	calls := []interrupted{}
	for _, message := range w.Transcript {
		for _, call := range message.ToolCalls {
			if call.Status == CallPending && call.StartedAt != nil {
				calls = append(calls, interrupted{call.ID, call.Name})
			}
		}
	}
	for _, call := range calls {
		kind, known := class(call.name)
		if known && Rerunnable(kind) {
			updateCall(w, call.id, func(item *ToolCall) { item.StartedAt = nil })
			rerun = append(rerun, call.id)
			continue
		}
		ApplyResult(w, call.id, ErrorResult(ErrOutcomeUnknown, "The gateway restarted while this call was running, so it may or may not have taken effect. It was not run again. Check the current state before you retry."), nonce(), true)
		unknown = append(unknown, call.id)
	}
	return rerun, unknown
}

// Backoff is the wait before retry attempt n (from 0), doubling from 5 s to
// 5 min. jitter in [0,1) adds up to 20 %, still capped at 5 min.
func Backoff(attempt int, jitter float64) time.Duration {
	delay := BackoffMin
	for i := 0; i < attempt && delay < BackoffMax; i++ {
		delay *= 2
	}
	delay += time.Duration(float64(delay) * 0.2 * jitter)
	if delay > BackoffMax {
		return BackoffMax
	}
	return delay
}

// ApplyRateLimit sets waiting_retry. It honors the provider's value and does
// not touch Turns, so waiting never consumes max_turns.
func ApplyRateLimit(w *Worker, retryAfterSeconds int, jitter float64, now time.Time) time.Duration {
	wait := Backoff(w.RetryAttempts, jitter)
	if retryAfterSeconds > 0 {
		wait = time.Duration(retryAfterSeconds) * time.Second
		if wait > MaxRetryAfter {
			wait = MaxRetryAfter
		}
	}
	w.RetryAttempts++
	next := now.Add(wait)
	w.Status, w.NextAttemptAt = WorkerWaitingRetry, &next
	return wait
}

// ApplyProviderFailure behaves like a rate limit until failures have lasted 30
// minutes, then fails the worker with an error a resume can clear.
func ApplyProviderFailure(w *Worker, message string, jitter float64, now time.Time) bool {
	if w.FailingSince == nil {
		since := now
		w.FailingSince = &since
	}
	if now.Sub(*w.FailingSince) >= ProviderFailureSpan {
		Fail(w, ErrProviderUnavailable, message, true)
		return true
	}
	ApplyRateLimit(w, 0, jitter, now)
	return false
}

// BranchName validates git_push's short name and expands it. A model never
// supplies the run ID, and a name with "/" or ".." is refused.
func BranchName(runID, name string) (string, bool) {
	if !shortBranch.MatchString(name) || strings.Contains(name, "..") {
		return "", false
	}
	return "kionga/" + runID + "/" + name, true
}

// Fail ends a worker. The coordinator hears about it through NotifyPending.
func Fail(w *Worker, code, message string, resumable bool) {
	w.Status, w.LastError, w.LastErrorMessage, w.LastErrorResumable = WorkerFailed, code, message, resumable
	w.NextAttemptAt = nil
	w.NotifyPending = w.Role != RoleCoordinator
}

// Complete ends a worker with its report.
func Complete(w *Worker, report string) {
	w.Status, w.Report = WorkerCompleted, report
	w.LastError, w.LastErrorMessage, w.LastErrorResumable = "", "", false
	w.NextAttemptAt = nil
	w.NotifyPending = w.Role != RoleCoordinator
}

// Requeue returns a failed worker to the queue after an operator fix.
func Requeue(w *Worker) bool {
	if w.Status != WorkerFailed || !w.LastErrorResumable {
		return false
	}
	w.Status, w.LastError, w.LastErrorMessage, w.LastErrorResumable = WorkerQueued, "", "", false
	w.FailingSince, w.NextAttemptAt, w.RetryAttempts = nil, nil, 0
	w.NotifyPending = false
	return true
}

// TranscriptBytes approximates the stored size of a transcript.
func TranscriptBytes(w Worker) int {
	total := 0
	for _, message := range w.Transcript {
		total += len(message.Content)
		for _, call := range message.ToolCalls {
			total += len(call.Arguments)
		}
	}
	return total
}

// WorkerBudget checks one worker before a model call and returns a reason when
// its own limits are spent. Time waiting on retries or approvals is not counted.
func WorkerBudget(w Worker, snapshot Snapshot) string {
	if TranscriptBytes(w) > MaxTranscriptBytes {
		return "transcript limit reached"
	}
	if w.Role == RoleCoordinator {
		if w.Turns >= CoordinatorMaxTurns {
			return fmt.Sprintf("coordinator turn limit %d reached", CoordinatorMaxTurns)
		}
		return ""
	}
	member, ok := snapshot.Member(w.Agent)
	if !ok {
		return "agent is not in the run snapshot"
	}
	switch {
	case w.Turns >= member.Limits.MaxTurns:
		return fmt.Sprintf("max_turns %d reached", member.Limits.MaxTurns)
	case w.Usage.Total() >= member.Limits.MaxTokens:
		return fmt.Sprintf("max_tokens %d reached", member.Limits.MaxTokens)
	case w.ActiveMillis >= int64(member.Limits.MaxSeconds)*1000:
		return fmt.Sprintf("max_seconds %d reached", member.Limits.MaxSeconds)
	}
	return ""
}

// TeamBudget checks the run's limits and returns a reason when they are spent.
func TeamBudget(snapshot Snapshot, usage Usage, created, now time.Time) string {
	limits := snapshot.Team.Limits
	if usage.Total() >= limits.MaxTokens {
		return fmt.Sprintf("team max_tokens %d reached", limits.MaxTokens)
	}
	if now.Sub(created) >= time.Duration(limits.MaxSeconds)*time.Second {
		return fmt.Sprintf("team max_seconds %d reached", limits.MaxSeconds)
	}
	return ""
}

// ResolveTarget turns send_message's "to" into a worker: the worker id, or a name
// matching exactly one worker. A member spawned twice must be addressed by id.
func ResolveTarget(to string, workers []Worker) (Worker, string) {
	to = strings.TrimSpace(to)
	matches := []Worker{}
	for _, worker := range workers {
		if worker.ID == to {
			return worker, ""
		}
		if worker.Agent == to {
			matches = append(matches, worker)
		}
	}
	switch len(matches) {
	case 0:
		return Worker{}, ErrUnknownWorker
	case 1:
		return matches[0], ""
	}
	return Worker{}, ErrAmbiguousWorker
}

// MessageAllowed is the send_message rule: the coordinator reaches any worker
// other than itself, a worker reaches only the coordinator.
func MessageAllowed(from, to Worker) bool {
	if from.ID == to.ID {
		return false
	}
	return from.Role == RoleCoordinator || to.Role == RoleCoordinator
}

// LiveWorkers counts workers the coordinator must wait for.
func LiveWorkers(workers []Worker) int {
	live := 0
	for _, worker := range workers {
		if worker.Role != RoleCoordinator && worker.Live() {
			live++
		}
	}
	return live
}

// Eligible reports whether a scheduler may try to lease a worker now. An
// unexpired lease is never eligible. approvalDecided is whether the worker's
// parked approval has been decided or has expired.
func Eligible(w Worker, approvalDecided bool, now time.Time) bool {
	if w.LeaseOwner != "" && w.LeaseExpiresAt != nil && w.LeaseExpiresAt.After(now) {
		return false
	}
	switch w.Status {
	case WorkerQueued, WorkerRunning:
		return true
	case WorkerWaitingRetry:
		return w.NextAttemptAt == nil || !w.NextAttemptAt.After(now)
	case WorkerWaitingApproval:
		return approvalDecided
	}
	return false
}
