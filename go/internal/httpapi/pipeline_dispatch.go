package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ml-ai-ops/platform/internal/integrations"
	"github.com/ml-ai-ops/platform/pkg/api"
)

// dispatchPipeline selects the execution profile recorded by the definition.
// Prefect owns container-based durable flows. Function DAGs use OpenFaaS and
// run ready jobs concurrently while preserving dependency ordering.
func (s *Server) dispatchPipeline(ctx context.Context, run api.PipelineRun) api.PipelineRun {
	if run.ExecutionMode == "functions" {
		definition, err := s.store.PipelineDefinition(run.DefinitionID)
		if err != nil {
			failed, _ := s.store.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: "load-definition", Status: "failed", Message: err.Error()}, "system")
			return failed
		}
		if openfaas() == nil {
			failed, _ := s.store.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: definition.Jobs[0].Name, Status: "failed", Message: "OPENFAAS_URL is not configured"}, "system")
			return failed
		}
		go s.executeFunctionPipeline(context.Background(), run, definition)
		return run
	}
	if prefectURL := os.Getenv("PREFECT_API_URL"); prefectURL != "" {
		parameters := map[string]any{"run_id": run.ID, "project_id": run.ProjectID, "parameters": run.Parameters}
		// A caller-provided run name is a label, never a deployment selector.
		// Name-only runs always use the bundled training flow; definition-backed
		// runs use the generic container-DAG flow below.
		flowName := "training-pipeline"
		if run.DefinitionID != "" {
			definition, err := s.store.PipelineDefinition(run.DefinitionID)
			if err != nil {
				failed, _ := s.store.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: "load-definition", Status: "failed", Message: err.Error()}, "system")
				return failed
			}
			parameters["definition"] = definition
			flowName = "pipeline-definition"
		}
		prefect := integrations.NewPrefect(prefectURL, "")
		engineID, err := prefect.CreateFlowRun(ctx, flowName, "mlaiops", run.Name, parameters)
		if err != nil {
			failed, _ := s.store.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: "submit-to-engine", Status: "failed", Message: err.Error()}, "system")
			return failed
		}
		if linked, err := s.store.SetRunEngine(run.ID, engineID); err == nil {
			return linked
		}
	}
	return run
}

func (s *Server) executeFunctionPipeline(ctx context.Context, run api.PipelineRun, definition api.PipelineDefinition) {
	client := openfaas()
	if client == nil {
		return
	}
	remaining := make(map[string]api.PipelineJob, len(definition.Jobs))
	for _, job := range definition.Jobs {
		remaining[job.Name] = job
	}
	outputs := map[string]any{}
	completed := map[string]bool{}
	for len(remaining) > 0 {
		ready := make([]api.PipelineJob, 0)
		for _, job := range remaining {
			dependenciesReady := true
			for _, dependency := range job.DependsOn {
				if !completed[dependency] {
					dependenciesReady = false
					break
				}
			}
			if dependenciesReady {
				ready = append(ready, job)
			}
		}
		if len(ready) == 0 {
			_, _ = s.store.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: "orchestrator", Status: "failed", Message: "pipeline graph cannot make progress"}, "system")
			return
		}
		var wait sync.WaitGroup
		var mu sync.Mutex
		failed := false
		for _, job := range ready {
			job := job
			delete(remaining, job.Name)
			wait.Add(1)
			go func() {
				defer wait.Done()
				_, _ = s.store.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: job.Name, Status: "running", Message: "invoking function " + job.Function}, "system")
				mu.Lock()
				dependencies := map[string]any{}
				for _, name := range job.DependsOn {
					dependencies[name] = outputs[name]
				}
				mu.Unlock()
				payload, _ := json.Marshal(map[string]any{"run_id": run.ID, "project_id": run.ProjectID, "step": job.Name, "parameters": run.Parameters, "dependencies": dependencies})
				var raw []byte
				var err error
				for attempt := 0; attempt <= job.Retries; attempt++ {
					invocationContext, cancel := context.WithTimeout(ctx, 5*time.Minute)
					raw, err = client.Invoke(invocationContext, job.Function, payload)
					cancel()
					if err == nil {
						break
					}
				}
				if err != nil {
					_, _ = s.store.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: job.Name, Status: "failed", Message: err.Error()}, "system")
					mu.Lock()
					failed = true
					mu.Unlock()
					return
				}
				var output any
				if json.Unmarshal(raw, &output) != nil {
					output = string(raw)
				}
				mu.Lock()
				outputs[job.Name], completed[job.Name] = output, true
				mu.Unlock()
				message := fmt.Sprintf("function %s completed (%d bytes)", job.Function, len(raw))
				_, _ = s.store.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: job.Name, Status: "succeeded", Message: message}, "system")
			}()
		}
		wait.Wait()
		if failed {
			return
		}
	}
}
