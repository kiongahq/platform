package httpapi

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEventsStreamsDigest(t *testing.T) {
	server := httptest.NewServer(testServer())
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/events", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("unexpected content type %q", got)
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "data: ") || !strings.Contains(line, `"runs"`) {
		t.Fatalf("unexpected first SSE frame: %q", line)
	}
}

func TestRealtimeReportAndRead(t *testing.T) {
	server := testServer()
	report := newTestRequest(http.MethodPost, "/api/v1/realtime/fraud", strings.NewReader(`{"events":12,"flagged":3,"avg_latency_ms":4.5}`))
	reported := httptest.NewRecorder()
	server.ServeHTTP(reported, report)
	if reported.Code != http.StatusOK {
		t.Fatalf("report failed: %d %s", reported.Code, reported.Body.String())
	}
	read := httptest.NewRecorder()
	server.ServeHTTP(read, newTestRequest(http.MethodGet, "/api/v1/realtime", nil))
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"events":12`) {
		t.Fatalf("read failed: %d %s", read.Code, read.Body.String())
	}
	if !strings.Contains(read.Body.String(), `"updated_at"`) {
		t.Fatalf("timestamp missing: %s", read.Body.String())
	}
}
