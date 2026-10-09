package storetest

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

// PipelineRevisions checks that definitions advance their revision only on
// real changes, keep immutable snapshots, and preserve scheduler state.
func PipelineRevisions(t *testing.T, repo store.Repository, projectID string) {
	t.Helper()
	req := api.UpsertPipelineDefinitionRequest{
		ProjectID: projectID, Name: "revisions", Version: "1", ExecutionMode: "prefect",
		Triggers: []api.PipelineTrigger{{Type: "schedule", Cron: "0 * * * *", Timezone: "UTC"}},
		Jobs:     []api.PipelineJob{{Name: "a", Kind: "container", Image: "busybox"}},
	}
	first, err := repo.UpsertPipelineDefinition("", req, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || len(first.SHA256) != 64 || first.Triggers[0].NextRunAt == nil {
		t.Fatalf("first save: %+v", first)
	}
	same, err := repo.UpsertPipelineDefinition(first.ID, req, "tester")
	if err != nil || same.Revision != 1 {
		t.Fatalf("unchanged save bumped revision: %+v %v", same, err)
	}
	if !same.Triggers[0].NextRunAt.Equal(*first.Triggers[0].NextRunAt) {
		t.Fatal("scheduler state lost on unchanged save")
	}
	req.Jobs = append(req.Jobs, api.PipelineJob{Name: "b", Kind: "container", Image: "busybox", DependsOn: []string{"a"}})
	req.Message = "add b"
	second, err := repo.UpsertPipelineDefinition(first.ID, req, "tester")
	if err != nil || second.Revision != 2 || second.SHA256 == first.SHA256 {
		t.Fatalf("changed save: %+v %v", second, err)
	}
	revisions := store.PipelineRevisions(repo, first.ID)
	if len(revisions) != 2 || revisions[0].Revision != 2 || revisions[0].Message != "add b" || revisions[1].SHA256 != first.SHA256 {
		t.Fatalf("revisions: %+v", revisions)
	}
	old, err := store.PipelineRevision(repo, first.ID, 1)
	if err != nil || len(old.Spec.Jobs) != 1 || old.YAML == "" {
		t.Fatalf("revision 1 snapshot: %+v %v", old, err)
	}
	if _, err := repo.UpsertPipelineDefinition("pipe-does-not-exist", req, "tester"); err == nil {
		t.Fatal("update of unknown definition succeeded")
	}
}

// ConcurrentStepReports submits a fan-out run and has every node report at
// once. No transition may be lost, and step facts must be recorded.
func ConcurrentStepReports(t *testing.T, repo store.Repository, projectID string) {
	t.Helper()
	jobs := []api.PipelineJob{{Name: "root", Kind: "container", Image: "busybox"}}
	for i := 0; i < 20; i++ {
		jobs = append(jobs, api.PipelineJob{Name: fmt.Sprintf("leaf-%02d", i), Kind: "container", Image: "busybox", DependsOn: []string{"root"}})
	}
	definition, err := repo.UpsertPipelineDefinition("", api.UpsertPipelineDefinitionRequest{ProjectID: projectID, Name: fmt.Sprintf("fanout-%d", time.Now().UnixNano()), Version: "1", Jobs: jobs}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	run, err := repo.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: projectID, DefinitionID: definition.ID}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: "root", Status: "succeeded"}, "engine"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code := 0
			status := "succeeded"
			if i == 7 {
				status, code = "failed", 3
			}
			if _, err := repo.UpdateRunStep(run.ID, api.UpdateRunStepRequest{Step: fmt.Sprintf("leaf-%02d", i), Status: status, Attempt: 1, ExitCode: &code, WorkloadKind: "docker-container", WorkloadID: fmt.Sprintf("c%02d", i), ImageDigest: fmt.Sprintf("busybox@sha256:%064d", i)}, "engine"); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	final, err := repo.Run(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Provenance == nil || len(final.Provenance.ImageDigests) != 20 {
		t.Fatalf("image digests not recorded for every node: %+v", final.Provenance)
	}
	if final.Status != "failed" {
		t.Fatalf("run status %q", final.Status)
	}
	for _, step := range final.Steps {
		if step.Status == "pending" || step.Status == "running" {
			t.Fatalf("lost transition for %s: %+v", step.Name, step)
		}
		if step.Name != "root" && (step.WorkloadKind != "docker-container" || step.EndedAt == nil || step.ExitCode == nil) {
			t.Fatalf("step facts not recorded: %+v", step)
		}
	}
}
