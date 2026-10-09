package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	platformapi "github.com/kiongahq/platform/pkg/api"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

type LifecycleCommand struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Action   string          `json:"action"`
	Resource json.RawMessage `json:"resource"`
	Tenant   string          `json:"tenant"`
}

type Dispatcher struct {
	client    dynamic.Interface
	namespace string
}

func NewDispatcher(client dynamic.Interface, namespace string) *Dispatcher {
	return &Dispatcher{client: client, namespace: namespace}
}

func (d *Dispatcher) Dispatch(ctx context.Context, record KafkaRecord) error {
	var command LifecycleCommand
	if err := json.Unmarshal(record.Value, &command); err != nil {
		return err
	}
	var resource map[string]any
	if err := json.Unmarshal(command.Resource, &resource); err != nil {
		return err
	}
	name, _ := resource["name"].(string)
	if name == "" {
		name = command.ID
	}
	var plural, kind string
	var spec map[string]any
	switch command.Kind {
	case "pipeline_run":
		plural, kind = "kiongapipelineruns", "KiongaPipelineRun"
		spec = map[string]any{"pipelineRef": resource["name"], "parameters": map[string]any{"project_id": resource["project_id"]}}
	case "model":
		if command.Action != "model.promoted" {
			return nil
		}
		plural, kind = "kiongamodelpromotions", "KiongaModelPromotion"
		spec = map[string]any{"modelName": resource["name"], "version": resource["version"], "targetStage": resource["stage"]}
	case "agent":
		plural, kind = "kiongaagents", "KiongaAgent"
		name = platformapi.AgentDNSName(command.ID)
		autoscaling, _ := resource["autoscaling"].(map[string]any)
		minimum, maximum := integer(autoscaling["min_replicas"]), integer(autoscaling["max_replicas"])
		if minimum < 1 {
			minimum = integer(resource["replicas"])
		}
		if maximum < minimum {
			maximum = minimum
		}
		spec = map[string]any{
			"version": resource["version"], "image": resource["image"], "graphModule": resource["graph_module"],
			"replicas": map[string]any{"min": minimum, "max": maximum},
			"llm":      map[string]any{"backend": resource["llm_backend"]}, "tools": toolRefs(resource["tools"]),
			"trafficPolicy": map[string]any{"canaryWeight": resource["canary_weight"]},
		}
		resources, err := agentResourceRequirements(resource["resources"])
		if err != nil {
			return fmt.Errorf("invalid agent resources: %w", err)
		}
		if resources != nil {
			spec["resources"] = resources
		}
	case "tool":
		plural, kind = "kiongatools", "KiongaTool"
		spec = map[string]any{"version": resource["version"], "description": resource["description"], "tags": resource["tags"], "inputSchema": resource["input_schema"]}
	case "connection":
		plural, kind = "kiongaconnections", "KiongaConnection"
		spec = map[string]any{"type": resource["type"], "endpoint": resource["endpoint"], "secretRef": map[string]any{"name": resource["secret_ref"]}}
	case "user_access":
		plural, kind = "kiongaworkspaces", "KiongaWorkspace"
		name = "workspace-" + command.ID
		compute, _ := resource["compute"].(map[string]any)
		storage, _ := resource["storage"].(map[string]any)
		spec = map[string]any{
			"subject": resource["subject"], "services": resource["services"], "disabled": resource["disabled"],
			"compute":   map[string]any{"vcpus": integer(compute["vcpus"]), "memoryGB": integer(compute["memory_gb"]), "gpus": integer(compute["gpus"]), "gpuType": compute["gpu_type"], "maxVMs": integer(compute["max_vms"])},
			"storageGB": integer(storage["size_gb"]),
		}
	default:
		return fmt.Errorf("unsupported lifecycle kind %q", command.Kind)
	}
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "mlaiops.io/v1alpha1", "kind": kind,
		"metadata": map[string]any{"name": dnsName(name, command.ID), "namespace": d.namespace, "labels": map[string]any{"mlaiops.io/tenant": command.Tenant, "mlaiops.io/resource-id": command.ID}},
		"spec":     spec,
	}}
	gvr := schema.GroupVersionResource{Group: "mlaiops.io", Version: "v1alpha1", Resource: plural}
	resources := d.client.Resource(gvr).Namespace(d.namespace)
	if command.Kind == "user_access" && command.Action == "access.deleted" {
		err := resources.Delete(ctx, object.GetName(), metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	existing, err := resources.Get(ctx, object.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = resources.Create(ctx, object, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	object.SetResourceVersion(existing.GetResourceVersion())
	_, err = resources.Update(ctx, object, metav1.UpdateOptions{})
	return err
}

func toolRefs(value any) []any {
	values, _ := value.([]any)
	result := make([]any, 0, len(values))
	for _, item := range values {
		result = append(result, map[string]any{"name": item, "version": "latest"})
	}
	return result
}

func agentResourceRequirements(value any) (map[string]any, error) {
	resources, _ := value.(map[string]any)
	quantities := map[string]any{}
	if cpu, _ := resources["cpu"].(string); cpu != "" {
		quantities["cpu"] = cpu
	}
	if memory, _ := resources["memory"].(string); memory != "" {
		quantities["memory"] = memory
	}
	if gpu := integer(resources["gpu"]); gpu > 0 {
		gpuType, _ := resources["gpu_type"].(string)
		gpuType = strings.TrimSpace(gpuType)
		if gpuType == "" {
			gpuType = "nvidia.com/gpu"
		}
		// Lifecycle events are an external trust boundary. Even if an event
		// bypassed API/store normalization, a forged GPU key must never replace
		// native CPU or memory quantities in the Kubernetes resource map.
		switch gpuType {
		case "cpu", "memory", "ephemeral-storage", "storage":
			return nil, fmt.Errorf("gpu_type %q collides with a native Kubernetes resource", gpuType)
		}
		quantities[gpuType] = gpu
	}
	if len(quantities) == 0 {
		return nil, nil
	}
	requests, limits := map[string]any{}, map[string]any{}
	for name, quantity := range quantities {
		requests[name], limits[name] = quantity, quantity
	}
	return map[string]any{"requests": requests, "limits": limits}, nil
}

func integer(value any) int64 {
	switch number := value.(type) {
	case float64:
		return int64(number)
	case int64:
		return number
	case int:
		return int64(number)
	default:
		return 0
	}
}

func dnsName(name, fallback string) string {
	if name == "" {
		name = fallback
	}
	raw := strings.ToLower(name)
	value := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(raw, "-"), "-")
	if value == "" {
		value = "resource"
	}
	if len(value) <= 63 {
		return value
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))[:8]
	return strings.Trim(value[:54], "-") + "-" + digest
}
