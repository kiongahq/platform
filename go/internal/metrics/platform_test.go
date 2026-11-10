package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeGateway(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agents":
			_, _ = w.Write([]byte(`{"items":[{"id":"agt-1","status":"ready"}],"total":1}`))
		case "/api/v1/agents/agt-1/usage":
			_, _ = w.Write([]byte(`{"sessions":4,"active":1,"input_tokens":1200,"output_tokens":600,"cost_usd":0.42}`))
		case "/api/v1/pipelines/runs":
			_, _ = w.Write([]byte(`[{"status":"succeeded"},{"status":"succeeded"},{"status":"failed"}]`))
		case "/api/v1/realtime":
			_, _ = w.Write([]byte(`{"demos":{"fraud":{"events":10,"flagged":2,"avg_latency_ms":4.2}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestPlatformCollectorAggregatesGatewayState(t *testing.T) {
	gateway := fakeGateway(t)
	defer gateway.Close()
	collector := NewPlatformCollector(gateway.URL, "")
	if err := collector.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	exposition := collector.Prometheus()
	for _, expected := range []string{
		`kionga_agent_active_sessions{agent="agt-1"} 1`,
		`kionga_llm_tokens_used_total{agent="agt-1",type="input"} 1200`,
		`kionga_llm_cost_usd_total{agent="agt-1"} 0.42`,
		`kionga_pipeline_runs_total{status="succeeded"} 2`,
		`kionga_pipeline_runs_total{status="failed"} 1`,
		`kionga_realtime_events_total{demo="fraud"} 10`,
		`kionga_realtime_avg_latency_ms{demo="fraud"} 4.2`,
	} {
		if !strings.Contains(exposition, expected) {
			t.Fatalf("missing metric %q in:\n%s", expected, exposition)
		}
	}
}

func TestPlatformCollectorNoGatewayIsNoop(t *testing.T) {
	collector := NewPlatformCollector("", "")
	if err := collector.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(collector.Prometheus(), "kionga_agent") {
		t.Fatal("no data expected without a gateway")
	}
}
