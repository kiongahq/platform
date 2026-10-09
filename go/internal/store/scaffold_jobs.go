package store

import (
	"errors"
	"sort"
	"time"

	"github.com/ml-ai-ops/platform/pkg/api"
)

// Scaffold jobs are documents. A per-project lock document guarantees at
// most one active job per project without nesting document updates (the
// file backend's lock is not reentrant).
const (
	ScaffoldJobKind     = "scaffold_job"
	scaffoldJobLockKind = "scaffold_job_lock"
	// ScaffoldJobStaleAfter bounds how long a job may stay queued/running.
	// A gateway restart mid-job would otherwise leave it running forever.
	ScaffoldJobStaleAfter = 5 * time.Minute
)

// ErrJobActive is returned when a project already has a queued or running job.
var ErrJobActive = errors.New("a scaffold job is already running for this project")

type scaffoldJobLock struct {
	ProjectID string    `json:"project_id"`
	JobID     string    `json:"job_id"`
	Active    bool      `json:"active"`
	Since     time.Time `json:"since"`
}

// ActiveScaffoldStatus reports whether a status still needs to finish.
func ActiveScaffoldStatus(status string) bool { return status == "queued" || status == "running" }

// CreateScaffoldJob stores a queued job after acquiring the project's lock.
func CreateScaffoldJob(docs Documents, job api.ScaffoldJob, actor string) (api.ScaffoldJob, error) {
	now := time.Now().UTC()
	if job.ID == "" {
		job.ID = id("scf")
	}
	job.Status, job.CreatedAt = "queued", now
	if job.Files == nil {
		job.Files = []string{}
	}
	if job.GitStatus == nil {
		job.GitStatus = []string{}
	}
	_, err := UpdateDoc(docs, scaffoldJobLockKind, job.ProjectID, func(current scaffoldJobLock, exists bool) (scaffoldJobLock, error) {
		if exists && current.Active && now.Sub(current.Since) < ScaffoldJobStaleAfter {
			return current, ErrJobActive
		}
		return scaffoldJobLock{ProjectID: job.ProjectID, JobID: job.ID, Active: true, Since: now}, nil
	}, "", actor)
	if err != nil {
		return api.ScaffoldJob{}, err
	}
	return UpdateDoc(docs, ScaffoldJobKind, job.ID, func(_ api.ScaffoldJob, exists bool) (api.ScaffoldJob, error) {
		if exists {
			return job, ErrConflict
		}
		return job, nil
	}, "scaffold_job.created", actor)
}

// StartScaffoldJob moves a queued job to running (internal bookkeeping).
func StartScaffoldJob(docs Documents, jobID string) (api.ScaffoldJob, error) {
	return UpdateDoc(docs, ScaffoldJobKind, jobID, func(job api.ScaffoldJob, exists bool) (api.ScaffoldJob, error) {
		if !exists {
			return job, ErrNotFound
		}
		if job.Status != "queued" {
			return job, ErrSkipWrite
		}
		now := time.Now().UTC()
		job.Status, job.StartedAt = "running", &now
		return job, nil
	}, "", "scaffold-runner")
}

// FinishScaffoldJob records the terminal result and releases the lock. A job
// that already reached a terminal state is never rewritten.
func FinishScaffoldJob(docs Documents, jobID string, result api.ScaffoldJob, actor string) (api.ScaffoldJob, error) {
	status := "failed"
	if result.Status == "succeeded" {
		status = "succeeded"
	}
	job, err := UpdateDoc(docs, ScaffoldJobKind, jobID, func(job api.ScaffoldJob, exists bool) (api.ScaffoldJob, error) {
		if !exists {
			return job, ErrNotFound
		}
		if !ActiveScaffoldStatus(job.Status) {
			return job, ErrSkipWrite
		}
		now := time.Now().UTC()
		if job.StartedAt == nil {
			job.StartedAt = &now
		}
		job.Status, job.EndedAt = status, &now
		job.ExitCode, job.OutputTail, job.Error = result.ExitCode, result.OutputTail, result.Error
		job.Files, job.GitStatus = nonNil(result.Files), nonNil(result.GitStatus)
		return job, nil
	}, "scaffold_job."+status, actor)
	if err != nil {
		return job, err
	}
	_, lockErr := UpdateDoc(docs, scaffoldJobLockKind, job.ProjectID, func(current scaffoldJobLock, exists bool) (scaffoldJobLock, error) {
		if !exists || current.JobID != jobID || !current.Active {
			return current, ErrSkipWrite
		}
		current.Active = false
		return current, nil
	}, "", actor)
	if lockErr != nil && !errors.Is(lockErr, ErrNotFound) {
		return job, lockErr
	}
	return job, nil
}

// ScaffoldJob returns one job, failing it first if it outlived the stale
// bound (the runner that owned it is gone).
func ScaffoldJob(docs Documents, jobID string, now time.Time) (api.ScaffoldJob, error) {
	job, err := GetDoc[api.ScaffoldJob](docs, ScaffoldJobKind, jobID)
	if err != nil {
		return job, err
	}
	return reconcileStale(docs, job, now), nil
}

// ScaffoldJobs lists a project's jobs, newest first.
func ScaffoldJobs(docs Documents, projectID string, now time.Time) []api.ScaffoldJob {
	out := []api.ScaffoldJob{}
	for _, job := range ListDocs[api.ScaffoldJob](docs, ScaffoldJobKind) {
		if job.ProjectID == projectID {
			out = append(out, reconcileStale(docs, job, now))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func reconcileStale(docs Documents, job api.ScaffoldJob, now time.Time) api.ScaffoldJob {
	if !ActiveScaffoldStatus(job.Status) || now.Sub(job.CreatedAt) < ScaffoldJobStaleAfter {
		return job
	}
	finished, err := FinishScaffoldJob(docs, job.ID, api.ScaffoldJob{Status: "failed", Error: "The job was interrupted before it reported a result (the gateway restarted or the workspace stopped responding). Run it again."}, "scaffold-runner")
	if err != nil {
		return job
	}
	return finished
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
