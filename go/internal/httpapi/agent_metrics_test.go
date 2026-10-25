package httpapi

import (
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
