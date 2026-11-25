package store_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/internal/store/storetest"
	"github.com/kiongahq/platform/pkg/api"
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

func TestFileStoreRevokesTokensWhenAccessIsWithdrawn(t *testing.T) {
	storetest.TokenRevocation(t, store.New(filepath.Join(t.TempDir(), "state.json")), "file")
}

func TestStoreConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	storetest.Connections(t, store.New(path))
	renamed := false
	for _, item := range store.New(path).Connections() {
		renamed = renamed || strings.HasPrefix(item.Name, "runtime-renamed-")
	}
	if !renamed {
		t.Fatal("connection update was not persisted")
	}
}

func TestStoreSingleUseTokens(t *testing.T) {
	repo := store.New()
	storetest.SingleUseTokens(t, repo, repo, "file")
}

func TestStoreLocalSessions(t *testing.T) {
	repo := store.New()
	storetest.LocalSessions(t, repo, repo, "file")
}
