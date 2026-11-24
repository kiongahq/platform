package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLogEntryParsesTimestampsAndSkipsWhatTheClientHas(t *testing.T) {
	since := time.Date(2026, 11, 19, 10, 0, 0, 500_000_000, time.UTC)
	for _, tc := range []struct {
		raw, line, at string
		keep          bool
	}{
		{"2026-11-19T10:00:00.400000000Z earlier in the same second", "", "", false},
		{"2026-11-19T10:00:00.500000000Z the last line received", "", "", false},
		{"2026-11-19T10:00:00.600000000Z new line", "new line", "2026-11-19T10:00:00.6Z", true},
		{"no timestamp at all", "no timestamp at all", "", true},
	} {
		entry, keep := logEntry("pod-a", "app", tc.raw, &podResume{floor: since})
		if keep != tc.keep || (keep && (entry.Line != tc.line || entry.Time != tc.at || entry.Pod != "pod-a")) {
			t.Fatalf("%q: keep=%v entry=%+v", tc.raw, keep, entry)
		}
	}
	if entry, keep := logEntry("pod-a", "app", "2026-11-19T09:00:00Z from the tail", &podResume{}); !keep || entry.Line != "from the tail" {
		t.Fatalf("without a resume point every line is kept: %+v", entry)
	}
}

func TestLogSinceTakesTheLaterOfQueryAndLastEventID(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/x?since=2026-11-19T10:00:00Z", nil)
	request.Header.Set("Last-Event-ID", "2026-11-19T10:05:00.25Z")
	since, err := logSince(request)
	if err != nil || !since.Equal(time.Date(2026, 11, 19, 10, 5, 0, 250_000_000, time.UTC)) {
		t.Fatalf("since=%s err=%v", since, err)
	}
	if _, err := logSince(httptest.NewRequest(http.MethodGet, "/x?since=yesterday", nil)); err == nil {
		t.Fatal("an unparsable since must be rejected")
	}
}

func TestApplicationLogsFollowStreamsServerSentEvents(t *testing.T) {
	harness := newKubeHarness(t, appPod("billing-7d9-abcde", "billing", "billing-7d9"), appPod("billing-admin-5f6-xyz12", "billing-admin", "billing-admin-5f6"))
	projectID := createTestProject(t, harness.server)
	if created := harness.do(t, http.MethodPost, "/api/v1/applications", createAppBody(projectID, "")); created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	streamed := harness.do(t, http.MethodGet, "/api/v1/applications/billing/logs?follow=true&max_seconds=5", "")
	body := streamed.Body.String()
	if streamed.Code != http.StatusOK || streamed.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("follow: %d %s %s", streamed.Code, streamed.Header().Get("Content-Type"), body)
	}
	if !strings.Contains(body, "event: log\ndata: {\"pod\":\"billing-7d9-abcde\",\"container\":\"app\",\"line\":\"fake logs\"}") || strings.Contains(body, "billing-admin") {
		t.Fatalf("log events: %s", body)
	}
	if !strings.HasSuffix(body, "event: end\ndata: {\"reason\":\"all pod log streams closed\"}\n\n") {
		t.Fatalf("stream must end with a reason: %q", body)
	}
	for _, bad := range []string{"follow=true&max_seconds=0", "follow=true&max_seconds=7200", "follow=true&since=last-tuesday", "since=last-tuesday"} {
		if refused := harness.do(t, http.MethodGet, "/api/v1/applications/billing/logs?"+bad, ""); refused.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, refused.Code)
		}
	}
	resumed := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/v1/applications/billing/logs?follow=true", nil)
	request.Header.Set("Last-Event-ID", "2026-11-19T10:00:00.5Z")
	harness.server.ServeHTTP(resumed, request)
	if resumed.Code != http.StatusOK || !strings.Contains(resumed.Body.String(), "event: end") {
		t.Fatalf("resume: %d %s", resumed.Code, resumed.Body.String())
	}
}

// brokenWriter is a client that went away: every write fails.
type brokenWriter struct {
	header http.Header
	writes int
}

func (b *brokenWriter) Header() http.Header { return b.header }
func (b *brokenWriter) WriteHeader(int)     {}
func (b *brokenWriter) Write([]byte) (int, error) {
	b.writes++
	return 0, errors.New("client stopped reading")
}
func (b *brokenWriter) Flush() {}

func TestApplicationLogsFollowStopsWhenTheClientCannotBeWritten(t *testing.T) {
	newKubeHarness(t, appPod("billing-7d9-abcde", "billing", "billing-7d9"))
	access, _ := kubeAccessFactory()
	writer := &brokenWriter{header: http.Header{}}
	request := newTestRequest(http.MethodGet, "/api/v1/applications/billing/logs?follow=true&max_seconds=3600", nil)
	done := make(chan struct{})
	go func() {
		(&Server{}).streamApplicationLogs(writer, request, access, "billing", "", 10)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream kept running after a write failed")
	}
	if writer.writes != 1 {
		t.Fatalf("the stream must stop at the first failed write, wrote %d times", writer.writes)
	}
}
