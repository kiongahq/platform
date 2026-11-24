package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/kiongahq/platform/pkg/api"
)

// Functions on Kubernetes are KiongaFunction resources: the operator runs
// them as a Deployment and Service scaled by KEDA, to zero by default.
// Compose keeps faasd through the OpenFaaS API.
//
// Invocation routing. A function scaled to zero has no ready endpoints, so a
// request sent straight to its Service fails with connection refused and
// nothing scales it up. The KEDA HTTP add-on interceptor is the component
// that counts pending requests, which the external scaler turns into the
// scale-from-zero signal, and it holds each request until a replica is ready
// (KEDA_CONDITION_WAIT_TIMEOUT, 20s by default). It routes by Host header to
// the Service named in the HTTPScaledObject. So functions scaled on requests
// are invoked through the interceptor with the function's host; functions
// scaled on CPU always keep a replica and are invoked on their Service.

// functionInvoker runs one synchronous call. OpenFaaS and Kubernetes both
// satisfy it, so pipelines in functions mode run on either runtime.
type functionInvoker interface {
	Invoke(ctx context.Context, name string, payload []byte) ([]byte, error)
}

// functionRuntime returns the invoker for this install, or nil when no
// function runtime is configured.
func (s *Server) functionRuntime() functionInvoker {
	if kubernetesMode() {
		access, err := s.kube()
		if err != nil {
			return nil
		}
		return kubeFunctions{access: access, server: s}
	}
	if client := s.openfaas(); client != nil {
		return client
	}
	return nil
}

func interceptorURL() string {
	if value := strings.TrimSpace(os.Getenv("KIONGA_KEDA_INTERCEPTOR_URL")); value != "" {
		return strings.TrimRight(value, "/")
	}
	return "http://keda-add-ons-http-interceptor-proxy.keda:8080"
}

const defaultFunctionPort = 8080

// functionSpec builds the KiongaFunction spec from a deploy request.
func functionSpec(req api.DeployFunctionRequest) map[string]any {
	replicas := api.ReplicaRange{Min: 0, Max: 5}
	if req.Replicas != nil {
		replicas = *req.Replicas
	}
	scaling := api.WorkloadScaling{}
	if req.Scaling != nil {
		scaling = *req.Scaling
	}
	_ = api.NormalizeScaling(&scaling) // validated by store.ValidateFunctionRequest
	spec := map[string]any{
		"image":    req.Image,
		"port":     int64(defaultFunctionPort),
		"replicas": map[string]any{"min": int64(replicas.Min), "max": int64(replicas.Max)},
		"scaling":  map[string]any{"metric": scaling.Metric, "target": int64(scaling.Target)},
	}
	if len(req.EnvVars) > 0 {
		spec["env"] = envList(req.EnvVars)
	}
	if resources := resourceRequirements(req.CPU, req.Memory); resources != nil {
		spec["resources"] = resources
	}
	visibility := req.Visibility
	if visibility == "" {
		visibility = "internal"
	}
	spec["visibility"] = visibility
	if req.RunAsUser != nil {
		spec["runAsUser"] = *req.RunAsUser
	}
	if len(req.Annotations) > 0 {
		annotations := map[string]any{}
		for key, value := range req.Annotations {
			annotations[key] = value
		}
		spec["annotations"] = annotations
	}
	return spec
}

func functionObject(req api.DeployFunctionRequest, namespace string) *unstructured.Unstructured {
	return newWorkloadObject("KiongaFunction", req.Name, namespace, map[string]any{"kionga.dev/project": req.ProjectID, "kionga.dev/function": req.Name}, functionSpec(req))
}

// mapFunctionStatus: failed, ready, scaled-to-zero, or pending.
func mapFunctionStatus(object *unstructured.Unstructured) (string, int, string) {
	if object == nil {
		return "missing", 0, "the KiongaFunction resource does not exist"
	}
	state, message := mapApplicationStatus(object)
	return state, readWorkloadStatus(object).ReadyReplicas, message
}

type kubeFunctions struct {
	access *kubeAccess
	server *Server
}

// target resolves where to send a call for a function resource.
func (k kubeFunctions) target(object *unstructured.Unstructured) (endpoint, host string) {
	name, namespace := object.GetName(), object.GetNamespace()
	status := readWorkloadStatus(object)
	metric, _, _ := unstructured.NestedString(object.Object, "spec", "scaling", "metric")
	port, found, _ := unstructured.NestedFieldNoCopy(object.Object, "spec", "port")
	servicePort := int64(defaultFunctionPort)
	if found && asInt64(port) > 0 {
		servicePort = asInt64(port)
	}
	if metric == "cpu" {
		return fmt.Sprintf("http://%s.%s.svc:%d", name, namespace, servicePort), ""
	}
	host = status.Host
	if host == "" {
		host = name + "." + namespace + ".svc.cluster.local"
	}
	return interceptorURL(), host
}

func (k kubeFunctions) Invoke(ctx context.Context, name string, payload []byte) ([]byte, error) {
	object, err := k.access.dynamic.Resource(functionsGVR).Namespace(k.access.namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("function %s is not deployed", name)
	}
	if err != nil {
		return nil, err
	}
	if reason := readWorkloadStatus(object).failureReason("Ready"); reason != "" {
		return nil, fmt.Errorf("function %s failed: %s", name, reason)
	}
	endpoint, host := k.target(object)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/", bytes.NewReader(payload)) // #nosec G704 -- endpoint is the configured interceptor or an in-cluster Service name derived from a validated DNS label
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if host != "" {
		request.Host = host
	}
	response, err := functionHTTPClient.Do(request) // #nosec G704 -- see above
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("function %s returned %d: %s", name, response.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

// functionHTTPClient allows for a cold start: the interceptor holds the
// request while KEDA scales from zero.
var functionHTTPClient = &http.Client{Timeout: 2 * time.Minute}

func (s *Server) deployKubernetesFunction(w http.ResponseWriter, r *http.Request, req api.DeployFunctionRequest) {
	access, err := s.kube()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "kubernetes_unavailable", errKubernetesUnavailable.Error())
		return
	}
	if req.Visibility == "" {
		req.Visibility = "internal" // recorded as applied, not left empty
	}
	if len(req.Name) > api.MaxWorkloadNameLength {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("function names on Kubernetes are at most %d characters", api.MaxWorkloadNameLength))
		return
	}
	// Function names are global. Never overwrite a resource that belongs to
	// another project, whether a record says so or the resource's label does.
	for _, existing := range s.store.Functions() {
		if existing.Name == req.Name && existing.ProjectID != req.ProjectID {
			writeError(w, http.StatusConflict, "conflict", "a function named "+req.Name+" belongs to another project")
			return
		}
	}
	if existing, getErr := access.dynamic.Resource(functionsGVR).Namespace(access.namespace).Get(r.Context(), req.Name, metav1.GetOptions{}); getErr == nil && existing.GetLabels()["kionga.dev/project"] != req.ProjectID {
		writeError(w, http.StatusConflict, "conflict", "a function named "+req.Name+" belongs to another project")
		return
	}
	object, err := applyWorkload(r.Context(), access.dynamic, functionsGVR, functionObject(req, access.namespace))
	if err != nil {
		writeError(w, http.StatusBadGateway, "deploy_failed", "Kubernetes rejected the function: "+err.Error())
		return
	}
	item, err := s.store.UpsertFunction(req, principal(r).Subject, actor(r))
	if err != nil {
		writeMutation(w, item, err, http.StatusAccepted)
		return
	}
	item.Status, item.Replicas, item.Message = mapFunctionStatus(object)
	if item.Status == "ready" || item.Status == "scaled-to-zero" {
		// A fresh apply has not been reconciled yet; never report a stale ready.
		item.Status = "pending"
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (s *Server) listKubernetesFunctions(w http.ResponseWriter, r *http.Request, persisted []api.Function) {
	access, err := s.kube()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "kubernetes_unavailable", errKubernetesUnavailable.Error())
		return
	}
	list, err := access.dynamic.Resource(functionsGVR).Namespace(access.namespace).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeError(w, http.StatusBadGateway, "kubernetes_unavailable", "reading function status failed: "+err.Error())
		return
	}
	live := map[string]*unstructured.Unstructured{}
	for i := range list.Items {
		live[list.Items[i].GetName()] = &list.Items[i]
	}
	for i := range persisted {
		persisted[i].Status, persisted[i].Replicas, persisted[i].Message = mapFunctionStatus(live[persisted[i].Name])
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "runtime": "kubernetes", "items": persisted, "total": len(persisted)})
}

func (s *Server) deleteKubernetesFunction(w http.ResponseWriter, r *http.Request, name string) bool {
	access, err := s.kube()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "kubernetes_unavailable", errKubernetesUnavailable.Error())
		return false
	}
	if err := deleteWorkload(r.Context(), access.dynamic, functionsGVR, access.namespace, name); err != nil {
		writeError(w, http.StatusBadGateway, "delete_failed", err.Error())
		return false
	}
	return true
}

// invokeKubernetesFunctionAsync accepts the call and runs it in the
// background. There is no queue on Kubernetes: a call accepted here is lost
// if this gateway replica stops before it completes. The outcome is logged
// with the call id.
func (s *Server) invokeKubernetesFunctionAsync(w http.ResponseWriter, invoker functionInvoker, name string, payload []byte) {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	callID := "call-" + hex.EncodeToString(id)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		started := time.Now()
		raw, err := invoker.Invoke(ctx, name, payload)
		if err != nil {
			log.Printf("async function call %s to %s failed after %s: %v", callID, url.PathEscape(name), time.Since(started).Round(time.Millisecond), err)
			return
		}
		log.Printf("async function call %s to %s completed in %s (%d bytes)", callID, url.PathEscape(name), time.Since(started).Round(time.Millisecond), len(raw))
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "call_id": callID})
}
