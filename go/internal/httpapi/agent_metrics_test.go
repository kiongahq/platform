package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAgentPodMetricsOnlyCountsMatchingRuntimeContainers(t *testing.T) {
	pods := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{
		{Object: map[string]any{"metadata": map[string]any{"name": "flow-pod"}, "containers": []any{
			map[string]any{"name": "agent", "usage": map[string]any{"cpu": "250m", "memory": "128Mi"}},
			map[string]any{"name": "trace-proxy", "usage": map[string]any{"cpu": "900m", "memory": "1Gi"}},
		}}},
		{Object: map[string]any{"metadata": map[string]any{"name": "other-pod"}, "containers": []any{
			map[string]any{"name": "agent", "usage": map[string]any{"cpu": "1", "memory": "2Gi"}},
		}}},
	}}
	got := summarizeAgentPodMetrics(pods, map[string]bool{"flow-pod": true})
	if !got.Available || got.Pods != 1 || got.CPUMillicores != 250 || got.MemoryBytes != 128<<20 || got.GPUAvailable {
		t.Fatalf("wrong pod metrics or leaked another agent: %+v", got)
	}
}

func TestAgentGPUUsesOnlyOwnedPodsAndRejectsMissingSamples(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if !strings.Contains(query, `namespace="agents"`) || !strings.Contains(query, `pod=~"flow-pod"`) || strings.Contains(query, "other-pod") {
			t.Errorf("GPU query escaped owned pods: %s", query)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"value":[1792940000,"73.5"]}]}}`))
	}))
	defer server.Close()
	t.Setenv("KIONGA_GPU_METRICS_URL", server.URL)
	value, ok := queryAgentGPU(t.Context(), "agents", map[string]bool{"flow-pod": true})
	if !ok || value != 73.5 {
		t.Fatalf("GPU utilization: %.1f %v", value, ok)
	}
	t.Setenv("KIONGA_GPU_METRICS_URL", "")
	if _, ok := queryAgentGPU(t.Context(), "agents", map[string]bool{"flow-pod": true}); ok {
		t.Fatal("missing GPU telemetry must stay unavailable")
	}
}
