package storetest

import (
	"fmt"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// Logs checks append, filtering, cursors and retention for a LogStore.
func Logs(t *testing.T, logs store.LogStore, runID string) {
	t.Helper()
	base := time.Now().UTC().Add(-time.Hour)
	var entries []api.LogEntry
	for i := 0; i < 25; i++ {
		severity := "info"
		if i%5 == 0 {
			severity = "error"
		}
		entries = append(entries, api.LogEntry{Timestamp: base.Add(time.Duration(i) * time.Second), ProjectID: "prj-a", RunID: runID, Node: fmt.Sprintf("n%d", i%3), Source: "runner", Severity: severity, Message: fmt.Sprintf("line %02d epoch", i)})
	}
	entries = append(entries, api.LogEntry{Timestamp: base, ProjectID: "prj-b", RunID: runID + "-other", Source: "runner", Severity: "info", Message: "other project"})
	if err := logs.AppendLogs(entries); err != nil {
		t.Fatal(err)
	}
	page, err := logs.QueryLogs(api.LogFilter{RunID: runID, Limit: 10})
	if err != nil || len(page) != 10 {
		t.Fatalf("first page: %d %v", len(page), err)
	}
	next, _ := logs.QueryLogs(api.LogFilter{RunID: runID, Limit: 10, After: page[len(page)-1].Sequence})
	if len(next) != 10 || next[0].Sequence <= page[9].Sequence || next[0].Message != "line 10 epoch" {
		t.Fatalf("cursor page: %+v", next[:1])
	}
	errors, _ := logs.QueryLogs(api.LogFilter{RunID: runID, Severities: []string{"error"}, Limit: 100})
	if len(errors) != 5 {
		t.Fatalf("severity filter: %d", len(errors))
	}
	node, _ := logs.QueryLogs(api.LogFilter{RunID: runID, Node: "n1", Query: "EPOCH", Limit: 100})
	if len(node) != 8 {
		t.Fatalf("node+search filter: %d", len(node))
	}
	scoped, _ := logs.QueryLogs(api.LogFilter{ProjectIDs: []string{"prj-b"}, Limit: 100})
	for _, entry := range scoped {
		if entry.ProjectID != "prj-b" {
			t.Fatalf("project filter leaked %+v", entry)
		}
	}
	latest, _ := logs.QueryLogs(api.LogFilter{RunID: runID, Limit: 1, Descending: true})
	if len(latest) != 1 || latest[0].Message != "line 24 epoch" {
		t.Fatalf("descending: %+v", latest)
	}
	since := base.Add(20 * time.Second)
	recent, _ := logs.QueryLogs(api.LogFilter{RunID: runID, Since: &since, Limit: 100})
	if len(recent) != 5 {
		t.Fatalf("since filter: %d", len(recent))
	}
	if _, err := logs.PurgeLogs(time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if rest, _ := logs.QueryLogs(api.LogFilter{RunID: runID, Limit: 100}); len(rest) != 0 {
		t.Fatalf("retention left %d entries", len(rest))
	}
}
