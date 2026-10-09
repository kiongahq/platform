package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

func scheduledDefinition(t *testing.T, data *store.Store, owner string) api.PipelineDefinition {
	t.Helper()
	project, err := data.CreateProject(api.CreateProjectRequest{Name: fmt.Sprintf("Scheduled %s %d", owner, time.Now().UnixNano())}, owner)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := data.UpsertPipelineDefinition("", api.UpsertPipelineDefinitionRequest{ProjectID: project.ID, Name: "nightly", Version: "1",
		Triggers: []api.PipelineTrigger{{Type: "schedule", Cron: "0 2 * * *", Timezone: "UTC"}},
		Jobs:     []api.PipelineJob{{Name: "a", Kind: "container", Image: "busybox"}}}, owner)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestScheduledRunsExecuteAsTheOwner(t *testing.T) {
	t.Setenv("PREFECT_API_URL", "")
	data := store.New()
	s := &Server{store: data}
	slot := time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC)

	// The local bootstrap administrator may own schedules.
	definition := scheduledDefinition(t, data, "admin")
	runID, err := s.submitScheduledRun(context.Background(), definition, slot)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := data.Run(runID)
	if run.Trigger != "schedule" || run.ScheduledFor == nil || !run.ScheduledFor.Equal(slot) || run.OwnerSubject != "admin" ||
		run.Provenance == nil || run.Provenance.DefinitionRevision != 1 || run.Provenance.DefinitionSHA256 != definition.SHA256 {
		t.Fatalf("scheduled run provenance: %+v %+v", run, run.Provenance)
	}

	// A suspended owner's schedules stop running.
	definition = scheduledDefinition(t, data, "alice")
	if _, err := data.UpsertUserAccess("alice", api.UpsertUserAccessRequest{Role: "user", Services: []string{"pipelines"}, Disabled: true}, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.submitScheduledRun(context.Background(), definition, slot); err == nil || !strings.Contains(err.Error(), "suspended") {
		t.Fatalf("suspended owner: %v", err)
	}

	// Unknown owners and OIDC deployments never fall back to administrator.
	definition = scheduledDefinition(t, data, "ghost")
	if _, err := s.submitScheduledRun(context.Background(), definition, slot); err == nil || !strings.Contains(err.Error(), "no provisioned access") {
		t.Fatalf("unknown owner: %v", err)
	}
	t.Setenv("OIDC_ISSUER", "https://idp.example")
	definition = scheduledDefinition(t, data, "admin")
	if _, err := s.submitScheduledRun(context.Background(), definition, slot); err == nil {
		t.Fatal("bootstrap admin fallback used in OIDC mode")
	}
}
