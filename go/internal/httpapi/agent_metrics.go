package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

type agentLiveMetrics struct {
	Available     bool    `json:"available"`
	Pods          int     `json:"pods"`
	CPUMillicores int64   `json:"cpu_millicores"`
	MemoryBytes   int64   `json:"memory_bytes"`
	GPUAvailable  bool    `json:"gpu_utilization_available"`
	GPUPercent    float64 `json:"gpu_utilization_percent"`
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
	metrics := summarizeAgentPodMetrics(pods, allowedNames)
	if percentage, ok := queryAgentGPU(r.Context(), namespace, allowedNames); ok {
		metrics.GPUAvailable = true
		metrics.GPUPercent = percentage
	}
	writeJSON(w, 200, metrics)
}

var gpuMetricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

func queryAgentGPU(ctx context.Context, namespace string, podNames map[string]bool) (float64, bool) {
	base := strings.TrimSpace(os.Getenv("KIONGA_GPU_METRICS_URL"))
	if base == "" || len(podNames) == 0 {
		return 0, false
	}
	endpoint, err := url.Parse(base)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return 0, false
	}
	metric := os.Getenv("KIONGA_GPU_METRIC_NAME")
	if metric == "" {
		metric = "DCGM_FI_DEV_GPU_UTIL"
	}
	if !gpuMetricName.MatchString(metric) {
		return 0, false
	}
	if len(validation.IsDNS1123Label(namespace)) != 0 {
		return 0, false
	}
	names := make([]string, 0, len(podNames))
	for name := range podNames {
		if len(validation.IsDNS1123Subdomain(name)) != 0 {
			return 0, false
		}
		names = append(names, name)
	}
	sort.Strings(names)
	query := `avg(` + metric + `{namespace="` + namespace + `",pod=~"` + strings.Join(names, "|") + `"})`
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/v1/query"
	endpoint.RawQuery = url.Values{"query": {query}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, false
	}
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return 0, false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return 0, false
	}
	var result struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil || result.Status != "success" || len(result.Data.Result) != 1 || len(result.Data.Result[0].Value) != 2 {
		return 0, false
	}
	var raw string
	if err := json.Unmarshal(result.Data.Result[0].Value[1], &raw); err != nil {
		return 0, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	return value, err == nil && !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
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
