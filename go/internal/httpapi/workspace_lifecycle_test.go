package httpapi

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

type lifecycleHarness struct {
	server  http.Handler
	store   *store.Store
	dynamic *dynamicfake.FakeDynamicClient
}

func developerWorkspace(name, subject string, disabled bool, services ...string) *unstructured.Unstructured {
	list := make([]any, len(services))
	for i, service := range services {
		list[i] = service
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kionga.dev/v1alpha1", "kind": "KiongaWorkspace",
		"metadata": map[string]any{"name": name, "namespace": "ws"},
		"spec": map[string]any{"subject": subject, "services": list, "disabled": disabled, "storageGB": int64(20),
			"compute": map[string]any{"vcpus": int64(2), "memoryGB": int64(4)}},
		"status": map[string]any{"phase": "Ready", "readyReplicas": int64(1)},
	}}
}

func newLifecycleHarness(t *testing.T, objects ...runtime.Object) *lifecycleHarness {
	t.Helper()
	t.Setenv("KIONGA_WORKSPACE_NAMESPACE", "ws")
	t.Setenv("KIONGA_ENVIRONMENT", "")
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{workspaceResource: "KiongaWorkspaceList"}, objects...)
	previous := kubeAccessFactory
	kubeAccessFactory = func() (*kubeAccess, error) { return &kubeAccess{dynamic: client, namespace: "ws"}, nil }
	t.Cleanup(func() { kubeAccessFactory = previous })
	repository := store.New()
	static, _ := fs.Sub(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, ".")
	return &lifecycleHarness{server: New(repository, static), store: repository, dynamic: client}
}

func (h *lifecycleHarness) as(t *testing.T, p auth.Principal, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := newTestRequest(method, path, nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), p))
	recorder := httptest.NewRecorder()
	h.server.ServeHTTP(recorder, request)
	return recorder
}

func (h *lifecycleHarness) workspace(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	item, err := h.dynamic.Resource(workspaceResource).Namespace("ws").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func (h *lifecycleHarness) auditActions(resourceID string) []string {
	out := []string{}
	for _, event := range h.store.Audit() {
		if event.Resource == workspaceLifecycleKind && event.ResourceID == resourceID {
			out = append(out, event.Action)
		}
	}
	return out
}

var (
	devAlice = auth.Principal{Subject: "alice", Email: "alice@example.com", Roles: []string{auth.RoleEngineer}}
	lcAdmin  = auth.Principal{Subject: "root", Email: "root@example.com", Roles: []string{auth.RoleAdmin}}
	lcViewer = auth.Principal{Subject: "alice", Roles: []string{auth.RoleViewer}}
	lcUser   = auth.Principal{Subject: "alice", Roles: []string{auth.RoleUser}, Provisioned: true, Services: []string{"workbench", "ide"}}
)

func TestWorkspaceStartStopReturnsNotConfiguredOnCompose(t *testing.T) {
	t.Setenv("KIONGA_WORKSPACE_NAMESPACE", "")
	repository := store.New()
	static, _ := fs.Sub(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, ".")
	server := New(repository, static)
	for _, action := range []string{"start", "stop"} {
		request := newTestRequest(http.MethodPost, "/api/v1/workspaces/workbench/"+action, nil)
		request = request.WithContext(auth.WithPrincipal(request.Context(), lcAdmin))
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		body := jsonBody(t, recorder)
		steps, _ := body["steps"].([]any)
		if recorder.Code != http.StatusConflict || body["error"] != "not_configured" || len(steps) == 0 || !strings.Contains(body["message"].(string), "Compose") {
			t.Fatalf("%s: %d %v", action, recorder.Code, body)
		}
	}
}

func TestStopSuspendsWithoutTouchingTheVolumeAndIsAudited(t *testing.T) {
	h := newLifecycleHarness(t, developerWorkspace("alice-ws", "alice", false, "workbench", "ide"))
	recorder := h.as(t, devAlice, http.MethodPost, "/api/v1/workspaces/workbench/stop")
	body := jsonBody(t, recorder)
	if recorder.Code != 200 || body["changed"] != true || body["state"] != "stopping" || body["workspace"] != "alice-ws" {
		t.Fatalf("stop: %d %v", recorder.Code, body)
	}
	item := h.workspace(t, "alice-ws")
	disabled, _, _ := unstructured.NestedBool(item.Object, "spec", "disabled")
	storage, _, _ := unstructured.NestedInt64(item.Object, "spec", "storageGB")
	services, _, _ := unstructured.NestedStringSlice(item.Object, "spec", "services")
	if !disabled || storage != 20 || len(services) != 2 {
		t.Fatalf("stop must only set spec.disabled: %v", item.Object["spec"])
	}
	for _, action := range h.dynamic.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatalf("stop must never delete anything: %#v", action)
		}
	}
	if actions := h.auditActions("alice-ws"); len(actions) != 1 || actions[0] != "workspace.stopped" {
		t.Fatalf("audit: %v", actions)
	}
	if event := h.store.Audit()[0]; event.Actor != "alice@example.com" {
		t.Fatalf("audit actor: %#v", event)
	}
}

func TestStartAndStopAreIdempotent(t *testing.T) {
	parked := developerWorkspace("alice-ws", "alice", true, "workbench", "ide")
	_ = unstructured.SetNestedField(parked.Object, int64(0), "status", "readyReplicas")
	h := newLifecycleHarness(t, parked)
	if again := jsonBody(t, h.as(t, devAlice, http.MethodPost, "/api/v1/workspaces/ide/stop")); again["changed"] != false {
		t.Fatalf("stopping a stopped workspace must change nothing: %v", again)
	}
	if actions := h.auditActions("alice-ws"); len(actions) != 0 {
		t.Fatalf("a no-op must not be audited: %v", actions)
	}
	first := jsonBody(t, h.as(t, devAlice, http.MethodPost, "/api/v1/workspaces/ide/start"))
	second := jsonBody(t, h.as(t, devAlice, http.MethodPost, "/api/v1/workspaces/ide/start"))
	if first["changed"] != true || first["state"] != "starting" || second["changed"] != false {
		t.Fatalf("start: %v then %v", first, second)
	}
	if actions := h.auditActions("alice-ws"); len(actions) != 1 || actions[0] != "workspace.started" {
		t.Fatalf("audit: %v", actions)
	}
}

func TestStartedWorkspaceIsReadyOnlyAfterTheOperatorObservesIt(t *testing.T) {
	// A stale readyReplicas from before the stop must not read as running.
	stale := developerWorkspace("alice-ws", "alice", false, "workbench")
	stale.SetGeneration(3)
	_ = unstructured.SetNestedField(stale.Object, int64(2), "status", "observedGeneration")
	if view := workspaceLifecycleState(stale); view.State != lifecycleStarting || view.Ready || !view.Transitioning {
		t.Fatalf("stale status: %#v", view)
	}
	_ = unstructured.SetNestedField(stale.Object, int64(3), "status", "observedGeneration")
	if view := workspaceLifecycleState(stale); view.State != lifecycleReady || !view.Ready || view.Transitioning {
		t.Fatalf("observed status: %#v", view)
	}
	stopping := developerWorkspace("alice-ws", "alice", true, "workbench")
	if view := workspaceLifecycleState(stopping); view.State != lifecycleStopping {
		t.Fatalf("pod still up: %#v", view)
	}
	_ = unstructured.SetNestedField(stopping.Object, int64(0), "status", "readyReplicas")
	if view := workspaceLifecycleState(stopping); view.State != lifecycleStopped || view.Transitioning {
		t.Fatalf("pod gone: %#v", view)
	}
}

func TestWorkspaceLifecycleOwnership(t *testing.T) {
	h := newLifecycleHarness(t,
		developerWorkspace("alice-ws", "alice", false, "workbench", "ide"),
		developerWorkspace("bob-ws", "bob", false, "workbench", "ide"))
	bob := auth.Principal{Subject: "bob", Roles: []string{auth.RoleEngineer}}
	if r := h.as(t, bob, http.MethodPost, "/api/v1/workspaces/ide/stop?subject=alice"); r.Code != http.StatusForbidden {
		t.Fatalf("a non-administrator must not act on another person: %d %s", r.Code, r.Body.String())
	}
	if r := h.as(t, bob, http.MethodPost, "/api/v1/workspaces/ide/stop"); r.Code != 200 {
		t.Fatalf("own stop: %d", r.Code)
	}
	if disabled, _, _ := unstructured.NestedBool(h.workspace(t, "alice-ws").Object, "spec", "disabled"); disabled {
		t.Fatal("alice's workspace was touched by bob")
	}
	if r := h.as(t, lcAdmin, http.MethodPost, "/api/v1/workspaces/ide/stop?subject=alice"); r.Code != 200 {
		t.Fatalf("admin stop for another person: %d %s", r.Code, r.Body.String())
	}
	if disabled, _, _ := unstructured.NestedBool(h.workspace(t, "alice-ws").Object, "spec", "disabled"); !disabled {
		t.Fatal("admin stop did not apply")
	}
	if event := h.store.Audit()[0]; event.Actor != "root@example.com" || event.ResourceID != "alice-ws" {
		t.Fatalf("admin action must be attributed to the administrator: %#v", event)
	}
	if r := h.as(t, lcAdmin, http.MethodPost, "/api/v1/workspaces/ide/stop?subject=nobody"); r.Code != http.StatusNotFound {
		t.Fatalf("unknown subject: %d", r.Code)
	}
}

func TestWorkspaceLifecycleRoles(t *testing.T) {
	h := newLifecycleHarness(t, developerWorkspace("alice-ws", "alice", false, "workbench", "ide"))
	if r := h.as(t, lcViewer, http.MethodPost, "/api/v1/workspaces/workbench/stop"); r.Code != http.StatusForbidden {
		t.Fatalf("viewer: %d %s", r.Code, r.Body.String())
	}
	if r := h.as(t, auth.Principal{Subject: "alice", Roles: []string{auth.RoleService}}, http.MethodPost, "/api/v1/workspaces/workbench/stop"); r.Code != http.StatusForbidden {
		t.Fatalf("service: %d", r.Code)
	}
	withoutIDE := lcUser
	withoutIDE.Services = []string{"workbench"}
	if r := h.as(t, withoutIDE, http.MethodPost, "/api/v1/workspaces/ide/stop"); r.Code != http.StatusForbidden {
		t.Fatalf("user without the ide service: %d", r.Code)
	}
	if r := h.as(t, lcUser, http.MethodPost, "/api/v1/workspaces/ide/stop"); r.Code != 200 {
		t.Fatalf("user with the service: %d %s", r.Code, r.Body.String())
	}
	if r := h.as(t, lcAdmin, http.MethodPost, "/api/v1/workspaces/langflow/stop"); r.Code != http.StatusNotFound {
		t.Fatalf("langflow uses its own endpoints: %d", r.Code)
	}
	if r := h.as(t, auth.Principal{Subject: "carol", Roles: []string{auth.RoleEngineer}}, http.MethodPost, "/api/v1/workspaces/ide/start"); r.Code != http.StatusNotFound {
		t.Fatalf("no workspace provisioned: %d", r.Code)
	}
}

func TestFailedChangeLeavesTheWorkspaceAloneAndIsAudited(t *testing.T) {
	h := newLifecycleHarness(t, developerWorkspace("alice-ws", "alice", false, "workbench"))
	h.dynamic.PrependReactor("update", "kiongaworkspaces", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("admission webhook denied the request")
	})
	r := h.as(t, devAlice, http.MethodPost, "/api/v1/workspaces/workbench/stop")
	if r.Code != http.StatusBadGateway || strings.Contains(r.Body.String(), "webhook") {
		t.Fatalf("failure: %d %s", r.Code, r.Body.String())
	}
	if disabled, _, _ := unstructured.NestedBool(h.workspace(t, "alice-ws").Object, "spec", "disabled"); disabled {
		t.Fatal("a refused update must not change the workspace")
	}
	if actions := h.auditActions("alice-ws"); len(actions) != 2 || actions[0] != "workspace.stop_failed" {
		t.Fatalf("audit: %v", actions)
	}
}

func TestWorkspaceListShowsStoppedAndStartingInsteadOfUnconfigured(t *testing.T) {
	stopped := developerWorkspace("alice-ws", "alice", true, "workbench", "ide")
	_ = unstructured.SetNestedField(stopped.Object, int64(0), "status", "readyReplicas")
	h := newLifecycleHarness(t, stopped)
	body := decodeBody[struct {
		Items []struct {
			ID        string                  `json:"id"`
			State     string                  `json:"state"`
			Available bool                    `json:"available"`
			Message   string                  `json:"message"`
			Lifecycle *workspaceLifecycleView `json:"lifecycle"`
		} `json:"items"`
	}](t, h.as(t, devAlice, http.MethodGet, "/api/v1/workspaces"))
	for _, item := range body.Items {
		switch item.ID {
		case "workbench", "ide":
			if item.State != "stopped" || item.Available || item.Lifecycle == nil || !item.Lifecycle.Managed || item.Lifecycle.State != "stopped" || !strings.Contains(item.Message, "Start it") {
				t.Fatalf("%s: %#v", item.ID, item)
			}
		}
	}
	if _, err := h.dynamic.Resource(workspaceResource).Namespace("ws").Get(context.Background(), "alice-ws", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
	// Starting reports "starting" until readyReplicas arrives.
	h.as(t, devAlice, http.MethodPost, "/api/v1/workspaces/workbench/start")
	body = decodeBody[struct {
		Items []struct {
			ID        string                  `json:"id"`
			State     string                  `json:"state"`
			Available bool                    `json:"available"`
			Message   string                  `json:"message"`
			Lifecycle *workspaceLifecycleView `json:"lifecycle"`
		} `json:"items"`
	}](t, h.as(t, devAlice, http.MethodGet, "/api/v1/workspaces"))
	if body.Items[0].State != "starting" || !body.Items[0].Lifecycle.Transitioning {
		t.Fatalf("starting: %#v", body.Items[0])
	}
}

func TestWorkspaceListOnComposeIsNotManaged(t *testing.T) {
	t.Setenv("KIONGA_WORKSPACE_NAMESPACE", "")
	repository := store.New()
	static, _ := fs.Sub(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, ".")
	server := New(repository, static)
	request := newTestRequest(http.MethodGet, "/api/v1/workspaces", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), lcAdmin))
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	body := decodeBody[struct {
		Items []struct {
			ID        string                  `json:"id"`
			Lifecycle *workspaceLifecycleView `json:"lifecycle"`
		} `json:"items"`
	}](t, recorder)
	for _, item := range body.Items {
		managed := item.Lifecycle != nil && item.Lifecycle.Managed
		if (item.ID == "langflow" && item.Lifecycle != nil) || (item.ID != "langflow" && (item.Lifecycle == nil || managed || !strings.Contains(item.Lifecycle.Message, "Compose"))) {
			t.Fatalf("%s: %#v", item.ID, item.Lifecycle)
		}
	}
}

var _ = api.AuditEvent{}
