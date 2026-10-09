package execution

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiongahq/platform/pkg/api"
)

func dag(specs ...[]string) []api.PipelineJob {
	jobs := make([]api.PipelineJob, len(specs))
	for i, spec := range specs {
		jobs[i] = api.PipelineJob{Name: spec[0], DependsOn: spec[1:]}
	}
	return jobs
}

func TestReadySetStartsNodesWhenTheirOwnDependenciesFinish(t *testing.T) {
	afterFastStarted := make(chan struct{})
	var mu sync.Mutex
	order := []string{}
	start := func(_ context.Context, job api.PipelineJob, deps map[string]any) (any, error) {
		mu.Lock()
		order = append(order, "start:"+job.Name)
		mu.Unlock()
		switch job.Name {
		case "after-fast":
			close(afterFastStarted)
		case "slow":
			select {
			case <-afterFastStarted:
			case <-time.After(2 * time.Second):
				return nil, errors.New("after-fast did not start while slow ran (layer barrier)")
			}
		}
		return job.Name, nil
	}
	jobs := dag([]string{"extract"}, []string{"slow", "extract"}, []string{"fast", "extract"}, []string{"after-fast", "fast"}, []string{"join", "slow", "after-fast"})
	outputs, failures := Run(context.Background(), jobs, nil, start, func(string, string, string) {}, 8)
	if len(failures) > 0 {
		t.Fatalf("failures: %v", failures)
	}
	if len(outputs) != 5 || order[len(order)-1] != "start:join" {
		t.Fatalf("outputs %v order %v", outputs, order)
	}
}

func TestReadySetSkipsDownstreamOfFailureOnly(t *testing.T) {
	var reports []string
	var started sync.Map
	start := func(_ context.Context, job api.PipelineJob, _ map[string]any) (any, error) {
		started.Store(job.Name, true)
		if job.Name == "bad" {
			return nil, errors.New("exit 3")
		}
		return nil, nil
	}
	jobs := dag([]string{"root"}, []string{"bad", "root"}, []string{"good", "root"}, []string{"after-bad", "bad"}, []string{"after-good", "good"})
	_, failures := Run(context.Background(), jobs, nil, start, func(step, status, message string) { reports = append(reports, step+":"+status) }, 8)
	if _, ok := started.Load("after-bad"); ok {
		t.Fatal("downstream of failure ran")
	}
	if _, ok := started.Load("after-good"); !ok {
		t.Fatal("unrelated branch did not run")
	}
	if failures["bad"] == nil || len(reports) != 1 || reports[0] != "after-bad:skipped" {
		t.Fatalf("failures %v reports %v", failures, reports)
	}
}

func TestReadySetConditionsAndParallelismBound(t *testing.T) {
	jobs := dag([]string{"a"}, []string{"optional", "a"}, []string{"b", "optional"})
	jobs[1].When = &api.NodeCondition{Param: "mode", Equals: "full"}
	var received map[string]any
	outputs, failures := Run(context.Background(), jobs, map[string]any{"mode": "quick"}, func(_ context.Context, job api.PipelineJob, deps map[string]any) (any, error) {
		if job.Name == "b" {
			received = deps
		}
		return job.Name, nil
	}, func(string, string, string) {}, 8)
	if len(failures) > 0 || outputs["optional"] != nil || received == nil {
		t.Fatalf("condition: %v %v %v", outputs, failures, received)
	}

	var active, peak int32
	wide := make([]api.PipelineJob, 10)
	for i := range wide {
		wide[i] = api.PipelineJob{Name: string(rune('a' + i))}
	}
	Run(context.Background(), wide, nil, func(context.Context, api.PipelineJob, map[string]any) (any, error) {
		now := atomic.AddInt32(&active, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if now <= old || atomic.CompareAndSwapInt32(&peak, old, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return nil, nil
	}, func(string, string, string) {}, 3)
	if peak != 3 {
		t.Fatalf("peak parallelism %d, want 3", peak)
	}
}
