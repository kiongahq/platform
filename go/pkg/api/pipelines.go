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
