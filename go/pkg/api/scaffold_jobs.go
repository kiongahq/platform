package api

import "time"

// ScaffoldJob is one structured `kionga scaffold` execution inside a
// project's workspace. The argv is built by the gateway from the stored
// project and the versioned template catalog; no browser-supplied text is
// ever part of it.
type ScaffoldJob struct {
	ID          string     `json:"id"`
	ProjectID   string     `json:"project_id"`
	Namespace   string     `json:"namespace"`
	Status      string     `json:"status"` // queued | running | succeeded | failed
	Workspace   string     `json:"workspace"`
	Argv        []string   `json:"argv"`
	Command     string     `json:"command"`
	WorkingDir  string     `json:"working_dir"`
	OutputDir   string     `json:"output_dir"`
	RequestedBy string     `json:"requested_by"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	OutputTail  string     `json:"output_tail,omitempty"`
	Files       []string   `json:"files"`
	GitStatus   []string   `json:"git_status"`
	Error       string     `json:"error,omitempty"`
}

// ScaffoldPlan is what a scaffold job would run, shown to the user before
// they confirm.
type ScaffoldPlan struct {
	ProjectID  string   `json:"project_id"`
	Namespace  string   `json:"namespace"`
	Workspace  string   `json:"workspace"`
	Argv       []string `json:"argv"`
	Command    string   `json:"command"`
	WorkingDir string   `json:"working_dir"`
	OutputDir  string   `json:"output_dir"`
	Available  bool     `json:"available"`
	Reason     string   `json:"reason,omitempty"`
}

// ScaffoldJobOptions are the only caller-controlled inputs. Workspace picks
// which assigned workspace executes the job ("workbench" or "ide"); empty
// selects the first one available.
type ScaffoldJobOptions struct {
	Workspace string `json:"workspace,omitempty"`
}

type CreateScaffoldJobRequest struct {
	Options *ScaffoldJobOptions `json:"options,omitempty"`
}
