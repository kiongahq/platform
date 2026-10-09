package api

import "time"

// PipelineRevision is an immutable snapshot of a definition. Runs record the
// revision and SHA-256 they executed; rollback creates a new revision from
// an old one rather than rewriting history.
type PipelineRevision struct {
	DefinitionID string                          `json:"definition_id"`
	Revision     int                             `json:"revision"`
	SHA256       string                          `json:"sha256"`
	YAML         string                          `json:"yaml"`
	Spec         UpsertPipelineDefinitionRequest `json:"spec"`
	Author       string                          `json:"author"`
	Message      string                          `json:"message,omitempty"`
	CreatedAt    time.Time                       `json:"created_at"`
}

// ValidationIssue is one field-level problem returned with HTTP 422.
type ValidationIssue struct {
	Path    string `json:"path"`
	Node    string `json:"node,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

// LogEntry is one structured log line or event. Sources: runner (container
// stdout/stderr), engine (orchestrator), platform (control-plane events such
// as step transitions), k8s (pod/job events), audit.
type LogEntry struct {
	Sequence     int64          `json:"sequence"`
	Timestamp    time.Time      `json:"ts"`
	ProjectID    string         `json:"project_id,omitempty"`
	PipelineID   string         `json:"pipeline_id,omitempty"`
	RunID        string         `json:"run_id,omitempty"`
	Node         string         `json:"node,omitempty"`
	Attempt      int            `json:"attempt,omitempty"`
	WorkloadKind string         `json:"workload_kind,omitempty"`
	WorkloadID   string         `json:"workload_id,omitempty"`
	Source       string         `json:"source"`
	Severity     string         `json:"severity"`
	Message      string         `json:"message"`
	Context      map[string]any `json:"context,omitempty"`
}

// LogFilter selects log entries; After/Before are sequence cursors.
type LogFilter struct {
	ProjectIDs []string
	PipelineID string
	RunID      string
	Node       string
	Sources    []string
	Severities []string
	Query      string
	Since      *time.Time
	Until      *time.Time
	After      int64
	Before     int64
	Limit      int
	Descending bool
}

// IngestLogsRequest is sent by runners: lines of one run, any nodes.
type IngestLogsRequest struct {
	Entries []IngestLogEntry `json:"entries"`
}

type IngestLogEntry struct {
	Timestamp    *time.Time     `json:"ts,omitempty"`
	Node         string         `json:"node"`
	Attempt      int            `json:"attempt,omitempty"`
	WorkloadKind string         `json:"workload_kind,omitempty"`
	WorkloadID   string         `json:"workload_id,omitempty"`
	Stream       string         `json:"stream,omitempty"`
	Severity     string         `json:"severity,omitempty"`
	Message      string         `json:"message"`
	Context      map[string]any `json:"context,omitempty"`
}
