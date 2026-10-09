package operator

import (
	"errors"
	"strings"

	platformapi "github.com/kiongahq/platform/pkg/api"
)

type AgentSpec struct {
	ResourceID      string
	Name            string
	Namespace       string
	Version         string
	Image           string
	GraphModule     string
	MinReplicas     int
	MaxReplicas     int
	LLMBackend      string
	LangfuseProject string
	CanaryWeight    int
	StableRef       string
}

type Container struct {
	Name  string            `json:"name"`
	Image string            `json:"image"`
	Port  int               `json:"port"`
	Env   map[string]string `json:"env"`
}

type AgentWorkload struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Labels      map[string]string `json:"labels"`
	Replicas    int               `json:"replicas"`
	Containers  []Container       `json:"containers"`
	Annotations map[string]string `json:"annotations"`
}

type TrafficRoute struct {
	StableDestination string `json:"stable_destination"`
	CanaryDestination string `json:"canary_destination"`
	StableWeight      int    `json:"stable_weight"`
	CanaryWeight      int    `json:"canary_weight"`
}

type AgentPlan struct {
	Workload AgentWorkload `json:"workload"`
	Traffic  TrafficRoute  `json:"traffic"`
}

func ReconcileAgent(spec AgentSpec) (AgentPlan, error) {
	if strings.TrimSpace(spec.Name) == "" || strings.TrimSpace(spec.Namespace) == "" {
		return AgentPlan{}, errors.New("name and namespace are required")
	}
	if spec.Image == "" || spec.Version == "" || spec.GraphModule == "" {
		return AgentPlan{}, errors.New("image, version and graph module are required")
	}
	if spec.CanaryWeight < 0 || spec.CanaryWeight > 100 {
		return AgentPlan{}, errors.New("canary weight must be between 0 and 100")
	}
	if spec.MinReplicas < 1 {
		spec.MinReplicas = 1
	}
	if spec.MaxReplicas < spec.MinReplicas {
		return AgentPlan{}, errors.New("max replicas must be greater than or equal to min replicas")
	}
	resourceID := spec.ResourceID
	if resourceID == "" {
		resourceID = spec.Name
	}
	name := platformapi.AgentDNSName(resourceID)
	labels := map[string]string{
		"app.kubernetes.io/name":       spec.Name,
		"app.kubernetes.io/component":  "agent",
		"app.kubernetes.io/managed-by": "mlaiops-operator",
		"mlaiops.io/version":           spec.Version,
	}
	env := map[string]string{
		"MLAIOPS_AGENT_NAME":       spec.Name,
		"MLAIOPS_AGENT_VERSION":    spec.Version,
		"MLAIOPS_GRAPH_MODULE":     spec.GraphModule,
		"MLAIOPS_LLM_BACKEND":      spec.LLMBackend,
		"MLAIOPS_LANGFUSE_PROJECT": spec.LangfuseProject,
		"OPENAI_BASE_URL":          "http://localhost:8081/v1",
	}
	plan := AgentPlan{
		Workload: AgentWorkload{
			Name: name, Namespace: spec.Namespace, Labels: labels, Replicas: spec.MinReplicas,
			Annotations: map[string]string{"mlaiops.io/inject-trace-proxy": "true"},
			Containers: []Container{
				{Name: "agent", Image: spec.Image, Port: 8080, Env: env},
				{Name: "trace-proxy", Image: "ghcr.io/mlaiops/trace-proxy:latest", Port: 8081, Env: map[string]string{"MLAIOPS_AGENT_NAME": spec.Name}},
			},
		},
		Traffic: TrafficRoute{
			StableDestination: spec.StableRef, CanaryDestination: name,
			StableWeight: 100 - spec.CanaryWeight, CanaryWeight: spec.CanaryWeight,
		},
	}
	if spec.StableRef == "" {
		plan.Traffic.StableDestination = name
		plan.Traffic.StableWeight = 100
		plan.Traffic.CanaryWeight = 0
	}
	return plan, nil
}
