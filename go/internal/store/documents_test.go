package store_test

import (
	"path/filepath"
	"testing"

	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/internal/store/storetest"
	"github.com/ml-ai-ops/platform/pkg/api"
)

func TestStoreDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	storetest.Documents(t, store.New(path), "sample")
	if _, err := store.New(path).GetDocument("sample", "b"); err != nil {
		t.Fatalf("documents not persisted: %v", err)
	}
}

func TestStorePipelineRevisions(t *testing.T) {
	repo := store.New()
	project, err := repo.CreateProject(api.CreateProjectRequest{Name: "Revisions project"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	storetest.PipelineRevisions(t, repo, project.ID)
}

func TestStoreConcurrentStepReports(t *testing.T) {
	repo := store.New()
	project, err := repo.CreateProject(api.CreateProjectRequest{Name: "Fanout project"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	storetest.ConcurrentStepReports(t, repo, project.ID)
}

func TestStoreLogs(t *testing.T) {
	storetest.Logs(t, store.New(), "run-logs")
}

func TestStoreIAM(t *testing.T) {
	storetest.IAM(t, store.New(filepath.Join(t.TempDir(), "state.json")))
}

func TestFeatureCatalogDocuments(t *testing.T) {
	storetest.FeatureCatalog(t, store.New(filepath.Join(t.TempDir(), "state.json")))
}

func TestScaffoldJobDocuments(t *testing.T) {
	storetest.ScaffoldJobs(t, store.New(filepath.Join(t.TempDir(), "state.json")))
}
