package httpapi

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/policy"
	"github.com/kiongahq/platform/internal/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
)

// Start and stop for the developer workspaces (JupyterLab and the browser
// IDE) on Kubernetes installs. One KiongaWorkspace per person backs both
// tools: they share a pod and a PersistentVolumeClaim. Stopping sets
// spec.disabled, which the operator turns into zero replicas. The claim, the
// credentials Secret and the resource itself are never touched, so files
// survive a stop. Starting clears the flag and the operator brings the pod
// back on the same claim.

const workspaceLifecycleKind = "workspace_lifecycle"

// Lifecycle states shown by the console. They describe the pod, not whether
// the tool answers a health probe.
const (
	lifecycleStopped  = "stopped"
	lifecycleStopping = "stopping"
	lifecycleStarting = "starting"
	lifecycleReady    = "ready"
	lifecycleFailed   = "failed"
)

var workspaceStartEnablement = []string{
	"Developer workspaces are started and stopped by the operator on Kubernetes. Set KIONGA_WORKSPACE_NAMESPACE on the gateway to the namespace that holds the KiongaWorkspace resources.",
	"On Compose, JupyterLab and the IDE are separate services of the stack. Start them with make -C deploy local-up or make -C deploy ide-up, and stop them with docker compose stop.",
}

// workspaceLifecycleView is the console's picture of one person's workspace.
type workspaceLifecycleView struct {
	Managed bool `json:"managed"`
	// Workspace names the KiongaWorkspace (empty until one is provisioned).
	Workspace string `json:"workspace,omitempty"`
	State     string `json:"state,omitempty"`
	Ready     bool   `json:"ready"`
	// Services lists every tool that runs in the same pod. Starting or
	// stopping one starts or stops all of them.
	Services []string `json:"services,omitempty"`
	// Transitioning is true while the console should keep polling.
	Transitioning bool   `json:"transitioning"`
	Message       string `json:"message,omitempty"`
}

type workspaceLifecycleResponse struct {
	ID      string `json:"id"`
	Changed bool   `json:"changed"`
	workspaceLifecycleView
}

// workspaceLifecycleRecord is the audit document for one workspace: its last
// requested state and who asked. The audit trail lives in the platform store
// like every other mutation.
type workspaceLifecycleRecord struct {
	Workspace string    `json:"workspace"`
	Subject   string    `json:"subject"`
	Kind      string    `json:"kind"`
	Requested string    `json:"requested"`
	Actor     string    `json:"actor"`
	At        time.Time `json:"at"`
}

func lifecycleKind(kind string) bool { return kind == "workbench" || kind == "ide" }

func workspaceDisplayName(kind string) string {
	if kind == "ide" {
		return "Browser IDE"
	}
	return "JupyterLab"
}

// workspaceLifecycleState derives the state from the resource. The operator
// reports readiness against a generation; until it has observed the latest
// spec a stale readyReplicas must not read as running.
func workspaceLifecycleState(item *unstructured.Unstructured) workspaceLifecycleView {
	disabled, _, _ := unstructured.NestedBool(item.Object, "spec", "disabled")
	ready, _, _ := unstructured.NestedInt64(item.Object, "status", "readyReplicas")
	observed, _, _ := unstructured.NestedInt64(item.Object, "status", "observedGeneration")
	phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
	services, _, _ := unstructured.NestedStringSlice(item.Object, "spec", "services")
	services = slices.DeleteFunc(slices.Clone(services), func(service string) bool { return service != "workbench" && service != "ide" })
	stale := observed < item.GetGeneration()
	view := workspaceLifecycleView{Managed: true, Workspace: item.GetName(), Services: services}
	switch {
	case disabled && (ready > 0 || stale):
		view.State, view.Transitioning = lifecycleStopping, true
		view.Message = "Stopping. Your files stay on the workspace volume."
	case disabled:
		view.State = lifecycleStopped
		view.Message = "Stopped. Your files stay on the workspace volume. Start it to continue."
	case !stale && phase == "Failed":
		view.State = lifecycleFailed
		view.Message = "The workspace could not be reconciled. Ask your administrator to check its status."
	case !stale && ready > 0:
		view.State, view.Ready = lifecycleReady, true
		view.Message = "Running."
	default:
		view.State, view.Transitioning = lifecycleStarting, true
		view.Message = "Starting. This can take a minute while the volume attaches."
	}
	return view
}

// lifecycleView resolves the caller's own workspace for the availability
// list. It reports not-managed on Compose, and a nil view when Kubernetes is
// configured but the lookup cannot be made, so the list falls back to its
// probe-based answer.
func (s *Server) lifecycleView(r *http.Request, kind string) *workspaceLifecycleView {
	namespace := os.Getenv("KIONGA_WORKSPACE_NAMESPACE")
	if namespace == "" {
		return &workspaceLifecycleView{Managed: false, Message: "On Compose, JupyterLab and the IDE are separate services of the stack. Start and stop them from the stack, not the console."}
	}
	access, err := s.kube()
	if err != nil {
		return nil
	}
	list, err := access.dynamic.Resource(workspaceResource).Namespace(namespace).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		return nil
	}
	match, ambiguous := matchWorkspace(list.Items, principal(r).Subject, kind, "")
	if match == nil || ambiguous {
		return &workspaceLifecycleView{Managed: true, Message: "Your workspace is still being provisioned."}
	}
	view := workspaceLifecycleState(match)
	return &view
}

func (s *Server) startWorkspace(w http.ResponseWriter, r *http.Request) {
	s.changeWorkspaceLifecycle(w, r, "start")
}

func (s *Server) stopWorkspace(w http.ResponseWriter, r *http.Request) {
	s.changeWorkspaceLifecycle(w, r, "stop")
}

// changeWorkspaceLifecycle starts or stops the caller's own workspace, or
// another person's when the caller is an administrator or operator and names
// them with ?subject=. It is idempotent: asking for the state the workspace
// already has changes nothing, writes no audit event and answers 200.
func (s *Server) changeWorkspaceLifecycle(w http.ResponseWriter, r *http.Request, action string) {
	kind := r.PathValue("kind")
	if !lifecycleKind(kind) {
		writeError(w, http.StatusNotFound, "not_found", "Unknown workspace. Langflow workspaces are managed under /api/v1/langflow-workspaces")
		return
	}
	caller := principal(r)
	if !auth.Allowed(caller, r.Method, r.URL.Path) {
		writeError(w, http.StatusForbidden, "forbidden", "This workspace has not been assigned to you")
		return
	}
	namespace := os.Getenv("KIONGA_WORKSPACE_NAMESPACE")
	if namespace == "" {
		writeNotConfigured(w, "Developer workspaces are started and stopped on Kubernetes. This install runs them as Compose services.", workspaceStartEnablement)
		return
	}
	subject := caller.Subject
	if requested := r.URL.Query().Get("subject"); requested != "" && requested != subject {
		if !slices.Contains(caller.Roles, auth.RoleAdmin) && !slices.Contains(caller.Roles, auth.RoleOperator) {
			writeError(w, http.StatusForbidden, "access_denied", "Only an administrator can start or stop another person's workspace")
			return
		}
		subject = requested
	}
	access, err := s.kube()
	if err != nil {
		log.Printf("workspaces: Kubernetes client unavailable: %v", err)
		writeError(w, http.StatusServiceUnavailable, "kubernetes_unavailable", errKubernetesUnavailable.Error())
		return
	}
	resources := access.dynamic.Resource(workspaceResource).Namespace(namespace)
	list, err := resources.List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "workspace_unavailable", "Workspace discovery is unavailable; contact your administrator")
		return
	}
	match, ambiguous := matchWorkspace(list.Items, subject, kind, "")
	if ambiguous {
		writeError(w, http.StatusConflict, "conflict", "Multiple workspaces are assigned to this identity; contact your administrator")
		return
	}
	if match == nil {
		writeError(w, http.StatusNotFound, "not_found", "No workspace is provisioned for this account yet")
		return
	}
	project, _, _ := unstructured.NestedString(match.Object, "spec", "projectID")
	if decision := s.authorize(r, policy.WorkspaceOpen, policy.WorkspaceResource(project, kind)); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	wantDisabled := action == "stop"
	disabled, _, _ := unstructured.NestedBool(match.Object, "spec", "disabled")
	if disabled == wantDisabled {
		writeJSON(w, http.StatusOK, workspaceLifecycleResponse{ID: kind, workspaceLifecycleView: workspaceLifecycleState(match)})
		return
	}

	// Record the request before the change so no state change is unaudited,
	// then the outcome: workspace.started or workspace.stopped on success,
	// workspace.start_failed or workspace.stop_failed otherwise.
	by := actor(r)
	if err := s.recordWorkspaceLifecycle(match.GetName(), subject, kind, action, "workspace."+action+"_requested", by); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not record the audit event; the workspace was not changed")
		return
	}
	var updated *unstructured.Unstructured
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, getErr := resources.Get(r.Context(), match.GetName(), metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		if setErr := unstructured.SetNestedField(current.Object, wantDisabled, "spec", "disabled"); setErr != nil {
			return setErr
		}
		var updateErr error
		updated, updateErr = resources.Update(r.Context(), current, metav1.UpdateOptions{})
		return updateErr
	})
	if err != nil {
		if auditErr := s.recordWorkspaceLifecycle(match.GetName(), subject, kind, action, "workspace."+action+"_failed", by); auditErr != nil {
			log.Printf("workspaces: could not record failed %s of %s: %v", action, match.GetName(), auditErr)
		}
		switch {
		case apierrors.IsNotFound(err):
			writeError(w, http.StatusNotFound, "not_found", "The workspace no longer exists")
		case apierrors.IsConflict(err):
			writeError(w, http.StatusConflict, "workspace_conflict", "Workspace could not be updated; retry")
		default:
			log.Printf("workspaces: %s of %s failed: %v", action, match.GetName(), err)
			writeError(w, http.StatusBadGateway, "workspace_unavailable", fmt.Sprintf("Could not %s the workspace; retry", action))
		}
		return
	}
	done := map[string]string{"start": "workspace.started", "stop": "workspace.stopped"}[action]
	if auditErr := s.recordWorkspaceLifecycle(match.GetName(), subject, kind, action, done, by); auditErr != nil {
		log.Printf("workspaces: could not record %s of %s: %v", done, match.GetName(), auditErr)
	}
	writeJSON(w, http.StatusOK, workspaceLifecycleResponse{ID: kind, Changed: true, workspaceLifecycleView: workspaceLifecycleState(updated)})
}

func (s *Server) recordWorkspaceLifecycle(workspace, subject, kind, requested, auditAction, by string) error {
	_, err := store.UpdateDoc(s.store, workspaceLifecycleKind, workspace, func(_ workspaceLifecycleRecord, _ bool) (workspaceLifecycleRecord, error) {
		return workspaceLifecycleRecord{Workspace: workspace, Subject: subject, Kind: kind, Requested: requested, Actor: by, At: time.Now().UTC()}, nil
	}, auditAction, by)
	if err != nil && !errors.Is(err, store.ErrSkipWrite) {
		return err
	}
	return nil
}
