//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/internal/store/storetest"
	"github.com/kiongahq/platform/pkg/api"
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

func TestPostgresFeatureCatalogAndScaffoldJobs(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	repository, err := store.OpenPostgres(context.Background(), databaseURL, "integration-test")
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	storetest.FeatureCatalog(t, repository)
	storetest.ScaffoldJobs(t, repository)
}

func TestPostgresEditorialRevisionsMediaAndSchedule(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	repository, err := store.OpenPostgres(context.Background(), databaseURL, fmt.Sprintf("editorial-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	storetest.Editorial(t, repository)
}

func TestPostgresRevokesTokensWhenAccessIsWithdrawn(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	repository, err := store.OpenPostgres(context.Background(), databaseURL, fmt.Sprintf("tokens-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	storetest.TokenRevocation(t, repository, fmt.Sprint(time.Now().UnixNano()))
}

// Two connections stand for two gateway replicas on one database.
func TestPostgresSingleUseTokensAcrossReplicas(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	tenant := "sign-in-" + suffix
	first, err := store.OpenPostgres(context.Background(), databaseURL, tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := store.OpenPostgres(context.Background(), databaseURL, tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	storetest.SingleUseTokens(t, first, second, suffix)

	// Another tenant on the same database shares nothing.
	other, err := store.OpenPostgres(context.Background(), databaseURL, tenant+"-other")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.RedeemOnce(context.Background(), "ticket", "once-"+suffix, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("another tenant was refused a token id used elsewhere: %v", err)
	}
}
