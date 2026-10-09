package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/feature"
)

func memoryGateway(token string) *gateway {
	store := feature.NewMemoryStore()
	return &gateway{mode: "memory", store: store, token: token, metrics: newMetrics(),
		lookup: func(request feature.Request) (feature.Response, error) { return feature.Lookup(store, request) }}
}

func call(t *testing.T, handler http.Handler, method, path, body, auth string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth != "" {
		request.Header.Set("Authorization", auth)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func scrape(t *testing.T, handler http.Handler) string {
	t.Helper()
	body, _ := io.ReadAll(call(t, handler, http.MethodGet, "/metrics", "", "").Body)
	return string(body)
}

func TestUsageAndFailureCounters(t *testing.T) {
	g := memoryGateway("secret")
	handler := g.handler()
	if response := call(t, handler, http.MethodPut, "/internal/v1/features/customer_profile/user_id=u1", `{"plan":"pro"}`, "Bearer secret"); response.Code != http.StatusNoContent {
		t.Fatalf("write: %d %s", response.Code, response.Body.String())
	}
	if response := call(t, handler, http.MethodPut, "/internal/v1/features/customer_profile/user_id=u2", `{"plan":"pro"}`, "Bearer wrong"); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized write accepted: %d", response.Code)
	}
	lookup := `{"feature_service":"customer_profile","entities":[{"user_id":"u1"},{"user_id":"u9"}]}`
	if response := call(t, handler, http.MethodPost, "/get-online-features", lookup, ""); response.Code != http.StatusOK {
		t.Fatalf("lookup: %d %s", response.Code, response.Body.String())
	}
	call(t, handler, http.MethodPost, "/get-online-features", `{"feature_service":""}`, "")
	call(t, handler, http.MethodPost, "/get-online-features", `not json`, "")
	metrics := scrape(t, handler)
	for _, want := range []string{
		`kionga_feature_gateway_lookups_total{mode="memory",outcome="ok"} 1`,
		`kionga_feature_gateway_lookups_total{mode="memory",outcome="failed"} 1`,
		`kionga_feature_gateway_lookups_total{mode="memory",outcome="invalid_request"} 1`,
		`kionga_feature_gateway_lookup_entities_total{mode="memory"} 2`,
		`kionga_feature_gateway_lookup_entities_missing_total{mode="memory"} 1`,
		`kionga_feature_gateway_writes_total{mode="memory",outcome="ok"} 1`,
		`kionga_feature_gateway_writes_total{mode="memory",outcome="unauthorized"} 1`,
		`kionga_feature_gateway_lookup_duration_seconds_count{mode="memory"} 2`,
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("missing %s in\n%s", want, metrics)
		}
	}
}

func TestHealthReportsOnlineStoreFailure(t *testing.T) {
	g := memoryGateway("")
	g.mode = "redis"
	g.ping = func(context.Context) error { return errors.New("dial tcp redis:6379: connection refused") }
	handler := g.handler()
	response := call(t, handler, http.MethodGet, "/healthz", "", "")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"mode":"redis"`) {
		t.Fatalf("down Redis must fail health: %d %s", response.Code, response.Body.String())
	}
	g.ping = func(context.Context) error { return nil }
	if response := call(t, handler, http.MethodGet, "/healthz", "", ""); response.Code != http.StatusOK {
		t.Fatalf("healthy: %d", response.Code)
	}
	metrics := scrape(t, handler)
	if !strings.Contains(metrics, `kionga_feature_gateway_health_checks_total{mode="redis",outcome="failed"} 1`) {
		t.Fatalf("health failure not counted:\n%s", metrics)
	}
}

func TestFeastModeRefusesWrites(t *testing.T) {
	g := &gateway{mode: "feast", metrics: newMetrics(), lookup: func(feature.Request) (feature.Response, error) { return feature.Response{}, nil }}
	response := call(t, g.handler(), http.MethodPut, "/internal/v1/features/a/b=c", `{}`, "")
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("feast writes: %d", response.Code)
	}
}
