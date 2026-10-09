// Package execution runs validated pipeline DAGs with ready-set scheduling:
// a node starts as soon as its own dependencies succeed, independent
// branches run in parallel up to a bound, nodes whose `when` condition is not
// met are skipped (their dependents still run), and nodes downstream of a
// failure are skipped and reported. The Python container runner implements
// the same semantics (python/pipelines/definition.py:execute_ready_set).
package execution

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/kiongahq/platform/pkg/api"
)

// Start runs one node and returns its output.
type Start func(ctx context.Context, job api.PipelineJob, dependencies map[string]any) (any, error)

// Report receives skipped transitions decided by the engine itself; Start
// reports running/succeeded/failed for the nodes it executes.
type Report func(step, status, message string)

// ConditionMet evaluates a node's `when` against run parameters.
func ConditionMet(job api.PipelineJob, parameters map[string]any) bool {
	if job.When == nil {
		return true
	}
	return fmt.Sprint(parameters[job.When.Param]) == job.When.Equals
}

// Run executes jobs and returns outputs plus per-node failures.
func Run(ctx context.Context, jobs []api.PipelineJob, parameters map[string]any, start Start, report Report, maxParallel int) (map[string]any, map[string]error) {
	if maxParallel <= 0 {
		maxParallel = 8
	}
	type result struct {
		name   string
		output any
		err    error
	}
	pending := make([]api.PipelineJob, len(jobs))
	copy(pending, jobs)
	outputs := map[string]any{}
	failures := map[string]error{}
	satisfied, blocked := map[string]bool{}, map[string]bool{}
	results := make(chan result)
	running := 0
	var wg sync.WaitGroup

	for len(pending) > 0 || running > 0 {
		progressed := false
		next := pending[:0]
		for _, job := range pending {
			var upstream []string
			ready := true
			for _, dependency := range job.DependsOn {
				if _, failed := failures[dependency]; failed || blocked[dependency] {
					upstream = append(upstream, dependency)
				}
				if !satisfied[dependency] {
					ready = false
				}
			}
			switch {
			case len(upstream) > 0:
				blocked[job.Name] = true
				report(job.Name, "skipped", "not run: upstream "+strings.Join(upstream, ", ")+" did not succeed")
				progressed = true
			case !ready:
				next = append(next, job)
			case !ConditionMet(job, parameters):
				satisfied[job.Name], outputs[job.Name] = true, nil
				report(job.Name, "skipped", fmt.Sprintf("condition not met: %s != %s", job.When.Param, job.When.Equals))
				progressed = true
			case running >= maxParallel:
				next = append(next, job)
			default:
				dependencies := map[string]any{}
				for _, dependency := range job.DependsOn {
					dependencies[dependency] = outputs[dependency]
				}
				running++
				progressed = true
				wg.Add(1)
				go func(job api.PipelineJob) {
					defer wg.Done()
					output, err := start(ctx, job, dependencies)
					results <- result{job.Name, output, err}
				}(job)
			}
		}
		pending = next
		if running == 0 {
			if !progressed && len(pending) > 0 {
				names := make([]string, len(pending))
				for i, job := range pending {
					names[i] = job.Name
				}
				failures["orchestrator"] = fmt.Errorf("nodes can never start: %s", strings.Join(names, ", "))
				break
			}
			continue
		}
		done := <-results
		running--
		if done.err != nil {
			failures[done.name] = done.err
		} else {
			outputs[done.name], satisfied[done.name] = done.output, true
		}
	}
	wg.Wait()
	return outputs, failures
}
