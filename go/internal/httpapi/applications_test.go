package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"

)

func createAppBody(projectID, extra string) string {
	body := `{"name":"billing","project_id":"` + projectID + `","image":"ghcr.io/acme/billing:1.4","port":3000,` +
		`"env":{"LOG_LEVEL":"info"},"secret_refs":["billing-db"],"resources":{"cpu":"500m","memory":"512Mi"},` +
		`"replicas":{"min":0,"max":4},"scaling":{"metric":"requests","target":50},"health_path":"/healthz","visibility":"public"`
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

func TestApplicationsReturnNotConfiguredOnCompose(t *testing.T) {
	t.Setenv("KIONGA_RUNTIME", "compose")
	server := testServer()
	for _, call := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/applications", ""},
		{http.MethodPost, "/api/v1/applications", `{"name":"billing"}`},
		{http.MethodGet, "/api/v1/applications/billing", ""},
		{http.MethodPut, "/api/v1/applications/billing", `{"image":"x"}`},
		{http.MethodDelete, "/api/v1/applications/billing", ""},
		{http.MethodPost, "/api/v1/applications/billing/redeploy", ""},
		{http.MethodGet, "/api/v1/applications/billing/logs", ""},
		{http.MethodGet, "/api/v1/applications/billing/events", ""},
	} {
		recorder := (&kubeHarness{server: server}).do(t, call.method, call.path, call.body)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("%s %s: %d %s", call.method, call.path, recorder.Code, recorder.Body.String())
		}
		body := jsonBody(t, recorder)
		steps, _ := body["steps"].([]any)
		if body["error"] != "not_configured" || len(steps) == 0 || !strings.Contains(body["message"].(string), "Compose") {
			t.Fatalf("%s %s: missing enablement steps: %v", call.method, call.path, body)
		}
	}
}

func TestApplicationsNeedABaseDomain(t *testing.T) {
	harness := newKubeHarness(t)
	t.Setenv("KIONGA_APPLICATIONS_BASE_DOMAIN", "")
	recorder := harness.do(t, http.MethodGet, "/api/v1/applications", "")
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "KIONGA_APPLICATIONS_BASE_DOMAIN") {
		t.Fatalf("unexpected: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestApplicationLifecycleOnKubernetes(t *testing.T) {
	harness := newKubeHarness(t)
	projectID := createTestProject(t, harness.server)

	created := harness.do(t, http.MethodPost, "/api/v1/applications", createAppBody(projectID, ""))
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	body := jsonBody(t, created)
	if body["status"] != "pending" || body["url"] != "https://billing.apps.example.com" {
		t.Fatalf("a new application must be pending at its host: %v", body)
	}

	object := harness.object(t, applicationsGVR, "billing")
	spec := object.Object["spec"].(map[string]any)
	minimum, _, _ := unstructured.NestedInt64(spec, "replicas", "min")
	maximum, _, _ := unstructured.NestedInt64(spec, "replicas", "max")
	target, _, _ := unstructured.NestedInt64(spec, "scaling", "target")
	port, _, _ := unstructured.NestedInt64(spec, "port")
	cpu, _, _ := unstructured.NestedString(spec, "resources", "requests", "cpu")
	if spec["image"] != "ghcr.io/acme/billing:1.4" || port != 3000 || minimum != 0 || maximum != 4 || target != 50 ||
		spec["healthPath"] != "/healthz" || spec["visibility"] != "public" || cpu != "500m" {
		t.Fatalf("unexpected KiongaApplication spec: %#v", spec)
	}
	env, _, _ := unstructured.NestedSlice(spec, "env")
	refs, _, _ := unstructured.NestedSlice(spec, "secretRefs")
	if len(env) != 1 || env[0].(map[string]any)["name"] != "LOG_LEVEL" || len(refs) != 1 || refs[0].(map[string]any)["name"] != "billing-db" {
		t.Fatalf("env or secret refs were not dispatched: %#v %#v", env, refs)
	}
	if object.GetLabels()["kionga.dev/project"] != projectID || object.GetLabels()["kionga.dev/application"] != "billing" {
		t.Fatalf("labels: %v", object.GetLabels())
	}

	if again := harness.do(t, http.MethodPost, "/api/v1/applications", createAppBody(projectID, "")); again.Code != http.StatusConflict {
		t.Fatalf("duplicate name must conflict: %d %s", again.Code, again.Body.String())
	}

	harness.setStatus(t, applicationsGVR, "billing", map[string]any{
		"phase": "Ready", "url": "https://billing.apps.example.com", "readyReplicas": int64(2),
		"conditions": []any{readyCondition("Ready", "True", "Available", "2 replicas available")},
	})
	listed := harness.do(t, http.MethodGet, "/api/v1/applications", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"status":"ready"`) || !strings.Contains(listed.Body.String(), `"ready_replicas":2`) {
		t.Fatalf("list must report the operator's status: %d %s", listed.Code, listed.Body.String())
	}
	detail := harness.do(t, http.MethodGet, "/api/v1/applications/billing", "")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"type":"Ready"`) {
		t.Fatalf("detail: %d %s", detail.Code, detail.Body.String())
	}

	updated := harness.do(t, http.MethodPut, "/api/v1/applications/billing", strings.Replace(createAppBody(projectID, ""), "billing:1.4", "billing:1.5", 1))
	if updated.Code != http.StatusAccepted {
		t.Fatalf("update: %d %s", updated.Code, updated.Body.String())
	}
	object = harness.object(t, applicationsGVR, "billing")
	if image, _, _ := unstructured.NestedString(object.Object, "spec", "image"); image != "ghcr.io/acme/billing:1.5" {
		t.Fatalf("update did not reach the resource: %s", image)
	}
	if phase, _, _ := unstructured.NestedString(object.Object, "status", "phase"); phase != "Ready" {
		t.Fatal("update must keep the operator-owned status")
	}
	if moved := harness.do(t, http.MethodPut, "/api/v1/applications/billing", `{"project_id":"prj-other","image":"x:1"}`); moved.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an application must not move between projects: %d", moved.Code)
	}

	redeployed := harness.do(t, http.MethodPost, "/api/v1/applications/billing/redeploy", "")
	if redeployed.Code != http.StatusAccepted || jsonBody(t, redeployed)["status"] != "pending" {
		t.Fatalf("redeploy: %d %s", redeployed.Code, redeployed.Body.String())
	}
	if restarted, _, _ := unstructured.NestedString(harness.object(t, applicationsGVR, "billing").Object, "spec", "restartedAt"); restarted == "" {
		t.Fatal("redeploy must set spec.restartedAt")
	}

	deleted := harness.do(t, http.MethodDelete, "/api/v1/applications/billing", "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	if _, err := harness.dynamic.Resource(applicationsGVR).Namespace(testNamespace).Get(context.Background(), "billing", metav1.GetOptions{}); err == nil {
		t.Fatal("delete must remove the KiongaApplication")
	}
	if missing := harness.do(t, http.MethodGet, "/api/v1/applications/billing", ""); missing.Code != http.StatusNotFound {
		t.Fatalf("deleted application still readable: %d", missing.Code)
	}
}

func TestApplicationGitSourceIsDispatched(t *testing.T) {
	harness := newKubeHarness(t)
	projectID := createTestProject(t, harness.server)
	created := harness.do(t, http.MethodPost, "/api/v1/applications", `{"name":"docs-site","project_id":"`+projectID+`","source":{"git":"https://github.com/acme/docs.git","context_dir":"site"}}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	spec := harness.object(t, applicationsGVR, "docs-site").Object["spec"].(map[string]any)
	source := spec["source"].(map[string]any)
	if _, hasImage := spec["image"]; hasImage || source["git"] != "https://github.com/acme/docs.git" || source["ref"] != "main" || source["contextDir"] != "site" || source["dockerfile"] != "Dockerfile" {
		t.Fatalf("unexpected source spec: %#v", spec)
	}
	minimum, _, _ := unstructured.NestedInt64(spec, "replicas", "min")
	metric, _, _ := unstructured.NestedString(spec, "scaling", "metric")
	if minimum != 1 || metric != "requests" || spec["visibility"] != "sso" {
		t.Fatalf("defaults were not applied: %#v", spec)
	}
}

func TestApplicationValidation(t *testing.T) {
	harness := newKubeHarness(t)
	projectID := createTestProject(t, harness.server)
	t.Setenv("KIONGA_ALLOWED_ORIGIN", "https://kionga.apps.example.com")
	for _, tc := range []struct {
		name, body, field string
	}{
		{"reserved name", strings.Replace(createAppBody(projectID, ""), `"name":"billing"`, `"name":"console"`, 1), "name"},
		{"workspace host family", strings.Replace(createAppBody(projectID, ""), `"name":"billing"`, `"name":"workbench-abc"`, 1), "name"},
		{"not a DNS label", strings.Replace(createAppBody(projectID, ""), `"name":"billing"`, `"name":"Billing_App"`, 1), "name"},
		{"image and source", createAppBody(projectID, `"source":{"git":"https://github.com/acme/a.git"}`), "image"},
		{"bad port", strings.Replace(createAppBody(projectID, ""), `"port":3000`, `"port":70000`, 1), "port"},
		{"cpu cannot scale to zero", strings.Replace(createAppBody(projectID, ""), `"metric":"requests","target":50`, `"metric":"cpu","target":70`, 1), "replicas.min"},
		{"reserved env prefix", strings.Replace(createAppBody(projectID, ""), `"LOG_LEVEL"`, `"KIONGA_INTERNAL_TOKEN"`, 1), "env"},
		{"unknown visibility", strings.Replace(createAppBody(projectID, ""), `"visibility":"public"`, `"visibility":"internal"`, 1), "visibility"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := harness.do(t, http.MethodPost, "/api/v1/applications", tc.body)
			if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), `"field":"`+tc.field+`"`) {
				t.Fatalf("want 422 on %s: %d %s", tc.field, recorder.Code, recorder.Body.String())
			}
		})
	}
	// "kionga" is reserved anyway; the console-host check covers installs
	// whose console host is <name>.<applications base domain>.
	t.Setenv("KIONGA_ALLOWED_ORIGIN", "https://ops.apps.example.com")
	recorder := harness.do(t, http.MethodPost, "/api/v1/applications", strings.Replace(createAppBody(projectID, ""), `"name":"billing"`, `"name":"ops"`, 1))
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "console host") {
		t.Fatalf("console host collision must be refused: %d %s", recorder.Code, recorder.Body.String())
	}
	if list, _ := harness.dynamic.Resource(applicationsGVR).Namespace(testNamespace).List(context.Background(), metav1.ListOptions{}); len(list.Items) != 0 {
		t.Fatalf("invalid requests must not create resources: %d", len(list.Items))
	}
}

func TestApplicationRBACDeniesViewerWrites(t *testing.T) {
	harness := newKubeHarness(t)
	projectID := createTestProject(t, harness.server)
	t.Setenv("KIONGA_LOCAL_ROLE", "viewer")
	if denied := harness.do(t, http.MethodPost, "/api/v1/applications", createAppBody(projectID, "")); denied.Code != http.StatusForbidden {
		t.Fatalf("viewer create: %d %s", denied.Code, denied.Body.String())
	}
	if denied := harness.do(t, http.MethodDelete, "/api/v1/applications/billing", ""); denied.Code != http.StatusForbidden {
		t.Fatalf("viewer delete: %d", denied.Code)
	}
	if read := harness.do(t, http.MethodGet, "/api/v1/applications", ""); read.Code != http.StatusOK {
		t.Fatalf("viewer read: %d %s", read.Code, read.Body.String())
	}
}

func TestApplicationCreateFailsClosedWhenKubernetesRefuses(t *testing.T) {
	harness := newKubeHarness(t)
	projectID := createTestProject(t, harness.server)
	harness.dynamic.PrependReactor("create", "kiongaapplications", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("admission webhook denied the request")
	})
	recorder := harness.do(t, http.MethodPost, "/api/v1/applications", createAppBody(projectID, ""))
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "admission webhook") {
		t.Fatalf("unexpected: %d %s", recorder.Code, recorder.Body.String())
	}
	if missing := harness.do(t, http.MethodGet, "/api/v1/applications/billing", ""); missing.Code != http.StatusNotFound {
		t.Fatalf("a refused application must not be recorded: %d %s", missing.Code, missing.Body.String())
	}
}

func TestApplicationStatusMapping(t *testing.T) {
	object := func(status map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"status": status}}
	}
	for _, tc := range []struct {
		name    string
		object  *unstructured.Unstructured
		state   string
		message string
	}{
		{"missing resource", nil, "missing", "does not exist"},
		{"not reconciled yet", object(nil), "pending", ""},
		{"progressing", object(map[string]any{"conditions": []any{readyCondition("Ready", "False", "Progressing", "0/2 replicas available")}}), "pending", "0/2 replicas"},
		{"ready", object(map[string]any{"readyReplicas": int64(2), "conditions": []any{readyCondition("Ready", "True", "Available", "")}}), "ready", ""},
		{"scaled to zero", object(map[string]any{"readyReplicas": int64(0), "conditions": []any{readyCondition("Ready", "True", "ScaledToZero", "idle")}}), "scaled-to-zero", "idle"},
		{"failed phase", object(map[string]any{"phase": "Failed", "message": "image pull failed"}), "failed", "image pull failed"},
		{"failed condition", object(map[string]any{"conditions": []any{readyCondition("Ready", "False", "Failed", "build failed: exit 1")}}), "failed", "build failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, message := mapApplicationStatus(tc.object)
			if state != tc.state || !strings.Contains(message, tc.message) {
				t.Fatalf("got %q %q", state, message)
			}
		})
	}
}

func appPod(name, app, owner string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: map[string]string{"kionga.dev/application": app},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: owner}}},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func event(name, objectKind, objectName, reason string) *corev1.Event {
	return &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		InvolvedObject: corev1.ObjectReference{Kind: objectKind, Name: objectName, Namespace: testNamespace}, Reason: reason, Type: "Normal", Count: 1}
}

func TestApplicationLogsAndEventsStayWithinTheApplication(t *testing.T) {
	harness := newKubeHarness(t,
		appPod("billing-7d9-abcde", "billing", "billing-7d9"),
		appPod("billing-admin-5f6-xyz12", "billing-admin", "billing-admin-5f6"),
		event("e1", "KiongaApplication", "billing", "Reconciled"),
		event("e2", "Pod", "billing-7d9-abcde", "Started"),
		event("e3", "ReplicaSet", "billing-7d9", "SuccessfulCreate"),
		event("e4", "Pod", "billing-admin-5f6-xyz12", "Started"),
		event("e5", "KiongaApplication", "billing-admin", "Reconciled"),
	)
	projectID := createTestProject(t, harness.server)
	if created := harness.do(t, http.MethodPost, "/api/v1/applications", createAppBody(projectID, "")); created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	logs := harness.do(t, http.MethodGet, "/api/v1/applications/billing/logs?tail=50", "")
	if logs.Code != http.StatusOK || !strings.Contains(logs.Body.String(), `"pod":"billing-7d9-abcde"`) || strings.Contains(logs.Body.String(), "billing-admin") || !strings.Contains(logs.Body.String(), "fake logs") {
		t.Fatalf("logs: %d %s", logs.Code, logs.Body.String())
	}
	if bad := harness.do(t, http.MethodGet, "/api/v1/applications/billing/logs?tail=0", ""); bad.Code != http.StatusBadRequest {
		t.Fatalf("tail=0 must be rejected: %d", bad.Code)
	}
	events := harness.do(t, http.MethodGet, "/api/v1/applications/billing/events", "")
	body := events.Body.String()
	for _, reason := range []string{"Reconciled", "Started", "SuccessfulCreate"} {
		if !strings.Contains(body, reason) {
			t.Fatalf("missing %s event: %s", reason, body)
		}
	}
	if events.Code != http.StatusOK || strings.Contains(body, "billing-admin") || jsonBody(t, events)["total"].(float64) != 3 {
		t.Fatalf("events leaked across applications: %d %s", events.Code, body)
	}
}
