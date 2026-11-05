package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFeatureViewApplyListMaterialize(t *testing.T) {
	server := testServer()

	apply := newTestRequest(http.MethodPost, "/api/v1/features", strings.NewReader(
		`{"name":"customer_profile","entity":"user_id","fields":[{"name":"plan","type":"string"}],"tags":["demo"],"source":"s3://mlaiops-features/customer_profile","ttl_seconds":3600}`))
	applied := httptest.NewRecorder()
	server.ServeHTTP(applied, apply)
	if applied.Code != http.StatusCreated || !strings.Contains(applied.Body.String(), `"status":"registered"`) {
		t.Fatalf("apply failed: %d %s", applied.Code, applied.Body.String())
	}

	// Re-apply upserts instead of duplicating.
	reapply := newTestRequest(http.MethodPost, "/api/v1/features", strings.NewReader(
		`{"name":"customer_profile","entity":"user_id","fields":[{"name":"plan","type":"string"},{"name":"csat","type":"float"}]}`))
	reapplied := httptest.NewRecorder()
	server.ServeHTTP(reapplied, reapply)
	if reapplied.Code != http.StatusCreated {
		t.Fatalf("re-apply failed: %d %s", reapplied.Code, reapplied.Body.String())
	}

	list := newTestRequest(http.MethodGet, "/api/v1/features", nil)
	listed := httptest.NewRecorder()
	server.ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"total":1`) {
		t.Fatalf("expected one upserted view: %d %s", listed.Code, listed.Body.String())
	}
	if !strings.Contains(listed.Body.String(), `"csat"`) {
		t.Fatalf("re-apply did not update fields: %s", listed.Body.String())
	}

	materialize := newTestRequest(http.MethodPost, "/api/v1/features/customer_profile/materialized", strings.NewReader(`{"entity_count":42}`))
	materialized := httptest.NewRecorder()
	server.ServeHTTP(materialized, materialize)
	if materialized.Code != http.StatusOK || !strings.Contains(materialized.Body.String(), `"online_entity_count":42`) {
		t.Fatalf("materialization report failed: %d %s", materialized.Code, materialized.Body.String())
	}
	if !strings.Contains(materialized.Body.String(), `"status":"materialized"`) {
		t.Fatalf("status not advanced: %s", materialized.Body.String())
	}
}

func TestEmptyListsSerializeAsArrays(t *testing.T) {
	server := testServer()
	for _, path := range []string{"/api/v1/features", "/api/v1/models", "/api/v1/tools", "/api/v1/connections"} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, newTestRequest(http.MethodGet, path, nil))
		if strings.Contains(response.Body.String(), `"items":null`) {
			t.Fatalf("%s serializes empty list as null: %s", path, response.Body.String())
		}
	}
}

func TestMaterializeUnknownViewIs404(t *testing.T) {
	request := newTestRequest(http.MethodPost, "/api/v1/features/missing/materialized", strings.NewReader(`{"entity_count":1}`))
	response := httptest.NewRecorder()
	testServer().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.Code)
	}
}

func TestApplyFeatureViewValidation(t *testing.T) {
	request := newTestRequest(http.MethodPost, "/api/v1/features", strings.NewReader(`{"name":"x"}`))
	response := httptest.NewRecorder()
	testServer().ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d %s", response.Code, response.Body.String())
	}
}

func TestStorageEndpointsProxyToStorageProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/buckets":
			_, _ = w.Write([]byte(`{"buckets":[{"name":"mlaiops-models"}]}`))
		case "/objects":
			if r.URL.Query().Get("bucket") != "mlaiops-models" {
				t.Errorf("query not forwarded: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"bucket":"mlaiops-models","objects":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	t.Setenv("STORAGE_PROXY_URL", upstream.URL)

	server := testServer()
	buckets := httptest.NewRecorder()
	server.ServeHTTP(buckets, newTestRequest(http.MethodGet, "/api/v1/storage/buckets", nil))
	if buckets.Code != http.StatusOK || !strings.Contains(buckets.Body.String(), "mlaiops-models") {
		t.Fatalf("bucket proxy failed: %d %s", buckets.Code, buckets.Body.String())
	}
	objects := httptest.NewRecorder()
	server.ServeHTTP(objects, newTestRequest(http.MethodGet, "/api/v1/storage/objects?bucket=mlaiops-models&prefix=churn/", nil))
	if objects.Code != http.StatusOK {
		t.Fatalf("object proxy failed: %d %s", objects.Code, objects.Body.String())
	}
}

func TestPromptsReportsUnconfiguredHonestly(t *testing.T) {
	t.Setenv("LANGFUSE_URL", "")
	response := httptest.NewRecorder()
	testServer().ServeHTTP(response, newTestRequest(http.MethodGet, "/api/v1/prompts", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"configured":false`) {
		t.Fatalf("unexpected prompts response: %d %s", response.Code, response.Body.String())
	}
}

func TestPromptsProxiesLangfuse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "pk" || pass != "sk" {
			t.Errorf("basic auth not forwarded")
		}
		_, _ = w.Write([]byte(`{"data":[{"name":"support-v3","version":3}]}`))
	}))
	defer upstream.Close()
	t.Setenv("LANGFUSE_URL", upstream.URL)
	t.Setenv("LANGFUSE_PUBLIC_KEY", "pk")
	t.Setenv("LANGFUSE_SECRET_KEY", "sk")
	response := httptest.NewRecorder()
	testServer().ServeHTTP(response, newTestRequest(http.MethodGet, "/api/v1/prompts", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "support-v3") {
		t.Fatalf("langfuse proxy failed: %d %s", response.Code, response.Body.String())
	}
}
