//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/internal/store/storetest"
	"github.com/ml-ai-ops/platform/pkg/api"
)

func TestPostgresPersistsResourceAuditAndOutboxAtomically(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	repository, err := store.OpenPostgres(context.Background(), databaseURL, "integration-test")
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Integration project"}, "test-user")
	if err != nil && err != store.ErrConflict {
		t.Fatal(err)
	}
	if project.ID != "" {
		if _, err := repository.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: project.ID, Name: "train"}, "test-user"); err != nil {
			t.Fatal(err)
		}
	}
	if len(repository.Audit()) == 0 {
		t.Fatal("expected durable audit event")
	}
	events, err := repository.PendingOutbox(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected transactional outbox events")
	}
}

func TestPostgresDocumentsConformance(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	repository, err := store.OpenPostgres(context.Background(), databaseURL, "integration-docs")
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	kind := fmt.Sprintf("conformance_%d", time.Now().UnixNano())
	storetest.Documents(t, repository, kind)
}

func TestPostgresPipelineRevisions(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	repository, err := store.OpenPostgres(context.Background(), databaseURL, fmt.Sprintf("revisions-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Revisions project"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	storetest.PipelineRevisions(t, repository, project.ID)
	storetest.ConcurrentStepReports(t, repository, project.ID)
	storetest.Logs(t, repository, fmt.Sprintf("run-logs-%d", time.Now().UnixNano()))
}

func TestPostgresIAMPolicies(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	repository, err := store.OpenPostgres(context.Background(), databaseURL, fmt.Sprintf("iam-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	storetest.IAM(t, repository)
}
