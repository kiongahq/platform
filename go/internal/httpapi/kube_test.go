package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// TestMain pins the runtime to Compose unless a test opts in, so the suite
// behaves the same when CI itself runs inside a Kubernetes pod.
func TestMain(m *testing.M) {
	if os.Getenv("KIONGA_RUNTIME") == "" {
		_ = os.Setenv("KIONGA_RUNTIME", "compose")
	}
	os.Exit(m.Run())
}

const testNamespace = "kionga-apps"

type kubeHarness struct {
	server  http.Handler
	dynamic *dynamicfake.FakeDynamicClient
	core    *kubefake.Clientset
}

// newKubeHarness runs the gateway in Kubernetes mode against fake clients.
func newKubeHarness(t *testing.T, coreObjects ...runtime.Object) *kubeHarness {
	t.Helper()
	t.Setenv("KIONGA_RUNTIME", "kubernetes")
	t.Setenv("KIONGA_TARGET_NAMESPACE", testNamespace)
	t.Setenv("KIONGA_APPLICATIONS_BASE_DOMAIN", "apps.example.com")
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		applicationsGVR:     "KiongaApplicationList",
		functionsGVR:        "KiongaFunctionList",
		modelDeploymentsGVR: "KiongaModelDeploymentList",
	})
	core := kubefake.NewClientset(coreObjects...)
	previous := kubeAccessFactory
	kubeAccessFactory = func() (*kubeAccess, error) {
		return &kubeAccess{dynamic: dynamicClient, core: core, namespace: testNamespace}, nil
	}
	t.Cleanup(func() { kubeAccessFactory = previous })
	return &kubeHarness{server: testServer(), dynamic: dynamicClient, core: core}
}

func (h *kubeHarness) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	recorder := httptest.NewRecorder()
	h.server.ServeHTTP(recorder, newTestRequest(method, path, reader))
	return recorder
}

func (h *kubeHarness) object(t *testing.T, gvr schema.GroupVersionResource, name string) *unstructured.Unstructured {
	t.Helper()
	object, err := h.dynamic.Resource(gvr).Namespace(testNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get %s %s: %v", gvr.Resource, name, err)
	}
	return object
}

// setStatus plays the operator: it writes a status block on a resource.
func (h *kubeHarness) setStatus(t *testing.T, gvr schema.GroupVersionResource, name string, status map[string]any) {
	t.Helper()
	object := h.object(t, gvr, name)
	object.Object["status"] = status
	if _, err := h.dynamic.Resource(gvr).Namespace(testNamespace).Update(context.Background(), object, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func readyCondition(kind, status, reason, message string) map[string]any {
	return map[string]any{"type": kind, "status": status, "reason": reason, "message": message}
}

func jsonBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	return decodeBody[map[string]any](t, recorder)
}
