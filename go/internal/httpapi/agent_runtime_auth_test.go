package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/pkg/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func runtimeSecretFixture(id, token string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": api.AgentRuntimeSecretName(id), "namespace": "default"},
		"data":     map[string]any{"KIONGA_INTERNAL_TOKEN": base64.StdEncoding.EncodeToString([]byte(token))},
	}}
}

func TestAgentRuntimeTokenIsolation(t *testing.T) {
	tokens := map[string]string{"a": runtimeTestToken(), "b": runtimeTestToken()}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), runtimeSecretFixture("a", tokens["a"]), runtimeSecretFixture("b", tokens["b"]))
	serviceToken := runtimeTestToken()
	t.Setenv("KIONGA_INTERNAL_TOKEN", serviceToken)
	// Exercise the gateway authentication boundary, including service-only routes.
	gateway := auth.New(auth.Config{}).Middleware(testServer())
	for _, path := range []string{"/api/v1/realtime/demo", "/api/v1/pipelines/runs/run/steps", "/api/v1/features/demo/materialized"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		request.Header.Set("Authorization", "Bearer "+tokens["a"])
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s: got %d, want 401", path, response.Code)
		}
	}
	control := httptest.NewRequest(http.MethodPost, "/api/v1/realtime/demo", strings.NewReader("{}"))
	control.Header.Set("Authorization", "Bearer "+serviceToken)
	control.Header.Set("Content-Type", "application/json")
	controlResponse := httptest.NewRecorder()
	gateway.ServeHTTP(controlResponse, control)
	if controlResponse.Code != http.StatusOK {
		t.Fatalf("service credential control: got %d", controlResponse.Code)
	}
	// Model the unchanged agent-runtime bearer contract. Agent B accepts only
	// the token supplied in its own KIONGA_INTERNAL_TOKEN environment.
	runtimeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+tokens["b"])) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer runtimeB.Close()
	for _, id := range []string{"a", "b"} {
		token, err := discoverAgentRuntimeToken(t.Context(), client, "default", id)
		if err != nil {
			t.Fatal(err)
		}
		request, _ := http.NewRequest(http.MethodPost, runtimeB.URL+"/invoke", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := runtimeB.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		want := http.StatusOK
		if id == "a" {
			want = http.StatusUnauthorized
		}
		if response.StatusCode != want {
			t.Fatalf("agent %s token: got %d, want %d", id, response.StatusCode, want)
		}
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "get" || action.GetResource() != secretResource || action.GetNamespace() != "default" {
			t.Fatalf("unexpected Kubernetes access: %v", action)
		}
	}
}

func TestDiscoverAgentRuntimeTokenFailsClosedAndReadsRotation(t *testing.T) {
	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	for _, value := range []string{"", "short", runtimeTestToken(), runtimeTestToken()} {
		secret := runtimeSecretFixture("a", value)
		if _, err := client.Resource(secretResource).Namespace("default").Get(t.Context(), secret.GetName(), metav1.GetOptions{}); err != nil {
			if _, err := client.Resource(secretResource).Namespace("default").Create(t.Context(), secret, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := client.Resource(secretResource).Namespace("default").Update(t.Context(), secret, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := discoverAgentRuntimeToken(t.Context(), client, "default", "a")
		if len(value) < 32 {
			if err == nil || got != "" {
				t.Fatal("invalid credential did not fail closed")
			}
		} else if err != nil || got != value {
			t.Fatalf("fresh credential lookup failed: %v", err)
		}
	}
	if token, err := discoverAgentRuntimeToken(t.Context(), client, "default", "missing"); err == nil || token != "" {
		t.Fatal("missing Secret did not fail closed")
	}
}

func TestConfiguredRuntimeNeverUsesServiceToken(t *testing.T) {
	t.Setenv("AGENT_RUNTIME_URL", "http://runtime")
	t.Setenv("KIONGA_INTERNAL_TOKEN", runtimeTestToken())
	t.Setenv("KIONGA_AGENT_RUNTIME_TOKEN", "")
	if token, err := agentRuntimeToken(t.Context(), "a"); err != nil || token != "" {
		t.Fatal("configured runtime fell back to service token")
	}
}

func TestAgentRuntimeTokenLookupErrors(t *testing.T) {
	for _, mode := range []string{"forbidden", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			secret := runtimeSecretFixture("a", runtimeTestToken())
			if mode == "malformed" {
				_ = unstructured.SetNestedField(secret.Object, "invalid-base64!", "data", "KIONGA_INTERNAL_TOKEN")
			}
			client := fake.NewSimpleDynamicClient(runtime.NewScheme(), secret)
			if mode == "forbidden" {
				client.PrependReactor("get", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, fmt.Errorf("forbidden")
				})
			}
			if token, err := discoverAgentRuntimeToken(t.Context(), client, "default", "a"); err == nil || token != "" {
				t.Fatal("lookup error did not fail closed")
			}
		})
	}
}

func runtimeTestToken() string { return rand.Text() + rand.Text() }
