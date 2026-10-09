package logexport

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// fakeBulk records NDJSON requests and answers with scripted item statuses.
type fakeBulk struct {
	mu       sync.Mutex
	actions  []map[string]map[string]string
	docs     []Document
	auth     []string
	statuses func(call int, docs []Document) []int
	calls    int
	fail     int // HTTP status for the whole request, 0 = OK
}

func (f *fakeBulk) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if r.URL.Path != "/_bulk" || r.Header.Get("Content-Type") != "application/x-ndjson" {
			t.Errorf("unexpected request %s %s", r.URL.Path, r.Header.Get("Content-Type"))
		}
		if f.fail != 0 {
			w.WriteHeader(f.fail)
			return
		}
		scanner := bufio.NewScanner(r.Body)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		var docs []Document
		for scanner.Scan() {
			var action map[string]map[string]string
			_ = json.Unmarshal(scanner.Bytes(), &action)
			f.actions = append(f.actions, action)
			scanner.Scan()
			var doc Document
			_ = json.Unmarshal(scanner.Bytes(), &doc)
			docs = append(docs, doc)
		}
		f.docs = append(f.docs, docs...)
		statuses := make([]int, len(docs))
		for i := range statuses {
			statuses[i] = 201
		}
		if f.statuses != nil {
			statuses = f.statuses(f.calls, docs)
		}
		items := []map[string]any{}
		failed := false
		for _, status := range statuses {
			item := map[string]any{"status": status}
			if status >= 300 {
				failed = true
				item["error"] = map[string]string{"type": "mapper_parsing_exception", "reason": "bad field"}
			}
			items = append(items, map[string]any{"create": item})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": failed, "items": items})
	})
}

func exporterFor(t *testing.T, target string, fake *fakeBulk, extra func(*Config)) (*Exporter, *store.Store) {
	t.Helper()
	server := httptest.NewServer(fake.handler(t))
	t.Cleanup(server.Close)
	config := Config{Target: target, URL: server.URL, Tenant: "Acme Corp", APIKey: "es-api-key-secret", Timeout: 5 * time.Second}
	if extra != nil {
		extra(&config)
	}
	sink, err := NewSink(config)
	if err != nil {
		t.Fatal(err)
	}
	data := store.New()
	var entries []api.LogEntry
	for i := 0; i < 5; i++ {
		entries = append(entries, api.LogEntry{Timestamp: time.Date(2026, 10, 9, 10, 0, i, 0, time.UTC), ProjectID: "prj-1", RunID: "run-1", Node: "train", Source: "runner", Severity: "info", Message: "line"})
	}
	_ = data.AppendLogs(entries)
	return &Exporter{Logs: data, Docs: data, Sink: sink, Tenant: "Acme Corp", BatchSize: 10, Sleep: func(time.Duration) {}}, data
}

func TestElasticsearchWritesToTheTenantDataStreamWithAPIKey(t *testing.T) {
	fake := &fakeBulk{}
	exporter, _ := exporterFor(t, "elasticsearch", fake, nil)
	handled, err := exporter.ExportOnce(context.Background())
	if err != nil || handled != 5 {
		t.Fatalf("export: %d %v", handled, err)
	}
	if fake.actions[0]["create"]["_index"] != "logs-kionga.acme-corp-default" || fake.auth[0] != "ApiKey es-api-key-secret" {
		t.Fatalf("action %v auth %q", fake.actions[0], fake.auth[0])
	}
	doc := fake.docs[0]
	if doc.Kionga["tenant"] != "Acme Corp" || doc.Kionga["run_id"] != "run-1" || doc.Log["level"] != "info" || doc.Event["dataset"] != "kionga.runner" {
		t.Fatalf("document: %+v", doc)
	}
	if status := exporter.Status(); status.State != "healthy" || status.Exported != 5 || status.Cursor != 5 {
		t.Fatalf("status: %+v", status)
	}
	if handled, _ := exporter.ExportOnce(context.Background()); handled != 0 {
		t.Fatal("exported the same entries twice")
	}
}

func TestOpenSearchUsesDailyIndicesBasicAuthAndStableIDs(t *testing.T) {
	fake := &fakeBulk{}
	exporter, _ := exporterFor(t, "opensearch", fake, func(c *Config) { c.APIKey, c.Username, c.Password = "", "exporter", "os-password" })
	if (Config{Target: "opensearch", URL: "https://os.example"}).Validate() == nil {
		t.Fatal("unauthenticated target accepted without explicit opt-in")
	}
	if (Config{Target: "opensearch", URL: "http://opensearch:9200", Anonymous: true}).Validate() != nil {
		t.Fatal("explicit anonymous test cluster rejected")
	}
	if _, err := exporter.ExportOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	action := fake.actions[0]["index"]
	if action["_index"] != "kionga-logs-acme-corp-2026.10.09" || action["_id"] != "Acme Corp-1" || !strings.HasPrefix(fake.auth[0], "Basic ") {
		t.Fatalf("opensearch action %v auth %q", action, fake.auth[0])
	}
}

func TestOnlyFailedItemsAreRetriedAndPermanentFailuresDeadLetter(t *testing.T) {
	fake := &fakeBulk{statuses: func(call int, docs []Document) []int {
		out := make([]int, len(docs))
		for i := range out {
			out[i] = 201
		}
		if call == 1 {
			out[1], out[3] = 429, 400 // one retryable, one permanent
		}
		return out
	}}
	exporter, _ := exporterFor(t, "elasticsearch", fake, nil)
	if _, err := exporter.ExportOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 2 || len(fake.docs) != 6 {
		t.Fatalf("calls %d documents sent %d (want 2 calls, 5+1)", fake.calls, len(fake.docs))
	}
	status := exporter.Status()
	if status.Exported != 4 || status.DeadLettered != 1 || !strings.Contains(status.LastError, "dead-lettered") {
		t.Fatalf("status: %+v", status)
	}
}

func TestTargetOutageKeepsTheCursorAndDegrades(t *testing.T) {
	fake := &fakeBulk{fail: http.StatusServiceUnavailable}
	exporter, _ := exporterFor(t, "elasticsearch", fake, nil)
	for i := 0; i < 5; i++ {
		if _, err := exporter.ExportOnce(context.Background()); err == nil {
			t.Fatal("outage not reported")
		}
		if i == 0 && exporter.Status().State != "degraded" {
			t.Fatalf("first failure state: %s", exporter.Status().State)
		}
	}
	status := exporter.Status()
	if status.State != "unavailable" || status.Cursor != 0 {
		t.Fatalf("status after outage: %+v", status)
	}
	fake.fail = 0
	if handled, err := exporter.ExportOnce(context.Background()); err != nil || handled != 5 || exporter.Status().State != "healthy" {
		t.Fatalf("recovery: %d %v %+v", handled, err, exporter.Status())
	}
}

func TestCredentialsNeverAppearInErrorsOrConfig(t *testing.T) {
	fake := &fakeBulk{fail: http.StatusUnauthorized}
	exporter, _ := exporterFor(t, "elasticsearch", fake, nil)
	_, err := exporter.ExportOnce(context.Background())
	if err == nil || strings.Contains(err.Error(), "es-api-key-secret") || !strings.Contains(err.Error(), "rejected the credentials") {
		t.Fatalf("auth error: %v", err)
	}
	for _, bad := range []Config{
		{Target: "splunk", URL: "https://x"},
		{Target: "elasticsearch", URL: "https://user:pass@es.example"},
		{Target: "elasticsearch", URL: "http://es.example", APIKey: "k"},
		{Target: "elasticsearch", URL: "https://es.example"},
	} {
		if err := bad.Validate(); err == nil || strings.Contains(err.Error(), "pass") {
			t.Errorf("config %+v accepted or leaked: %v", bad, err)
		}
	}
	if SafeTenant("../ACME!!") != "acme" || SafeTenant("") != "default" {
		t.Fatal("tenant sanitization")
	}
}
