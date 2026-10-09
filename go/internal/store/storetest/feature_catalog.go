package storetest

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// FeatureCatalog checks connection documents, immutable definition versions
// and lineage on any Documents backend. Identifiers are unique per run so a
// shared integration database can be reused.
func FeatureCatalog(t *testing.T, docs store.Documents) {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	connection := api.FeatureStoreConnection{
		ID: "fs-" + suffix, Provider: "feast", Name: "feast-" + suffix, Kind: "external",
		Config: map[string]string{"url": "http://feast:6566"}, SecretRef: "env:FEAST_TOKEN",
		AllowedProjects: []string{"p1"}, Health: api.ConnectionHealth{State: "configured"},
	}
	saved, err := store.SaveFeatureStoreConnection(docs, connection, true, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if saved.CreatedBy != "admin" || saved.CreatedAt.IsZero() {
		t.Fatalf("creation metadata missing: %+v", saved)
	}
	if _, err := store.SaveFeatureStoreConnection(docs, connection, true, "admin"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate create must conflict, got %v", err)
	}
	missing := connection
	missing.ID = "fs-missing-" + suffix
	if _, err := store.SaveFeatureStoreConnection(docs, missing, false, "admin"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update of missing connection must be not found, got %v", err)
	}
	now := time.Now().UTC()
	if _, err := store.SetFeatureStoreHealth(docs, connection.ID, api.ConnectionHealth{State: "healthy", Detail: "ok", CheckedAt: &now}, "admin"); err != nil {
		t.Fatal(err)
	}
	connection.Name = "renamed-" + suffix
	connection.Health = api.ConnectionHealth{}
	updated, err := store.SaveFeatureStoreConnection(docs, connection, false, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Health.State != "healthy" || updated.CreatedBy != "admin" || updated.Name != connection.Name {
		t.Fatalf("update must keep health and creator: %+v", updated)
	}
	raw, err := docs.GetDocument(store.FeatureStoreConnectionKind, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["secret_ref"] != "env:FEAST_TOKEN" || stored["secret_present"] != false {
		t.Fatalf("only the secret reference may be stored: %s", raw)
	}

	storage := api.StorageConnection{ID: "st-" + suffix, Name: "lake-" + suffix, Endpoint: "https://s3.example.com", Region: "eu-west-1", Bucket: "lake", PathStyle: true, SecretRef: "env:LAKE_CREDENTIALS", AllowedProjects: []string{}}
	if _, err := store.SaveStorageConnection(docs, storage, true, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetStorageHealth(docs, storage.ID, api.ConnectionHealth{State: "unavailable", Detail: "timeout", CheckedAt: &now}, "admin"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetDoc[api.StorageConnection](docs, store.StorageConnectionKind, storage.ID)
	if err != nil || got.Health.State != "unavailable" || !got.PathStyle {
		t.Fatalf("storage connection round trip: %+v %v", got, err)
	}
	if err := docs.DeleteDocument(store.StorageConnectionKind, storage.ID, "storage_connection.deleted", "admin"); err != nil {
		t.Fatal(err)
	}

	view := api.FeatureView{Name: "view_" + suffix, Entity: "user_id", Fields: []api.FeatureField{{Name: "plan", Type: "string"}}, TTLSeconds: 60, Source: "s3://features/x"}
	first, created, err := store.RecordFeatureDefinitionVersion(docs, view, "", "admin")
	if err != nil || !created || first.Version != 1 || first.StoreID != "internal" {
		t.Fatalf("first version: %+v created=%v err=%v", first, created, err)
	}
	again, created, err := store.RecordFeatureDefinitionVersion(docs, view, "", "admin")
	if err != nil || created || again.Version != 1 {
		t.Fatalf("unchanged definition must not create a version: %+v created=%v err=%v", again, created, err)
	}
	view.Fields = append(view.Fields, api.FeatureField{Name: "region", Type: "string"})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := store.RecordFeatureDefinitionVersion(docs, view, "", "admin"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	versions := store.FeatureDefinitionVersions(docs, view.Name)
	if len(versions) != 2 || versions[0].Version != 2 || versions[1].Version != 1 || len(versions[1].Fields) != 1 {
		t.Fatalf("concurrent identical applies must create exactly one new version, older versions unchanged: %+v", versions)
	}

	for _, status := range []string{"succeeded", "failed"} {
		if _, err := store.RecordFeatureMaterialization(docs, api.FeatureMaterialization{View: view.Name, ViewVersion: 2, RunID: "run-" + status, SourceDataset: "s3://raw/users", EntityCount: 3, Status: status}, "materializer"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	lineage := store.FeatureMaterializations(docs, view.Name)
	if len(lineage) != 2 || lineage[0].Status != "failed" || lineage[1].RunID != "run-succeeded" || !strings.HasPrefix(lineage[0].ID, "fmat-") {
		t.Fatalf("lineage must list newest first: %+v", lineage)
	}
}

// ScaffoldJobs checks the job lifecycle and the one-active-job-per-project
// guarantee on any Documents backend.
func ScaffoldJobs(t *testing.T, docs store.Documents) {
	t.Helper()
	project := fmt.Sprintf("prj-scaffold-%d", time.Now().UnixNano())
	job := api.ScaffoldJob{ProjectID: project, Namespace: "demo", Argv: []string{"kionga", "scaffold", "demo"}, RequestedBy: "admin"}
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, conflicts := []api.ScaffoldJob{}, 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item, err := store.CreateScaffoldJob(docs, job, "admin")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created = append(created, item)
			case errors.Is(err, store.ErrJobActive):
				conflicts++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(created) != 1 || conflicts != 5 {
		t.Fatalf("exactly one concurrent job may be created: created=%d conflicts=%d", len(created), conflicts)
	}
	first := created[0]
	if first.Status != "queued" {
		t.Fatalf("new job must be queued: %+v", first)
	}
	running, err := store.StartScaffoldJob(docs, first.ID)
	if err != nil || running.Status != "running" || running.StartedAt == nil {
		t.Fatalf("start: %+v %v", running, err)
	}
	code := 0
	done, err := store.FinishScaffoldJob(docs, first.ID, api.ScaffoldJob{Status: "succeeded", ExitCode: &code, Files: []string{"README.md"}, GitStatus: []string{"?? README.md"}}, "admin")
	if err != nil || done.Status != "succeeded" || done.EndedAt == nil || len(done.Files) != 1 {
		t.Fatalf("finish: %+v %v", done, err)
	}
	again, err := store.FinishScaffoldJob(docs, first.ID, api.ScaffoldJob{Status: "failed", Error: "late"}, "admin")
	if err != nil || again.Status != "succeeded" {
		t.Fatalf("terminal job must not be rewritten: %+v %v", again, err)
	}
	second, err := store.CreateScaffoldJob(docs, job, "admin")
	if err != nil {
		t.Fatalf("lock must be released after finish: %v", err)
	}
	stale, err := store.ScaffoldJob(docs, second.ID, second.CreatedAt.Add(store.ScaffoldJobStaleAfter+time.Second))
	if err != nil || stale.Status != "failed" || !strings.Contains(stale.Error, "interrupted") {
		t.Fatalf("stale job must fail: %+v %v", stale, err)
	}
	if jobs := store.ScaffoldJobs(docs, project, time.Now()); len(jobs) != 2 || jobs[0].ID != second.ID {
		t.Fatalf("list newest first: %+v", jobs)
	}
}
