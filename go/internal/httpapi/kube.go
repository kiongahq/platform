package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kiongahq/platform/pkg/api"
)

// Kubernetes mode: the gateway records a workload and creates a
// kionga.dev/v1alpha1 custom resource; the operator reconciles it and the
// gateway reads status back. No workload logic lives in the gateway.
//
// The mode is KIONGA_RUNTIME (kubernetes or compose) when set, otherwise
// automatic: a pod always has KUBERNETES_SERVICE_HOST, a Compose container
// never does.
func kubernetesMode() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KIONGA_RUNTIME"))) {
	case "kubernetes":
		return true
	case "compose":
		return false
	}
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

// workloadNamespace is where workload custom resources live: the operator's
// KIONGA_TARGET_NAMESPACE, the same namespace the integration worker uses.
func workloadNamespace() string {
	if namespace := strings.TrimSpace(os.Getenv("KIONGA_TARGET_NAMESPACE")); namespace != "" {
		return namespace
	}
	return "default"
}

func workloadTenant() string {
	if tenant := strings.TrimSpace(os.Getenv("KIONGA_TENANT")); tenant != "" {
		return tenant
	}
	return "default"
}

// kubeAccess bundles the clients for workload resources.
type kubeAccess struct {
	dynamic   dynamic.Interface
	core      kubernetes.Interface
	namespace string
}

var (
	inClusterOnce   sync.Once
	inClusterAccess *kubeAccess
	inClusterErr    error
	// kubeAccessFactory builds the clients; tests replace it with fakes.
	kubeAccessFactory = func() (*kubeAccess, error) {
		inClusterOnce.Do(func() {
			config, err := rest.InClusterConfig()
			if err != nil {
				inClusterErr = err
				return
			}
			config.Timeout = 15 * time.Second
			dynamicClient, err := dynamic.NewForConfig(config)
			if err != nil {
				inClusterErr = err
				return
			}
			core, err := kubernetes.NewForConfig(config)
			if err != nil {
				inClusterErr = err
				return
			}
			inClusterAccess = &kubeAccess{dynamic: dynamicClient, core: core}
		})
		if inClusterErr != nil {
			return nil, inClusterErr
		}
		return &kubeAccess{dynamic: inClusterAccess.dynamic, core: inClusterAccess.core, namespace: workloadNamespace()}, nil
	}
)

func (s *Server) kube() (*kubeAccess, error) { return kubeAccessFactory() }

var (
	applicationsGVR     = schema.GroupVersionResource{Group: "kionga.dev", Version: "v1alpha1", Resource: "kiongaapplications"}
	functionsGVR        = schema.GroupVersionResource{Group: "kionga.dev", Version: "v1alpha1", Resource: "kiongafunctions"}
	modelDeploymentsGVR = schema.GroupVersionResource{Group: "kionga.dev", Version: "v1alpha1", Resource: "kiongamodeldeployments"}
)

// newWorkloadObject builds a custom resource. Numbers must be int64 and
// lists []any so the object stays a valid unstructured value.
func newWorkloadObject(kind, name, namespace string, labels map[string]any, spec map[string]any) *unstructured.Unstructured {
	merged := map[string]any{"kionga.dev/tenant": workloadTenant(), "app.kubernetes.io/managed-by": "kionga-gateway"}
	for key, value := range labels {
		merged[key] = value
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kionga.dev/v1alpha1", "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": namespace, "labels": merged},
		"spec":     spec,
	}}
}

// applyWorkload creates the object or replaces its spec and labels, keeping
// status and anything else the operator owns.
func applyWorkload(ctx context.Context, client dynamic.Interface, gvr schema.GroupVersionResource, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	resources := client.Resource(gvr).Namespace(object.GetNamespace())
	existing, err := resources.Get(ctx, object.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return resources.Create(ctx, object, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	updated := existing.DeepCopy()
	updated.Object["spec"] = object.Object["spec"]
	updated.SetLabels(object.GetLabels())
	return resources.Update(ctx, updated, metav1.UpdateOptions{})
}

func deleteWorkload(ctx context.Context, client dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) error {
	err := client.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// workloadStatus is the common status block the operator writes on
// KiongaApplication, KiongaFunction and KiongaModelDeployment.
type workloadStatus struct {
	Phase         string
	URL           string
	Host          string
	ReadyReplicas int
	Message       string
	Conditions    []api.WorkloadCondition
	Raw           map[string]any
}

func readWorkloadStatus(object *unstructured.Unstructured) workloadStatus {
	status := workloadStatus{}
	if object == nil {
		return status
	}
	raw, _, _ := unstructured.NestedMap(object.Object, "status")
	status.Raw = raw
	status.Phase, _, _ = unstructured.NestedString(object.Object, "status", "phase")
	status.URL, _, _ = unstructured.NestedString(object.Object, "status", "url")
	status.Host, _, _ = unstructured.NestedString(object.Object, "status", "host")
	status.Message, _, _ = unstructured.NestedString(object.Object, "status", "message")
	if replicas, found, _ := unstructured.NestedFieldNoCopy(object.Object, "status", "readyReplicas"); found {
		status.ReadyReplicas = int(asInt64(replicas))
	}
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	for _, item := range conditions {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text := func(key string) string { value, _ := entry[key].(string); return value }
		status.Conditions = append(status.Conditions, api.WorkloadCondition{Type: text("type"), Status: text("status"), Reason: text("reason"), Message: text("message"), LastTransitionTime: text("lastTransitionTime")})
	}
	return status
}

func (status workloadStatus) condition(kind string) (api.WorkloadCondition, bool) {
	for _, condition := range status.Conditions {
		if strings.EqualFold(condition.Type, kind) {
			return condition, true
		}
	}
	return api.WorkloadCondition{}, false
}

// failureReason returns a non-empty explanation when the operator reports
// the workload as failed: phase Failed, or the named readiness condition
// False with reason Failed.
func (status workloadStatus) failureReason(readyCondition string) string {
	ready, hasReady := status.condition(readyCondition)
	failedCondition := hasReady && strings.EqualFold(ready.Status, "False") && strings.EqualFold(ready.Reason, "Failed")
	if !strings.EqualFold(status.Phase, "Failed") && !failedCondition {
		return ""
	}
	for _, candidate := range []string{ready.Message, status.Message, ready.Reason} {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return "the operator reported the workload as failed"
}

// mapApplicationStatus turns a KiongaApplication status into the API status:
// failed, ready (Ready=True), scaled-to-zero (Ready=True, no replicas while
// min is 0), or pending until the operator reports otherwise.
func mapApplicationStatus(object *unstructured.Unstructured) (state string, message string) {
	if object == nil {
		return "missing", "the KiongaApplication resource does not exist"
	}
	status := readWorkloadStatus(object)
	if reason := status.failureReason("Ready"); reason != "" {
		return "failed", reason
	}
	if ready, ok := status.condition("Ready"); ok && strings.EqualFold(ready.Status, "True") {
		if status.ReadyReplicas == 0 {
			return "scaled-to-zero", ready.Message
		}
		return "ready", ready.Message
	}
	if ready, ok := status.condition("Ready"); ok && ready.Message != "" {
		return "pending", ready.Message
	}
	return "pending", status.Message
}

func asInt64(value any) int64 {
	switch number := value.(type) {
	case int64:
		return number
	case int:
		return int64(number)
	case int32:
		return int64(number)
	case float64:
		return int64(number)
	default:
		return 0
	}
}

// notConfigured writes the 409 every Kubernetes-only feature returns when
// it cannot run, with the steps that enable it.
func writeNotConfigured(w http.ResponseWriter, message string, steps []string) {
	writeJSON(w, http.StatusConflict, map[string]any{"error": "not_configured", "message": message, "steps": steps})
}

var errKubernetesUnavailable = errors.New("the gateway cannot reach the Kubernetes API with its in-cluster service account")

// envList converts a variable map into a sorted Kubernetes env list.
func envList(values map[string]string) []any {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]any, 0, len(names))
	for _, name := range names {
		result = append(result, map[string]any{"name": name, "value": values[name]})
	}
	return result
}

func resourceRequirements(cpu, memory string) map[string]any {
	quantities := map[string]any{}
	if cpu != "" {
		quantities["cpu"] = cpu
	}
	if memory != "" {
		quantities["memory"] = memory
	}
	if len(quantities) == 0 {
		return nil
	}
	limits := map[string]any{}
	for key, value := range quantities {
		limits[key] = value
	}
	return map[string]any{"requests": quantities, "limits": limits}
}
