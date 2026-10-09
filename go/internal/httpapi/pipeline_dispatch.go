package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ml-ai-ops/platform/internal/execution"
	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

const defaultFunctionTimeout = 5 * time.Minute

// executionDefinition returns exactly what a run must execute: the
// definition revision pinned at submission, with the run's overrides
// applied. Falling back to the live definition only happens for legacy runs
// that predate revisions.
func (s *Server) executionDefinition(run api.PipelineRun) (api.PipelineDefinition, error) {
	definition, err := s.store.PipelineDefinition(run.DefinitionID)
	if err != nil {
		return definition, err
	}
	if run.Provenance != nil && run.Provenance.DefinitionRevision > 0 && run.Provenance.DefinitionRevision != definition.Revision {
		revision, err := store.PipelineRevision(s.store, definition.ID, run.Provenance.DefinitionRevision)
		if err != nil {
			return definition, fmt.Errorf("revision %d of %s is unavailable: %w", run.Provenance.DefinitionRevision, definition.ID, err)
		}
		definition.Jobs, definition.ExecutionMode = revision.Spec.Jobs, revision.Spec.ExecutionMode
		definition.Revision, definition.SHA256 = revision.Revision, revision.SHA256
	}
	if run.Provenance != nil && run.Provenance.Overrides != nil {
		definition.Jobs = ApplyNodeOverrides(definition.Jobs, run.Provenance.Overrides)
	}
	return definition, nil
}

// ApplyNodeOverrides returns jobs with one run's node overrides applied and,
// when nodes were selected, only the selected nodes plus everything they
// depend on.
func ApplyNodeOverrides(jobs []api.PipelineJob, overrides *api.RunOverrides) []api.PipelineJob {
	out := make([]api.PipelineJob, 0, len(jobs))
	keep := selectedClosure(jobs, overrides.SelectedNodes)
	for _, job := range jobs {
		if keep != nil && !keep[job.Name] {
			continue
		}
		if override, ok := overrides.Nodes[job.Name]; ok {
			if override.Image != "" {
				job.Image = override.Image
			}
			if override.Resources != nil {
				job.Resources = *override.Resources
			}
			if override.Retries != nil {
				job.Retries = *override.Retries
			}
			if override.TimeoutSeconds > 0 {
				job.TimeoutSeconds = override.TimeoutSeconds
			}
			if len(override.Environment) > 0 {
				environment := map[string]string{}
				for key, value := range job.Environment {
					environment[key] = value
				}
				for key, value := range override.Environment {
					environment[key] = value
				}
				job.Environment = environment
			}
		}
		if keep != nil {
			deps := job.DependsOn[:0:0]
			for _, dependency := range job.DependsOn {
				if keep[dependency] {
					deps = append(deps, dependency)
				}
			}
			job.DependsOn = deps
		}
		out = append(out, job)
	}
	return out
}

// selectedClosure returns the selected nodes plus all their ancestors, or
// nil when no selection was made (run everything).
func selectedClosure(jobs []api.PipelineJob, selected []string) map[string]bool {
	if len(selected) == 0 {
		return nil
	}
	parents := map[string][]string{}
	for _, job := range jobs {
		parents[job.Name] = job.DependsOn
	}
	keep := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if keep[name] {
			return
		}
		keep[name] = true
		for _, parent := range parents[name] {
			visit(parent)
		}
	}
	for _, name := range selected {
		if _, ok := parents[name]; ok {
			visit(name)
		}
	}
	return keep
}

// dispatchPipeline hands a recorded run to its execution engine. Container
// DAGs go to the pipeline runner (Prefect locally); function DAGs execute
// here against OpenFaaS. Engine rejections mark the run failed with the
// engine's reason rather than leaving it queued.
func (s *Server) dispatchPipeline(ctx context.Context, run api.PipelineRun) api.PipelineRun {
	if run.ExecutionMode == "functions" {
		definition, err := s.executionDefinition(run)
		if err != nil {
			failed, _ := s.reportStep(run.ID, api.UpdateRunStepRequest{Step: "load-definition", Status: "failed", Message: err.Error()}, "system")
			return failed
		}
		if s.openfaas() == nil {
			for _, job := range definition.Jobs {
				run, _ = s.reportStep(run.ID, api.UpdateRunStepRequest{Step: job.Name, Status: "failed", Message: "Not run: OpenFaaS is not configured (set OPENFAAS_URL or activate an OpenFaaS connection)"}, "system")
			}
			return run
		}
		go s.executeFunctionPipeline(context.Background(), run, definition)
		return run
	}
	if !s.prefectConfigured() {
		failed, _ := s.reportStep(run.ID, api.UpdateRunStepRequest{Step: "submit-to-engine", Status: "failed", Message: "The pipeline engine is not configured (PREFECT_API_URL); the run was recorded but cannot execute."}, "system")
		return failed
	}
	parameters := map[string]any{"run_id": run.ID, "project_id": run.ProjectID, "parameters": run.Parameters}
	// A caller-provided run name is a label, never a deployment selector.
	flowName := "training-pipeline"
	if run.DefinitionID != "" {
		definition, err := s.executionDefinition(run)
		if err != nil {
			failed, _ := s.reportStep(run.ID, api.UpdateRunStepRequest{Step: "load-definition", Status: "failed", Message: err.Error()}, "system")
			return failed
		}
		payload := map[string]any{"id": definition.ID, "revision": definition.Revision, "sha256": definition.SHA256, "jobs": definition.Jobs}
		if reuse := s.priorOutputs(run); len(reuse) > 0 {
			payload["reuse"] = reuse
		}
		if run.Provenance != nil && run.Provenance.Overrides != nil && run.Provenance.Overrides.MaxParallelism > 0 {
			payload["max_parallelism"] = run.Provenance.Overrides.MaxParallelism
		}
		parameters["definition"] = payload
		flowName = "pipeline-definition"
	}
	engineID, err := s.prefectClient().CreateFlowRun(ctx, flowName, "mlaiops", run.Name, parameters)
	if err != nil {
		failed, _ := s.reportStep(run.ID, api.UpdateRunStepRequest{Step: "submit-to-engine", Status: "failed", Message: err.Error()}, "system")
		return failed
	}
	if linked, err := s.store.SetRunEngine(run.ID, engineID); err == nil {
		return linked
	}
	return run
}

func (s *Server) executeFunctionPipeline(ctx context.Context, run api.PipelineRun, definition api.PipelineDefinition) {
	client := s.openfaas()
	if client == nil {
		return
	}
	report := func(req api.UpdateRunStepRequest) {
		now := time.Now().UTC()
		req.At = &now
		_, _ = s.reportStep(run.ID, req, "system")
	}
	maxParallel := 0
	if run.Provenance != nil && run.Provenance.Overrides != nil {
		maxParallel = run.Provenance.Overrides.MaxParallelism
	}
	reuse := s.priorOutputs(run)
	start := func(ctx context.Context, job api.PipelineJob, dependencies map[string]any) (any, error) {
		if output, ok := reuse[job.Name]; ok {
			report(api.UpdateRunStepRequest{Step: job.Name, Status: "skipped", Message: "Reused the output of run " + run.Provenance.Overrides.RerunFromRunID})
			return output, nil
		}
		payload, _ := json.Marshal(map[string]any{"run_id": run.ID, "project_id": run.ProjectID, "step": job.Name, "parameters": run.Parameters, "dependencies": dependencies})
		timeout := defaultFunctionTimeout
		if job.TimeoutSeconds > 0 {
			timeout = time.Duration(job.TimeoutSeconds) * time.Second
		}
		var raw []byte
		var err error
		attempts := job.Retries + 1
		for attempt := 1; attempt <= attempts; attempt++ {
			report(api.UpdateRunStepRequest{Step: job.Name, Status: "running", Message: fmt.Sprintf("invoking function %s attempt %d/%d", job.Function, attempt, attempts), Attempt: attempt, WorkloadKind: "openfaas-call", WorkloadID: job.Function})
			invocation, cancel := context.WithTimeout(ctx, timeout)
			raw, err = client.Invoke(invocation, job.Function, payload)
			cancel()
			if err == nil {
				var output any
				if json.Unmarshal(raw, &output) != nil {
					output = string(raw)
				}
				report(api.UpdateRunStepRequest{Step: job.Name, Status: "succeeded", Message: fmt.Sprintf("function %s completed (%d bytes)", job.Function, len(raw)), Attempt: attempt, WorkloadKind: "openfaas-call", WorkloadID: job.Function})
				return output, nil
			}
			if attempt < attempts && job.RetryBackoffSeconds > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(job.RetryBackoffSeconds) * time.Second):
				}
			}
		}
		if strings.Contains(err.Error(), "deadline exceeded") {
			err = fmt.Errorf("function %s timed out after %s: %w", job.Function, timeout, err)
		}
		report(api.UpdateRunStepRequest{Step: job.Name, Status: "failed", Message: err.Error(), Attempt: attempts, WorkloadKind: "openfaas-call", WorkloadID: job.Function})
		return nil, err
	}
	_, failures := execution.Run(ctx, definition.Jobs, run.Parameters, start, func(step, status, message string) {
		report(api.UpdateRunStepRequest{Step: step, Status: status, Message: message})
	}, maxParallel)
	if err, ok := failures["orchestrator"]; ok {
		report(api.UpdateRunStepRequest{Step: "orchestrator", Status: "failed", Message: err.Error()})
	}
}

// priorOutputs returns, for a rerun-from-failure, the recorded output of
// every node that succeeded in the previous run. Outputs are the JSON a node
// printed last (or its text), as recorded in the step message.
func (s *Server) priorOutputs(run api.PipelineRun) map[string]any {
	if run.Provenance == nil || run.Provenance.Overrides == nil || run.Provenance.Overrides.RerunFromRunID == "" {
		return nil
	}
	previous, err := s.store.Run(run.Provenance.Overrides.RerunFromRunID)
	if err != nil {
		return nil
	}
	out := map[string]any{}
	for _, step := range previous.Steps {
		if step.Status != "succeeded" {
			continue
		}
		var value any
		if json.Unmarshal([]byte(step.Message), &value) != nil {
			lines := strings.Split(strings.TrimSpace(step.Message), "\n")
			if json.Unmarshal([]byte(lines[len(lines)-1]), &value) != nil {
				value = step.Message
			}
		}
		out[step.Name] = value
	}
	return out
}
