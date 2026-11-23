package httpapi

import (
	"bytes"
	"context"
	"hash/fnv"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/kiongahq/platform/pkg/api"
)

// Model deployment on Kubernetes: Deploy records the model as pending and
// applies a KiongaModelDeployment (one per model name and project). The
// operator resolves the version's artifact through MLflow and creates a
// KServe InferenceService in raw mode; a canary is a second
// InferenceService. The gateway reads Ready / CanaryReady and the endpoint
// URLs back, and splits model invocations by the recorded canary weight.

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/models/{id}/deployment", s.modelDeployment)
	})
	openapiPaths["/api/v1/models/{id}/deployment"] = map[string]any{"get": map[string]any{"summary": "Live deployment status of a model version: pending, serving (with endpoint_url) or failed (with deployment_message)"}}
}

// modelServingSteps tell an administrator how to make Deploy work.
var modelServingSteps = []string{
	"On Kubernetes: install KServe in RawDeployment mode with the Helm chart prerequisites; Deploy then creates a KiongaModelDeployment.",
	"On Compose: run the serving manager and set SERVING_MANAGER_URL on the gateway.",
}

// liveDeploymentStatuses are the statuses refreshed from the cluster.
var liveDeploymentStatuses = []string{"pending", "deploying", "serving", "failed"}

func modelDeploymentSpec(name, version, canaryVersion string, weight int) map[string]any {
	spec := map[string]any{"modelName": name, "version": version, "canaryWeight": int64(0)}
	if canaryVersion != "" && weight > 0 && weight < 100 {
		spec["canaryVersion"] = canaryVersion
		spec["canaryWeight"] = int64(weight)
	}
	return spec
}

func modelDeploymentObject(model api.Model, namespace string, spec map[string]any) *unstructured.Unstructured {
	return newWorkloadObject("KiongaModelDeployment", api.ModelDeploymentDNSName(model.ProjectID, model.Name), namespace,
		map[string]any{"kionga.dev/project": model.ProjectID, "kionga.dev/model-deployment": "true"}, spec)
}

func findModel(models []api.Model, id string) (api.Model, bool) {
	for _, item := range models {
		if item.ID == id {
			return item, true
		}
	}
	return api.Model{}, false
}

func (s *Server) deployKubernetesModel(w http.ResponseWriter, r *http.Request, id string, weight int) {
	model, found := findModel(s.store.Models(), id)
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	}
	if weight < 0 || weight > 100 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "canary_weight must be between 0 and 100")
		return
	}
	if model.GateStatus != "passed" {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "model evaluation gates have not passed")
		return
	}
	access, err := s.kube()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "kubernetes_unavailable", errKubernetesUnavailable.Error())
		return
	}
	if weight == 100 {
		weight = 0 // all traffic to this version: a full rollout, not a canary
	}
	spec := modelDeploymentSpec(model.Name, model.Version, "", 0)
	if weight > 0 {
		stable, ok := s.stableVersion(r.Context(), access, model)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "validation_error", "a canary needs a serving stable version of "+model.Name+"; deploy one with canary_weight 0 first")
			return
		}
		spec = modelDeploymentSpec(model.Name, stable.Version, model.Version, weight)
	}
	if _, err := applyWorkload(r.Context(), access.dynamic, modelDeploymentsGVR, modelDeploymentObject(model, access.namespace, spec)); err != nil {
		writeError(w, http.StatusBadGateway, "deploy_failed", "Kubernetes rejected the model deployment: "+err.Error())
		return
	}
	item, err := s.store.DeployModel(model.ID, weight, actor(r))
	writeMutation(w, item, err, http.StatusAccepted)
}

// stableVersion is the version the model's deployment serves today, when it
// is serving and is not the model being deployed.
func (s *Server) stableVersion(ctx context.Context, access *kubeAccess, model api.Model) (api.Model, bool) {
	object, err := access.dynamic.Resource(modelDeploymentsGVR).Namespace(access.namespace).Get(ctx, api.ModelDeploymentDNSName(model.ProjectID, model.Name), metav1.GetOptions{})
	if err != nil {
		return api.Model{}, false
	}
	version, _, _ := unstructured.NestedString(object.Object, "spec", "version")
	for _, candidate := range s.store.Models() {
		if candidate.ProjectID == model.ProjectID && candidate.Name == model.Name && candidate.Version == version && candidate.ID != model.ID {
			candidate = mapModelDeployment(candidate, object)
			return candidate, candidate.DeploymentStatus == "serving"
		}
	}
	return api.Model{}, false
}

// mapModelDeployment derives one model version's deployment status from
// the KiongaModelDeployment for its name: serving only when the operator
// reports the version's InferenceService Ready with an endpoint URL.
func mapModelDeployment(model api.Model, object *unstructured.Unstructured) api.Model {
	if object == nil {
		if model.DeploymentStatus == "failed" {
			return model
		}
		model.DeploymentStatus, model.EndpointURL, model.DeploymentMessage = "stopped", "", "the KiongaModelDeployment resource does not exist"
		return model
	}
	version, _, _ := unstructured.NestedString(object.Object, "spec", "version")
	canaryVersion, _, _ := unstructured.NestedString(object.Object, "spec", "canaryVersion")
	status := readWorkloadStatus(object)
	readyCondition, url := "Ready", status.URL
	switch model.Version {
	case version:
	case canaryVersion:
		readyCondition = "CanaryReady"
		url, _, _ = unstructured.NestedString(object.Object, "status", "canaryURL")
	default:
		model.DeploymentStatus, model.EndpointURL, model.DeploymentMessage = "retired", "", "replaced by version "+version
		return model
	}
	if reason := status.failureReason(readyCondition); reason != "" {
		model.DeploymentStatus, model.EndpointURL, model.DeploymentMessage = "failed", "", reason
		return model
	}
	ready, ok := status.condition(readyCondition)
	switch {
	case ok && strings.EqualFold(ready.Status, "True") && strings.HasPrefix(url, "http"):
		model.DeploymentStatus, model.EndpointURL, model.DeploymentMessage = "serving", url, ""
	case ok && strings.EqualFold(ready.Status, "True"):
		model.DeploymentStatus, model.EndpointURL, model.DeploymentMessage = "pending", "", "ready but no endpoint URL has been reported yet"
	default:
		message := "waiting for the model server to become ready"
		if ok && ready.Message != "" {
			message = ready.Message
		} else if status.Message != "" {
			message = status.Message
		}
		model.DeploymentStatus, model.EndpointURL, model.DeploymentMessage = "pending", "", message
	}
	return model
}

// refreshModelDeployments reads every KiongaModelDeployment once and
// updates the models whose deployment is live. Changes are persisted.
func (s *Server) refreshModelDeployments(ctx context.Context, models []api.Model) ([]api.Model, error) {
	if !kubernetesMode() || !slices.ContainsFunc(models, func(m api.Model) bool { return slices.Contains(liveDeploymentStatuses, m.DeploymentStatus) }) {
		return models, nil
	}
	access, err := s.kube()
	if err != nil {
		return models, errKubernetesUnavailable
	}
	list, err := access.dynamic.Resource(modelDeploymentsGVR).Namespace(access.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return models, err
	}
	live := map[string]*unstructured.Unstructured{}
	for i := range list.Items {
		live[list.Items[i].GetName()] = &list.Items[i]
	}
	for i, model := range models {
		if !slices.Contains(liveDeploymentStatuses, model.DeploymentStatus) {
			continue
		}
		next := mapModelDeployment(model, live[api.ModelDeploymentDNSName(model.ProjectID, model.Name)])
		if next.DeploymentStatus != model.DeploymentStatus || next.EndpointURL != model.EndpointURL || next.DeploymentMessage != model.DeploymentMessage {
			if _, err := s.store.SetModelEndpoint(model.ID, next.EndpointURL, next.DeploymentStatus, next.DeploymentMessage); err != nil {
				log.Printf("record model %s deployment status: %v", model.ID, err)
			}
		}
		models[i] = next
	}
	return models, nil
}

func (s *Server) modelDeployment(w http.ResponseWriter, r *http.Request) {
	if !modelAllowed(s.store, principal(r), r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	}
	model, _ := findModel(s.store.Models(), r.PathValue("id"))
	refreshed, err := s.refreshModelDeployments(r.Context(), []api.Model{model})
	if err != nil {
		writeError(w, http.StatusBadGateway, "kubernetes_unavailable", "reading model deployment status failed: "+err.Error())
		return
	}
	model = refreshed[0]
	writeJSON(w, http.StatusOK, map[string]any{
		"model_id": model.ID, "name": model.Name, "version": model.Version, "status": model.DeploymentStatus,
		"endpoint_url": model.EndpointURL, "message": model.DeploymentMessage, "canary_weight": model.CanaryWeight,
	})
}

// rollbackKubernetesModel removes a rolled-back version from serving: a
// canary leaves the deployment (all traffic returns to stable); a stable
// version without a canary deletes the deployment.
func (s *Server) rollbackKubernetesModel(ctx context.Context, model api.Model) (int, string) {
	access, err := s.kube()
	if err != nil {
		return http.StatusServiceUnavailable, errKubernetesUnavailable.Error()
	}
	resources := access.dynamic.Resource(modelDeploymentsGVR).Namespace(access.namespace)
	object, err := resources.Get(ctx, api.ModelDeploymentDNSName(model.ProjectID, model.Name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return 0, ""
	}
	if err != nil {
		return http.StatusBadGateway, err.Error()
	}
	version, _, _ := unstructured.NestedString(object.Object, "spec", "version")
	canaryVersion, _, _ := unstructured.NestedString(object.Object, "spec", "canaryVersion")
	switch model.Version {
	case canaryVersion:
		if _, err := applyWorkload(ctx, access.dynamic, modelDeploymentsGVR, modelDeploymentObject(model, access.namespace, modelDeploymentSpec(model.Name, version, "", 0))); err != nil {
			return http.StatusBadGateway, err.Error()
		}
	case version:
		if canaryVersion != "" {
			return http.StatusConflict, "version " + canaryVersion + " is a canary on this version; roll the canary back first"
		}
		if err := deleteWorkload(ctx, access.dynamic, modelDeploymentsGVR, access.namespace, object.GetName()); err != nil {
			return http.StatusBadGateway, err.Error()
		}
	}
	return 0, ""
}

// canarySplitter decides, per deployment, whether a request goes to the
// canary. Without a routing key, request n (0-based) goes to the canary when
// floor((n+1)*w/100) > floor(n*w/100): exactly w of every 100 consecutive
// requests, evenly spread. With a routing key the choice is sticky:
// fnv32a(key) mod 100 < w.
type canarySplitter struct {
	mu       sync.Mutex
	counters map[string]uint64
}

func (c *canarySplitter) toCanary(deployment, routingKey string, weight int) bool {
	if weight <= 0 {
		return false
	}
	if weight >= 100 {
		return true
	}
	if routingKey != "" {
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(routingKey))
		return int(hash.Sum32()%100) < weight
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counters == nil {
		c.counters = map[string]uint64{}
	}
	n := c.counters[deployment]
	c.counters[deployment] = n + 1
	w := uint64(weight)
	return (n+1)*w/100 > n*w/100
}

// modelTarget chooses the endpoint for an invocation of model. On
// Kubernetes, invoking either version of a deployment with a canary splits
// traffic by the recorded weight between the two InferenceServices.
func (s *Server) modelTarget(ctx context.Context, r *http.Request, model api.Model) (endpoint, version string, err error) {
	if !kubernetesMode() {
		return model.EndpointURL, model.Version, nil
	}
	access, err := s.kube()
	if err != nil {
		return "", "", errKubernetesUnavailable
	}
	object, err := access.dynamic.Resource(modelDeploymentsGVR).Namespace(access.namespace).Get(ctx, api.ModelDeploymentDNSName(model.ProjectID, model.Name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	stableVersion, _, _ := unstructured.NestedString(object.Object, "spec", "version")
	canaryVersion, _, _ := unstructured.NestedString(object.Object, "spec", "canaryVersion")
	weightValue, _, _ := unstructured.NestedFieldNoCopy(object.Object, "spec", "canaryWeight")
	weight := int(asInt64(weightValue))
	stable := mapModelDeployment(api.Model{Version: stableVersion}, object)
	canary := api.Model{}
	if canaryVersion != "" {
		canary = mapModelDeployment(api.Model{Version: canaryVersion}, object)
	}
	if model.Version != stableVersion && model.Version != canaryVersion {
		return "", "", nil
	}
	if stable.DeploymentStatus != "serving" {
		// No traffic split without a serving stable version.
		return "", "", nil
	}
	if canary.DeploymentStatus == "serving" && s.canary.toCanary(object.GetName(), r.Header.Get("X-Kionga-Routing-Key"), weight) {
		return canary.EndpointURL, canaryVersion, nil
	}
	return stable.EndpointURL, stableVersion, nil
}

// proxyPrediction posts the payload to <endpoint>/invocations (the MLflow
// scoring contract; the KServe MLflow runtime serves it as well).
func proxyPrediction(w http.ResponseWriter, r *http.Request, endpoint, version string, body []byte) {
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(endpoint, "/")+"/invocations", bytes.NewReader(body)) // #nosec G704 -- endpoint comes from the serving manager or the operator's status, never the caller
	if err != nil {
		writeError(w, http.StatusBadGateway, "endpoint_unreachable", err.Error())
		return
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request) // #nosec G704 -- see above
	if err != nil {
		writeError(w, http.StatusBadGateway, "endpoint_unreachable", err.Error())
		return
	}
	defer func() { _ = response.Body.Close() }()
	w.Header().Set("Content-Type", "application/json")
	if version != "" {
		w.Header().Set("X-Kionga-Model-Version", version)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}
