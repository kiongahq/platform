// Package storetest holds backend-agnostic conformance checks for store
// implementations, shared by unit and Postgres integration tests.
package storetest

import (
	"errors"
	"sync"
	"testing"

	"github.com/kiongahq/platform/internal/store"
)

type sampleDoc struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// Documents runs the conformance contract every Documents backend must meet:
// not-found, lost-update safety under concurrency, skip-write, list, delete.
func Documents(t *testing.T, docs store.Documents, kind string) {
	t.Helper()
	if _, err := store.GetDoc[sampleDoc](docs, kind, "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing doc: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.UpdateDoc(docs, kind, "a", func(cur sampleDoc, exists bool) (sampleDoc, error) {
				cur.ID = "a"
				cur.Version++
				return cur, nil
			}, "sample.updated", "tester")
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := store.GetDoc[sampleDoc](docs, kind, "a")
	if err != nil || got.Version != 20 {
		t.Fatalf("lost updates: %+v %v", got, err)
	}
	if _, err := store.UpdateDoc(docs, kind, "a", func(cur sampleDoc, _ bool) (sampleDoc, error) {
		return cur, store.ErrSkipWrite
	}, "x", "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateDoc(docs, kind, "b", func(cur sampleDoc, exists bool) (sampleDoc, error) {
		if exists {
			t.Error("b should not exist")
		}
		return sampleDoc{ID: "b"}, nil
	}, "sample.created", "tester"); err != nil {
		t.Fatal(err)
	}
	if all := store.ListDocs[sampleDoc](docs, kind); len(all) != 2 {
		t.Fatalf("list: %+v", all)
	}
	if err := docs.DeleteDocument(kind, "a", "sample.deleted", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := docs.DeleteDocument(kind, "a", "sample.deleted", "tester"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("double delete: %v", err)
	}
}
