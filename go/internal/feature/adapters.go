package feature

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/kiongahq/platform/pkg/api"
	"github.com/redis/go-redis/v9"
)

// InternalCapabilities is what the platform-managed store provides: Redis
// online serving with TTLs and push writes, Parquet offline snapshots,
// materialization and the Python point-in-time join.
var InternalCapabilities = api.FeatureStoreCapabilities{Online: true, Offline: true, PointInTime: true, Materialize: true, TTL: true, Push: true}

// FeastCapabilities is what Kionga drives through Feast's HTTP feature
// server: online reads, with Feast enforcing view TTLs server side. Offline
// retrieval and materialization stay in the Feast SDK/CLI.
var FeastCapabilities = api.FeatureStoreCapabilities{Online: true, TTL: true}

// OnlineBackend is the online half of the internal store.
type OnlineBackend interface {
	Ping(ctx context.Context) error
	Lookup(ctx context.Context, request Request) (Response, error)
	Views(ctx context.Context) ([]ViewInfo, error)
	Location() string
}

// InternalAdapter is the platform-managed store: an online backend (Redis,
// or the feature-gateway in front of it) plus Parquet snapshots under
// OfflineURI.
type InternalAdapter struct {
	Online     OnlineBackend
	OfflineURI string
}

func (a *InternalAdapter) Name() string                               { return "internal" }
func (a *InternalAdapter) Capabilities() api.FeatureStoreCapabilities { return InternalCapabilities }

func (a *InternalAdapter) Health(ctx context.Context) Health {
	if a.Online == nil {
		return Health{State: StateConfigured, Detail: "No online store is configured"}
	}
	if err := a.Online.Ping(ctx); err != nil {
		return Health{State: StateUnavailable, Detail: fmt.Sprintf("Online store %s is unreachable: %v", a.Online.Location(), err)}
	}
	if a.OfflineURI == "" {
		return Health{State: StateDegraded, Detail: fmt.Sprintf("Online store %s is reachable, but no offline snapshot location is configured, so training reads and point-in-time joins are unavailable", a.Online.Location())}
	}
	return Health{State: StateHealthy, Detail: fmt.Sprintf("Online store %s is reachable; offline snapshots at %s", a.Online.Location(), a.OfflineURI)}
}

func (a *InternalAdapter) ListFeatureViews(ctx context.Context) ([]ViewInfo, error) {
	if a.Online == nil {
		return nil, ErrNotSupported
	}
	return a.Online.Views(ctx)
}

func (a *InternalAdapter) GetOnline(ctx context.Context, request Request) (Response, error) {
	if a.Online == nil {
		return Response{}, ErrNotSupported
	}
	return a.Online.Lookup(ctx, request)
}

// MemoryBackend adapts MemoryStore for tests and dependency-free runs.
type MemoryBackend struct{ Store *MemoryStore }

func (m MemoryBackend) Ping(context.Context) error { return nil }
func (m MemoryBackend) Location() string           { return "memory" }
func (m MemoryBackend) Lookup(_ context.Context, request Request) (Response, error) {
	return Lookup(m.Store, request)
}
func (m MemoryBackend) Views(context.Context) ([]ViewInfo, error) {
	m.Store.mu.RLock()
	defer m.Store.mu.RUnlock()
	views := []ViewInfo{}
	for service, rows := range m.Store.data {
		features := map[string]bool{}
		for _, values := range rows {
			for name := range values {
				features[name] = true
			}
		}
		views = append(views, ViewInfo{Name: service, Features: sortedKeys(features)})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views, nil
}

// RedisBackend reads the platform key convention directly.
type RedisBackend struct {
	Store *RedisStore
	Addr  string
}

func (r RedisBackend) Ping(ctx context.Context) error { return r.Store.Ping(ctx) }
func (r RedisBackend) Location() string               { return "redis://" + r.Addr }
func (r RedisBackend) Lookup(_ context.Context, request Request) (Response, error) {
	return Lookup(r.Store, request)
}

// Views scans at most 10k keys; it is a diagnostic listing, not an index.
func (r RedisBackend) Views(ctx context.Context) ([]ViewInfo, error) {
	features := map[string]map[string]bool{}
	iterator := r.Store.client.Scan(ctx, 0, "mlaiops:features:*", 500).Iterator()
	for scanned := 0; iterator.Next(ctx) && scanned < 10000; scanned++ {
		rest := strings.TrimPrefix(iterator.Val(), "mlaiops:features:")
		service, _, ok := strings.Cut(rest, ":")
		if ok && features[service] == nil {
			features[service] = map[string]bool{}
		}
	}
	if err := iterator.Err(); err != nil {
		return nil, err
	}
	views := []ViewInfo{}
	for service := range features {
		views = append(views, ViewInfo{Name: service, Features: []string{}})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views, nil
}

// GatewayBackend talks to the feature-gateway service over HTTP, which is
// how the control plane reaches the internal store in Compose.
type GatewayBackend struct {
	BaseURL string
	Client  *http.Client
}

func (g GatewayBackend) Location() string { return g.BaseURL }
func (g GatewayBackend) Ping(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.BaseURL, "/")+"/healthz", nil)
	if err != nil {
		return err
	}
	response, err := g.client().Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("health check returned %s %s", response.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
func (g GatewayBackend) Lookup(ctx context.Context, request Request) (Response, error) {
	body, _ := json.Marshal(request)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.BaseURL, "/")+"/get-online-features", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := g.client().Do(httpRequest)
	if err != nil {
		return Response{}, fmt.Errorf("feature gateway unreachable: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("feature gateway returned %s", response.Status)
	}
	var decoded Response
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return Response{}, fmt.Errorf("invalid feature gateway response: %w", err)
	}
	return decoded, nil
}
func (g GatewayBackend) Views(context.Context) ([]ViewInfo, error) { return nil, ErrNotSupported }
func (g GatewayBackend) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 5 * time.Second}
}

// FeastAdapter delegates to a Feast feature server.
type FeastAdapter struct {
	Client   *FeastClient
	Services []string
}

func (a *FeastAdapter) Name() string                               { return "feast" }
func (a *FeastAdapter) Capabilities() api.FeatureStoreCapabilities { return FeastCapabilities }

// Health calls Feast's GET /health, which returns 200 with an empty body
// when the server and its registry are loaded.
func (a *FeastAdapter) Health(ctx context.Context) Health {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(a.Client.BaseURL, "/")+"/health", nil)
	if err != nil {
		return Health{State: StateUnavailable, Detail: err.Error()}
	}
	if a.Client.Token != "" {
		request.Header.Set("Authorization", "Bearer "+a.Client.Token)
	}
	started := time.Now()
	response, err := a.Client.Client.Do(request)
	if err != nil {
		return Health{State: StateUnavailable, Detail: "Feast feature server is unreachable: " + redactURLError(err)}
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusOK && time.Since(started) > 2*time.Second:
		return Health{State: StateDegraded, Detail: fmt.Sprintf("Feast responded in %s; online lookups will miss latency budgets", time.Since(started).Round(time.Millisecond))}
	case response.StatusCode == http.StatusOK:
		return Health{State: StateHealthy, Detail: "Feast feature server is serving at " + a.Client.BaseURL}
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return Health{State: StateUnavailable, Detail: fmt.Sprintf("Feast rejected the credential (%s); check the secret reference", response.Status)}
	default:
		return Health{State: StateDegraded, Detail: fmt.Sprintf("Feast health check returned %s", response.Status)}
	}
}

func (a *FeastAdapter) ListFeatureViews(context.Context) ([]ViewInfo, error) {
	if len(a.Services) == 0 {
		return nil, ErrNotSupported
	}
	views := make([]ViewInfo, 0, len(a.Services))
	for _, service := range a.Services {
		views = append(views, ViewInfo{Name: service, Features: []string{}})
	}
	return views, nil
}

func (a *FeastAdapter) GetOnline(ctx context.Context, request Request) (Response, error) {
	return a.Client.LookupContext(ctx, request)
}

// redactURLError drops the request URL from transport errors so a check
// never echoes connection details beyond the configured base URL.
func redactURLError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error()
	}
	return err.Error()
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func init() {
	Register(Provider{
		Name: "internal", Title: "Kionga internal store", Kind: "internal",
		Capabilities: InternalCapabilities,
		ConfigKeys:   []string{"online_url", "offline_uri"},
		Validate: func(settings map[string]string) []Issue {
			issues := []Issue{}
			online := settings["online_url"]
			parsed, err := url.Parse(online)
			switch {
			case online == "":
				issues = append(issues, Issue{Field: "config.online_url", Message: "is required (redis://host:6379/0 or the feature-gateway http(s) URL)"})
			case err != nil || parsed.Host == "" || (parsed.Scheme != "redis" && parsed.Scheme != "rediss" && parsed.Scheme != "http" && parsed.Scheme != "https"):
				issues = append(issues, Issue{Field: "config.online_url", Message: "must be redis://, rediss:// or http(s):// with a host"})
			}
			if offline := settings["offline_uri"]; offline != "" {
				parsed, err := url.Parse(offline)
				if err != nil || parsed.Scheme != "s3" || parsed.Host == "" {
					issues = append(issues, Issue{Field: "config.offline_uri", Message: "must be s3://bucket/prefix"})
				}
			}
			return issues
		},
		New: func(config Config) (Adapter, error) {
			online := config.Settings["online_url"]
			parsed, _ := url.Parse(online)
			adapter := &InternalAdapter{OfflineURI: config.Settings["offline_uri"]}
			if parsed.Scheme == "http" || parsed.Scheme == "https" {
				adapter.Online = GatewayBackend{BaseURL: online}
				return adapter, nil
			}
			options, err := redis.ParseURL(online)
			if err != nil {
				return nil, err
			}
			options.Password = config.Secret
			options.DialTimeout, options.ReadTimeout = 2*time.Second, 2*time.Second
			adapter.Online = RedisBackend{Store: &RedisStore{client: redis.NewClient(options)}, Addr: options.Addr}
			return adapter, nil
		},
	})
	Register(Provider{
		Name: "feast", Title: "Feast feature server", Kind: "external",
		Capabilities: FeastCapabilities,
		ConfigKeys:   []string{"url", "feature_services"},
		Validate: func(settings map[string]string) []Issue {
			return validHTTPURL("url", settings["url"], true)
		},
		New: func(config Config) (Adapter, error) {
			client := NewFeastClient(strings.TrimRight(config.Settings["url"], "/"))
			client.Token = config.Secret
			services := []string{}
			for _, service := range strings.Split(config.Settings["feature_services"], ",") {
				if service = strings.TrimSpace(service); service != "" {
					services = append(services, service)
				}
			}
			return &FeastAdapter{Client: client, Services: services}, nil
		},
	})
}
