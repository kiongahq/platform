// Package agentteam is the pure core of Agent Teams: resource validation, the
// worker state machine, provenance framing, system prompt assembly and tool
// grants. It does no I/O and reads no clock it is not given, so every rule is
// tested as a table. The gateway (internal/httpapi/agent_teams*.go) owns
// persistence, leases, the runner and the sandbox manager.
package agentteam

import (
	"encoding/json"
	"time"
)

// Worker roles.
const (
	RoleCoordinator = "coordinator"
	RoleWorker      = "worker"
)

// Worker status.
const (
	WorkerQueued          = "queued"
	WorkerRunning         = "running"
	WorkerWaitingRetry    = "waiting_retry"
	WorkerWaitingApproval = "waiting_approval"
	WorkerIdle            = "idle"
	WorkerCompleted       = "completed"
	WorkerFailed          = "failed"
	WorkerCancelled       = "cancelled"
)

// Run status.
const (
	RunRunning    = "running"
	RunSucceeded  = "succeeded"
	RunFailed     = "failed"
	RunCancelling = "cancelling"
	RunCancelled  = "cancelled"
)

// Tool call status, as stored on a transcript item.
const (
	CallPending         = "pending"
	CallWaitingApproval = "waiting_approval"
	CallDone            = "done"
	CallError           = "error"
)

// Error codes recorded on workers, runs and tool results.
const (
	ErrBudgetExceeded         = "budget_exceeded"
	ErrOwnerAccessLost        = "owner_access_lost"
	ErrOutcomeUnknown         = "outcome_unknown"
	ErrSideEffectsNotApproved = "side_effects_not_approved"
	ErrApprovalRejected       = "approval_rejected"
	ErrApprovalExpired        = "approval_expired"
	ErrSandboxUnavailable     = "sandbox_unavailable"
	ErrProviderUnavailable    = "provider_unavailable"
	ErrTranscriptTooLarge     = "transcript_too_large"
	ErrUnknownTool            = "unknown_tool"
	ErrAccessDenied           = "access_denied"
	ErrAmbiguousWorker        = "ambiguous_worker"
	ErrUnknownWorker          = "unknown_worker"
	ErrNotPermitted           = "not_permitted"
	ErrInvalidArguments       = "invalid_arguments"
	ErrWorkersLive            = "workers_live"
	ErrMaxWorkers             = "max_workers_exceeded"
	ErrContextExhausted       = "context_exhausted"
)

// ToolGrant is one entry in a definition's tools list.
type ToolGrant struct {
	Kind       string `json:"kind"`
	Ref        string `json:"ref,omitempty"`
	Repository string `json:"repository,omitempty"`
	Connection string `json:"connection,omitempty"`
}

// DefinitionLimits bound one worker.
type DefinitionLimits struct {
	MaxTurns   int `json:"max_turns"`
	MaxTokens  int `json:"max_tokens"`
	MaxSeconds int `json:"max_seconds"`
}

// TeamLimits bound a whole run.
type TeamLimits struct {
	MaxWorkersTotal      int `json:"max_workers_total"`
	MaxConcurrentWorkers int `json:"max_concurrent_workers"`
	MaxTokens            int `json:"max_tokens"`
	MaxSeconds           int `json:"max_seconds"`
}

// DefinitionContent is the versioned part of an agent definition.
type DefinitionContent struct {
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	Instructions string           `json:"instructions"`
	Model        string           `json:"model"`
	Tools        []ToolGrant      `json:"tools"`
	Workspace    string           `json:"workspace"`
	Limits       DefinitionLimits `json:"limits"`
}

// Coordinator is a team's coordinator role.
type Coordinator struct {
	Instructions string `json:"instructions"`
	Model        string `json:"model"`
}

// TeamContent is the versioned part of a team.
type TeamContent struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Coordinator Coordinator `json:"coordinator"`
	Members     []string    `json:"members"`
	Brief       string      `json:"brief"`
	Limits      TeamLimits  `json:"limits"`
}

// MemberSnapshot is a definition as a run saw it.
type MemberSnapshot struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	DefinitionContent
}

// Snapshot is the immutable team a run executes, so later edits never change a
// running team.
type Snapshot struct {
	TeamID       string           `json:"team_id"`
	TeamRevision int              `json:"team_revision"`
	Team         TeamContent      `json:"team"`
	Members      []MemberSnapshot `json:"members"`
}

// Member returns the snapshot member with this name.
func (s Snapshot) Member(name string) (MemberSnapshot, bool) {
	for _, member := range s.Members {
		if member.Name == name {
			return member, true
		}
	}
	return MemberSnapshot{}, false
}

// UsesSandbox reports whether any member works in a sandbox.
func (s Snapshot) UsesSandbox() bool {
	for _, member := range s.Members {
		if member.Workspace == "sandbox" {
			return true
		}
	}
	return false
}

// Usage counts model tokens.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Total is the budgeted token count.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// Add returns the sum of two usages.
func (u Usage) Add(other Usage) Usage {
	return Usage{InputTokens: u.InputTokens + other.InputTokens, OutputTokens: u.OutputTokens + other.OutputTokens}
}

// Run is a team run document. Workers are separate documents so a transcript
// write never rewrites the run.
type Run struct {
	ID               string     `json:"id"`
	ProjectID        string     `json:"project_id"`
	TeamID           string     `json:"team_id"`
	Input            string     `json:"input"`
	AllowSideEffects bool       `json:"allow_side_effects"`
	IdempotencyKey   string     `json:"idempotency_key,omitempty"`
	Subject          string     `json:"subject"`
	Roles            []string   `json:"roles"`
	HadProfile       bool       `json:"had_profile,omitempty"`
	Status           string     `json:"status"`
	Error            string     `json:"error,omitempty"`
	ErrorMessage     string     `json:"error_message,omitempty"`
	Report           string     `json:"report,omitempty"`
	Usage            Usage      `json:"usage"`
	ResumeCount      int        `json:"resume_count"`
	Snapshot         Snapshot   `json:"snapshot"`
	ContentSHA256    string     `json:"content_sha256"`
	CoordinatorID    string     `json:"coordinator_id"`
	WorkerIDs        []string   `json:"worker_ids"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
	SettledAt        *time.Time `json:"settled_at,omitempty"`
}

// Terminal reports whether the run reached a final status.
func (r Run) Terminal() bool {
	return r.Status == RunSucceeded || r.Status == RunFailed || r.Status == RunCancelled
}

// Deadline is when the team's max_seconds runs out.
func (r Run) Deadline() time.Time {
	return r.CreatedAt.Add(time.Duration(r.Snapshot.Team.Limits.MaxSeconds) * time.Second)
}

// ToolCall is one tool call on an assistant message. It is an intent until
// Status leaves pending. StartedAt is written before execution, which is what
// lets a later resume know the call may already have happened.
type ToolCall struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Arguments  json.RawMessage `json:"arguments"`
	Status     string          `json:"status"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	ApprovalID string          `json:"approval_id,omitempty"`
}

// Message is one transcript item.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// InboxMessage waits for delivery at the start of a worker's next turn.
type InboxMessage struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Body   string `json:"body"`
}

// Worker is one agent in a run, including the coordinator.
type Worker struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
	Agent     string `json:"agent"`
	Task      string `json:"task,omitempty"`
	Status    string `json:"status"`

	Transcript []Message      `json:"transcript"`
	Inbox      []InboxMessage `json:"inbox"`
	InboxSeen  []string       `json:"inbox_seen,omitempty"`

	Usage        Usage `json:"usage"`
	Turns        int   `json:"turns"`
	ActiveMillis int64 `json:"active_millis"`

	// LastInputTokens is what the last model call measured. CompactedBefore is the
	// transcript index below which tool results are sent as markers, and only moves
	// forward. CompactedAtTurn stops one measurement from compacting twice.
	LastInputTokens int `json:"last_input_tokens,omitempty"`
	CompactedBefore int `json:"compacted_before,omitempty"`
	CompactedAtTurn int `json:"compacted_at_turn,omitempty"`

	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
	RetryAttempts int        `json:"retry_attempts,omitempty"`
	FailingSince  *time.Time `json:"failing_since,omitempty"`

	LeaseOwner     string     `json:"lease_owner,omitempty"`
	LeaseToken     string     `json:"lease_token,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	ActiveSince    *time.Time `json:"active_since,omitempty"`

	Report             string `json:"report,omitempty"`
	LastError          string `json:"last_error,omitempty"`
	LastErrorMessage   string `json:"last_error_message,omitempty"`
	LastErrorResumable bool   `json:"last_error_resumable,omitempty"`
	// NotifyPending says a hand-back or failure is not yet in the coordinator's
	// inbox. It is written with the status change, and a scan re-drives it, so a
	// crash between the two writes cannot strand an idle coordinator.
	NotifyPending bool   `json:"notify_pending,omitempty"`
	SandboxID     string `json:"sandbox_id,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Terminal reports whether the worker is completed, failed or cancelled. A
// completed worker can still resume when a message reaches it.
func (w Worker) Terminal() bool {
	return w.Status == WorkerCompleted || w.Status == WorkerFailed || w.Status == WorkerCancelled
}

// Live reports whether the coordinator must still wait for this worker.
func (w Worker) Live() bool { return !w.Terminal() || w.NotifyPending }
