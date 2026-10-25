package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/policy"
	"github.com/kiongahq/platform/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

var langflowWorkspaceName = regexp.MustCompile(`^langflow-[a-f0-9]{24}$`)

type langflowWorkspaceEntry struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	ProjectID string    `json:"project_id"`
	Subject   string    `json:"subject"`
	State     string    `json:"state"`
	Ready     bool      `json:"ready"`
	CPU       int64     `json:"cpu"`
	MemoryGB  int64     `json:"memory_gb"`
	StorageGB int64     `json:"storage_gb"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}
type createLangflowRequest struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	CPU       int64  `json:"cpu"`
	MemoryGB  int64  `json:"memory_gb"`
	StorageGB int64  `json:"storage_gb"`
}

func langflowClient() (dynamic.Interface, string, error) {
	namespace := os.Getenv("KIONGA_WORKSPACE_NAMESPACE")
	if namespace == "" {
		return nil, "", fmt.Errorf("Kubernetes workspace provisioning is not configured")
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, "", err
	}
	config.Timeout = 5 * time.Second
	client, err := dynamic.NewForConfig(config)
	return client, namespace, err
}
func langflowResource(client dynamic.Interface, namespace string) dynamic.ResourceInterface {
	return client.Resource(schema.GroupVersionResource{Group: "mlaiops.io", Version: "v1alpha1", Resource: "kiongaworkspaces"}).Namespace(namespace)
}
func langflowEntry(item *unstructured.Unstructured) langflowWorkspaceEntry {
	subject, _, _ := unstructured.NestedString(item.Object, "spec", "subject")
	project, _, _ := unstructured.NestedString(item.Object, "spec", "projectID")
	display := item.GetAnnotations()["mlaiops.io/display-name"]
	if display == "" {
		display = item.GetName()
	}
	disabled, _, _ := unstructured.NestedBool(item.Object, "spec", "disabled")
	ready, _, _ := unstructured.NestedInt64(item.Object, "status", "readyReplicas")
	cpu, _, _ := unstructured.NestedInt64(item.Object, "spec", "compute", "vcpus")
	memory, _, _ := unstructured.NestedInt64(item.Object, "spec", "compute", "memoryGB")
	storageGB, _, _ := unstructured.NestedInt64(item.Object, "spec", "storageGB")
	state := "starting"
	if ready > 0 {
		state = "ready"
	}
	if disabled {
		state = "stopped"
	}
	return langflowWorkspaceEntry{ID: item.GetName(), Name: display, ProjectID: project, Subject: subject, State: state, Ready: !disabled && ready > 0, CPU: cpu, MemoryGB: memory, StorageGB: storageGB, CreatedAt: item.GetCreationTimestamp().Time}
}
func ownedLangflow(item *unstructured.Unstructured, subject string) bool {
	services, _, _ := unstructured.NestedStringSlice(item.Object, "spec", "services")
	owner, _, _ := unstructured.NestedString(item.Object, "spec", "subject")
	return owner == subject && slices.Contains(services, "langflow")
}
func (s *Server) langflowWorkspaces(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("KIONGA_WORKSPACE_NAMESPACE") == "" {
		items := []langflowWorkspaceEntry{}
		if os.Getenv("KIONGA_LANGFLOW_UPSTREAM") != "" && localBootstrapAdmin(principal(r)) {
			items = append(items, langflowWorkspaceEntry{ID: "local", Name: "Local Langflow", State: "configured", Subject: principal(r).Subject})
		}
		writeJSON(w, 200, map[string]any{"items": items, "mode": "local"})
		return
	}
	client, namespace, err := langflowClient()
	if err != nil {
		writeError(w, 503, "workspace_unavailable", "Kubernetes workspace discovery is unavailable")
		return
	}
	list, err := langflowResource(client, namespace).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeError(w, 503, "workspace_unavailable", "Could not list Langflow workspaces")
		return
	}
	items := []langflowWorkspaceEntry{}
	for i := range list.Items {
		item := &list.Items[i]
		if ownedLangflow(item, principal(r).Subject) && projectAllowed(s.store, principal(r), langflowEntry(item).ProjectID) {
			items = append(items, langflowEntry(item))
		}
	}
	writeJSON(w, 200, map[string]any{"items": items, "mode": "kubernetes"})
}
func (s *Server) createLangflowWorkspace(w http.ResponseWriter, r *http.Request) {
	var req createLangflowRequest
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := decode(r, &req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 80 {
		writeError(w, 422, "validation_error", "Workspace name must contain 1–80 characters")
		return
	}
	if req.ProjectID == "" || !projectAllowed(s.store, principal(r), req.ProjectID) {
		writeError(w, 403, "access_denied", "Project is not assigned to this user")
		return
	}
	if _, err := s.store.Project(req.ProjectID); err != nil {
		writeError(w, 404, "not_found", "Project not found")
		return
	}
	if req.CPU == 0 {
		req.CPU = 1
	}
	if req.MemoryGB == 0 {
		req.MemoryGB = 2
	}
	if req.StorageGB == 0 {
		req.StorageGB = 10
	}
	if req.CPU < 1 || req.CPU > 32 || req.MemoryGB < 1 || req.MemoryGB > 128 || req.StorageGB < 1 || req.StorageGB > 1000 {
		writeError(w, 422, "validation_error", "Workspace resources are outside the supported range")
		return
	}
	client, namespace, err := langflowClient()
	if err != nil {
		writeError(w, 503, "workspace_unavailable", "Kubernetes workspace provisioning is not configured")
		return
	}
	list, err := langflowResource(client, namespace).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeError(w, 503, "workspace_unavailable", "Could not check workspace quota")
		return
	}
	count := 0
	for i := range list.Items {
		owner, _, _ := unstructured.NestedString(list.Items[i].Object, "spec", "subject")
		if owner == principal(r).Subject {
			count++
		}
	}
	if !slices.Contains(principal(r).Roles, auth.RoleAdmin) && !slices.Contains(principal(r).Roles, auth.RoleOperator) {
		access, err := s.store.AccessFor(principal(r).Subject)
		if err != nil || access.Compute.MaxVMs < 1 || count >= access.Compute.MaxVMs || req.CPU > int64(access.Compute.VCPUs) || req.MemoryGB > int64(access.Compute.MemoryGB) || req.StorageGB > int64(access.Storage.SizeGB) {
			writeError(w, 403, "resource_not_provisioned", "Workspace exceeds your assigned capacity")
			return
		}
	}
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		writeError(w, 500, "internal_error", "Could not allocate workspace ID")
		return
	}
	name := "langflow-" + hex.EncodeToString(raw)
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "mlaiops.io/v1alpha1", "kind": "KiongaWorkspace", "metadata": map[string]any{"name": name, "namespace": namespace, "annotations": map[string]any{"mlaiops.io/display-name": req.Name}, "labels": map[string]any{"mlaiops.io/component": "langflow"}}, "spec": map[string]any{"subject": principal(r).Subject, "projectID": req.ProjectID, "services": []any{"langflow"}, "disabled": false, "compute": map[string]any{"vcpus": req.CPU, "memoryGB": req.MemoryGB, "gpus": int64(0)}, "storageGB": req.StorageGB}}}
	created, err := langflowResource(client, namespace).Create(r.Context(), object, metav1.CreateOptions{})
	if err != nil {
		writeError(w, 502, "workspace_unavailable", "Could not create Langflow workspace")
		return
	}
	writeJSON(w, 201, langflowEntry(created))
}
func (s *Server) ownedLangflowWorkspace(r *http.Request) (dynamic.ResourceInterface, *unstructured.Unstructured, error) {
	id := r.PathValue("id")
	if id == "local" {
		return nil, nil, fmt.Errorf("Local Langflow is managed by Compose")
	}
	if !langflowWorkspaceName.MatchString(id) {
		return nil, nil, store.ErrNotFound
	}
	client, namespace, err := langflowClient()
	if err != nil {
		return nil, nil, err
	}
	resources := langflowResource(client, namespace)
	item, err := resources.Get(r.Context(), id, metav1.GetOptions{})
	if err != nil || !ownedLangflow(item, principal(r).Subject) {
		return nil, nil, store.ErrNotFound
	}
	project, _, _ := unstructured.NestedString(item.Object, "spec", "projectID")
	if project == "" || !projectAllowed(s.store, principal(r), project) {
		return nil, nil, store.ErrNotFound
	}
	return resources, item, nil
}
func (s *Server) launchLangflowWorkspace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "local" && os.Getenv("KIONGA_WORKSPACE_NAMESPACE") == "" {
		target, err := workspaceTarget("langflow", r)
		if err != nil {
			writeError(w, 503, "workspace_unavailable", err.Error())
			return
		}
		s.writeWorkspaceLaunch(w, r, target, "langflow", "/workspaces/langflow/")
		return
	}
	_, item, err := s.ownedLangflowWorkspace(r)
	if err != nil {
		writeError(w, 404, "not_found", "Langflow workspace not found")
		return
	}
	disabled, _, _ := unstructured.NestedBool(item.Object, "spec", "disabled")
	if disabled {
		writeError(w, 409, "workspace_stopped", "Start the workspace before opening it")
		return
	}
	project, _, _ := unstructured.NestedString(item.Object, "spec", "projectID")
	if decision := s.authorize(r, policy.WorkspaceOpen, policy.WorkspaceResource(project, "langflow")); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	client, namespace, err := langflowClient()
	if err != nil {
		writeError(w, 503, "workspace_unavailable", "Workspace discovery is unavailable")
		return
	}
	target, err := discoverWorkspaceNamed(r.Context(), client, namespace, principal(r).Subject, "langflow", item.GetName())
	if err != nil {
		writeError(w, 503, "workspace_unavailable", err.Error())
		return
	}
	s.writeWorkspaceLaunch(w, r, target, "langflow", "/workspaces/langflow/")
}
func (s *Server) changeLangflowWorkspace(w http.ResponseWriter, r *http.Request, action string) {
	resources, item, err := s.ownedLangflowWorkspace(r)
	if err != nil {
		writeError(w, 404, "not_found", "Langflow workspace not found")
		return
	}
	if action == "stop" || action == "start" {
		_ = unstructured.SetNestedField(item.Object, action == "stop", "spec", "disabled")
	}
	if action == "restart" {
		annotations := item.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["mlaiops.io/restart-at"] = time.Now().UTC().Format(time.RFC3339Nano)
		item.SetAnnotations(annotations)
	}
	updated, err := resources.Update(r.Context(), item, metav1.UpdateOptions{})
	if err != nil {
		writeError(w, 409, "workspace_conflict", "Workspace could not be updated; retry")
		return
	}
	writeJSON(w, 200, langflowEntry(updated))
}
func (s *Server) startLangflowWorkspace(w http.ResponseWriter, r *http.Request) {
	s.changeLangflowWorkspace(w, r, "start")
}
func (s *Server) stopLangflowWorkspace(w http.ResponseWriter, r *http.Request) {
	s.changeLangflowWorkspace(w, r, "stop")
}
func (s *Server) restartLangflowWorkspace(w http.ResponseWriter, r *http.Request) {
	s.changeLangflowWorkspace(w, r, "restart")
}
func (s *Server) deleteLangflowWorkspace(w http.ResponseWriter, r *http.Request) {
	resources, item, err := s.ownedLangflowWorkspace(r)
	if err != nil {
		writeError(w, 404, "not_found", "Langflow workspace not found")
		return
	}
	if err := resources.Delete(r.Context(), item.GetName(), metav1.DeleteOptions{}); err != nil {
		writeError(w, 502, "workspace_unavailable", "Could not delete workspace")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
