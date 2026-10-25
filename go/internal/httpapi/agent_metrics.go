package httpapi

import (
	"net/http"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type agentLiveMetrics struct {
	Available     bool  `json:"available"`
	Pods          int   `json:"pods"`
	CPUMillicores int64 `json:"cpu_millicores"`
	MemoryBytes   int64 `json:"memory_bytes"`
	GPUAvailable  bool  `json:"gpu_utilization_available"`
}

var agentMetricsClient = langflowClient

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/agents/{id}/metrics", s.agentMetrics)
	})
}

func (s *Server) agentMetrics(w http.ResponseWriter, r *http.Request) {
	if !agentAllowed(s.store, principal(r), r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not_found", "agent not found")
		return
	}
	client, namespace, err := agentMetricsClient()
	if err != nil {
		writeJSON(w, 200, agentLiveMetrics{})
		return
	}
	agents, err := client.Resource(schema.GroupVersionResource{Group: "mlaiops.io", Version: "v1alpha1", Resource: "kiongaagents"}).Namespace(namespace).List(r.Context(), metav1.ListOptions{LabelSelector: "mlaiops.io/resource-id=" + r.PathValue("id")})
	if err != nil {
		writeJSON(w, 200, agentLiveMetrics{})
		return
	}
	name := ""
	for _, item := range agents.Items {
		if item.GetLabels()["mlaiops.io/resource-id"] == r.PathValue("id") {
			name = item.GetName()
			break
		}
	}
	if name == "" {
		writeJSON(w, 200, agentLiveMetrics{})
		return
	}
	ownedPods, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(namespace).List(r.Context(), metav1.ListOptions{LabelSelector: "mlaiops.io/agent=" + name})
	if err != nil {
		writeJSON(w, 200, agentLiveMetrics{})
		return
	}
	allowedNames := map[string]bool{}
	for _, pod := range ownedPods.Items {
		if pod.GetLabels()["mlaiops.io/agent"] == name {
			allowedNames[pod.GetName()] = true
		}
	}
	pods, err := client.Resource(schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}).Namespace(namespace).List(r.Context(), metav1.ListOptions{LabelSelector: "mlaiops.io/agent=" + name})
	if err != nil {
		writeJSON(w, 200, agentLiveMetrics{})
		return
	}
	writeJSON(w, 200, summarizeAgentPodMetrics(pods, allowedNames))
}

func summarizeAgentPodMetrics(pods *unstructured.UnstructuredList, allowedNames map[string]bool) agentLiveMetrics {
	metrics := agentLiveMetrics{Available: true}
	for _, pod := range pods.Items {
		if !allowedNames[pod.GetName()] {
			continue
		}
		containers, _, _ := unstructured.NestedSlice(pod.Object, "containers")
		for _, value := range containers {
			container, ok := value.(map[string]any)
			if !ok || container["name"] != "agent" {
				continue
			}
			usage, ok := container["usage"].(map[string]any)
			if !ok {
				continue
			}
			cpu, cpuOK := usage["cpu"].(string)
			memory, memoryOK := usage["memory"].(string)
			if !cpuOK || !memoryOK {
				continue
			}
			cpuQuantity, cpuErr := resource.ParseQuantity(strings.TrimSpace(cpu))
			memoryQuantity, memoryErr := resource.ParseQuantity(strings.TrimSpace(memory))
			if cpuErr != nil || memoryErr != nil {
				continue
			}
			metrics.Pods++
			metrics.CPUMillicores += cpuQuantity.MilliValue()
			metrics.MemoryBytes += memoryQuantity.Value()
		}
	}
	return metrics
}
