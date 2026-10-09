package feature

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ml-ai-ops/platform/pkg/api"
)

// seeded is the shared fixture every conformant adapter must serve:
// customer_profile for user_id=u1 (present) and user_id=u404 (absent).
var seeded = map[string]map[string]any{"u1": {"plan": "pro", "open_tickets": float64(2)}}

// fakeFeast reproduces Feast's HTTP feature server contract
// (sdk/python/feast/feature_server.py): GET /health answers 200 with an
// empty body; POST /get-online-features takes columnar entities and returns
// metadata.feature_names with one result column per name, entity key first,
// each carrying values, statuses and event_timestamps.
func fakeFeast(t *testing.T, token string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/get-online-features":
			var body struct {
				FeatureService string           `json:"feature_service"`
				Entities       map[string][]any `json:"entities"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.FeatureService != "customer_profile" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			ids := body.Entities["user_id"]
			type column struct {
				Values     []any    `json:"values"`
				Statuses   []string `json:"statuses"`
				Timestamps []string `json:"event_timestamps"`
			}
			names := []string{"user_id", "plan", "open_tickets"}
			columns := make([]column, len(names))
			for _, id := range ids {
				row, found := seeded[id.(string)]
				for i, name := range names {
					value, status := any(nil), "NOT_FOUND"
					if name == "user_id" {
						value, status = id, "PRESENT"
					} else if found {
						value, status = row[name], "PRESENT"
					}
					columns[i].Values = append(columns[i].Values, value)
					columns[i].Statuses = append(columns[i].Statuses, status)
					columns[i].Timestamps = append(columns[i].Timestamps, "2026-10-01T00:00:00Z")
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"feature_names": names}, "results": columns})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// conformance is the contract every adapter must satisfy.
func conformance(t *testing.T, provider string, adapter Adapter) {
	t.Helper()
	ctx := context.Background()
	registered, ok := LookupProvider(provider)
	if !ok || adapter.Name() != provider {
		t.Fatalf("adapter name %q must match registered provider %q", adapter.Name(), provider)
	}
	if adapter.Capabilities() != registered.Capabilities {
		t.Fatalf("capabilities %+v differ from registry %+v", adapter.Capabilities(), registered.Capabilities)
	}
	if health := adapter.Health(ctx); health.State != StateHealthy || health.Detail == "" {
		t.Fatalf("working store must be healthy with a reason: %+v", health)
	}
	response, err := adapter.GetOnline(ctx, Request{FeatureService: "customer_profile", Entities: []map[string]any{{"user_id": "u1"}, {"user_id": "u404"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 {
		t.Fatalf("one result per entity, got %d", len(response.Results))
	}
	if response.Results[0].Values["plan"] != "pro" || response.Results[0].Values["open_tickets"] != float64(2) {
		t.Fatalf("present entity values wrong: %+v", response.Results[0])
	}
	missing := false
	for _, status := range response.Results[1].Statuses {
		missing = missing || status == "NOT_FOUND"
	}
	if !missing {
		t.Fatalf("absent entity must report NOT_FOUND: %+v", response.Results[1])
	}
	if _, err := adapter.GetOnline(ctx, Request{}); err == nil {
		t.Fatal("empty request must be rejected")
	}
	if views, err := adapter.ListFeatureViews(ctx); err != nil && !errors.Is(err, ErrNotSupported) {
		t.Fatalf("list views: %v", err)
	} else if err == nil && len(views) == 0 {
		t.Fatal("a store with data must list at least one view or report ErrNotSupported")
	}
}

func memoryInternal() *InternalAdapter {
	memory := NewMemoryStore()
	memory.Put("customer_profile", "user_id=u1", seeded["u1"])
	return &InternalAdapter{Online: MemoryBackend{Store: memory}, OfflineURI: "s3://mlaiops-features"}
}

func TestInternalAdapterConformance(t *testing.T) {
	conformance(t, "internal", memoryInternal())
	views, err := memoryInternal().ListFeatureViews(context.Background())
	if err != nil || len(views) != 1 || views[0].Name != "customer_profile" || strings.Join(views[0].Features, ",") != "open_tickets,plan" {
		t.Fatalf("internal store lists its views: %+v %v", views, err)
	}
}

func TestInternalAdapterThroughFeatureGatewayConformance(t *testing.T) {
	memory := NewMemoryStore()
	memory.Put("customer_profile", "user_id=u1", seeded["u1"])
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/get-online-features":
			var request Request
			_ = json.NewDecoder(r.Body).Decode(&request)
			response, err := Lookup(memory, request)
			if err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			_ = json.NewEncoder(w).Encode(response)
		}
	}))
	defer gateway.Close()
	adapter, err := New("internal", Config{Settings: map[string]string{"online_url": gateway.URL, "offline_uri": "s3://mlaiops-features"}})
	if err != nil {
		t.Fatal(err)
	}
	conformance(t, "internal", adapter)
}

func TestFeastAdapterConformance(t *testing.T) {
	server := fakeFeast(t, "feast-secret")
	defer server.Close()
	adapter, err := New("feast", Config{Settings: map[string]string{"url": server.URL, "feature_services": "customer_profile"}, Secret: "feast-secret"})
	if err != nil {
		t.Fatal(err)
	}
	conformance(t, "feast", adapter)
}

func TestHealthStates(t *testing.T) {
	ctx := context.Background()
	degraded := memoryInternal()
	degraded.OfflineURI = ""
	if health := degraded.Health(ctx); health.State != StateDegraded || !strings.Contains(health.Detail, "offline") {
		t.Fatalf("missing offline store must degrade: %+v", health)
	}
	if health := (&InternalAdapter{}).Health(ctx); health.State != StateConfigured {
		t.Fatalf("no backend is only configured: %+v", health)
	}
	down, _ := New("internal", Config{Settings: map[string]string{"online_url": "http://127.0.0.1:1"}})
	if health := down.Health(ctx); health.State != StateUnavailable {
		t.Fatalf("unreachable online store must be unavailable: %+v", health)
	}
	unreachable, _ := New("feast", Config{Settings: map[string]string{"url": "http://127.0.0.1:1"}})
	if health := unreachable.Health(ctx); health.State != StateUnavailable {
		t.Fatalf("unreachable feast: %+v", health)
	}
	server := fakeFeast(t, "right")
	defer server.Close()
	wrong, _ := New("feast", Config{Settings: map[string]string{"url": server.URL}, Secret: "wrong"})
	if health := wrong.Health(ctx); health.State != StateUnavailable || strings.Contains(health.Detail, "wrong") {
		t.Fatalf("rejected credential is unavailable and never echoed: %+v", health)
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer failing.Close()
	sick, _ := New("feast", Config{Settings: map[string]string{"url": failing.URL}})
	if health := sick.Health(ctx); health.State != StateDegraded {
		t.Fatalf("5xx health must degrade: %+v", health)
	}
}

func TestValidationRejectsSecretsAndUnknownProviders(t *testing.T) {
	cases := []struct {
		provider string
		settings map[string]string
		field    string
	}{
		{"internal", map[string]string{"online_url": "redis://user:hunter2@redis:6379"}, "config.online_url"},
		{"internal", map[string]string{"online_url": "redis://redis:6379", "password": "x"}, "config.password"},
		{"internal", map[string]string{"online_url": "ftp://redis"}, "config.online_url"},
		{"internal", map[string]string{"online_url": "redis://redis:6379", "offline_uri": "/var/data"}, "config.offline_uri"},
		{"feast", map[string]string{}, "config.url"},
		{"feast", map[string]string{"url": "http://feast:6566?token=abc"}, "config.url"},
		{"tecton", map[string]string{}, "provider"},
		{"nope", map[string]string{}, "provider"},
	}
	for _, c := range cases {
		issues := Validate(c.provider, c.settings)
		found := false
		for _, issue := range issues {
			found = found || issue.Field == c.field
			if strings.Contains(issue.Message, "hunter2") {
				t.Fatalf("validation echoed a secret: %+v", issue)
			}
		}
		if !found {
			t.Fatalf("%s %v: expected issue on %s, got %+v", c.provider, c.settings, c.field, issues)
		}
	}
	if issues := Validate("internal", map[string]string{"online_url": "redis://redis:6379/0", "offline_uri": "s3://mlaiops-features"}); len(issues) != 0 {
		t.Fatalf("valid config rejected: %+v", issues)
	}
	for _, ref := range []string{"env:FEAST_TOKEN", ""} {
		if !ValidSecretRef(ref) {
			t.Fatalf("%q must be valid", ref)
		}
	}
	for _, ref := range []string{"FEAST_TOKEN", "env:lower", "env:A;rm", "vault:x", "env:"} {
		if ValidSecretRef(ref) {
			t.Fatalf("%q must be rejected", ref)
		}
	}
}

func TestRegistryReportsCapabilitiesHonestly(t *testing.T) {
	providers := Providers()
	if !providers[0].Adapter || providers[len(providers)-1].Adapter {
		t.Fatal("adapters must be listed before contract-only providers")
	}
	byName := map[string]api.FeatureStoreProvider{}
	for _, provider := range providers {
		byName[provider.Name] = provider
	}
	if byName["internal"].Capabilities != InternalCapabilities || !byName["internal"].Capabilities.PointInTime {
		t.Fatalf("internal capabilities: %+v", byName["internal"])
	}
	if feast := byName["feast"].Capabilities; !feast.Online || feast.Offline || feast.Materialize {
		t.Fatalf("feast capabilities must only claim what the HTTP adapter drives: %+v", feast)
	}
	if byName["tecton"].Adapter || byName["tecton"].Status != "contract available — no adapter" {
		t.Fatalf("contract-only providers must say so: %+v", byName["tecton"])
	}
	if _, err := New("tecton", Config{}); err == nil {
		t.Fatal("contract-only providers cannot be constructed")
	}
}

func TestFreshness(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) *time.Time { value := now.Add(-ago); return &value }
	cases := []struct {
		materialized *time.Time
		ttl          int
		state        string
	}{
		{nil, 60, "never"},
		{at(30 * time.Second), 60, "fresh"},
		{at(60 * time.Second), 60, "fresh"},
		{at(61 * time.Second), 60, "stale"},
		{at(1000 * time.Hour), 0, "fresh"},
		{at(-time.Minute), 60, "fresh"},
	}
	for _, c := range cases {
		if got := Freshness(c.materialized, c.ttl, now); got.State != c.state {
			t.Fatalf("materialized=%v ttl=%d: got %s want %s", c.materialized, c.ttl, got.State, c.state)
		}
	}
	if got := Freshness(at(90*time.Second), 60, now); got.AgeSeconds != 90 || got.TTLSeconds != 60 {
		t.Fatalf("age reported: %+v", got)
	}
}
