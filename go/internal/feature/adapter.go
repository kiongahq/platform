package feature

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kiongahq/platform/pkg/api"
)

// Health states shared by every adapter and connection.
const (
	StateConfigured  = "configured"
	StateHealthy     = "healthy"
	StateDegraded    = "degraded"
	StateUnavailable = "unavailable"
)

// ErrNotSupported is returned by optional operations an adapter cannot
// perform (for example listing views through Feast's HTTP server).
var ErrNotSupported = errors.New("operation not supported by this feature store adapter")

// Issue is one validation problem in a connection's configuration.
type Issue struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Health is the result of an active check.
type Health struct {
	State  string `json:"state"`
	Detail string `json:"detail"`
}

// ViewInfo is a feature view as the store itself reports it.
type ViewInfo struct {
	Name       string   `json:"name"`
	Entity     string   `json:"entity,omitempty"`
	Features   []string `json:"features"`
	TTLSeconds int      `json:"ttl_seconds,omitempty"`
}

// Adapter is the contract every feature store integration implements. The
// gateway never talks to a provider except through it.
type Adapter interface {
	Name() string
	Capabilities() api.FeatureStoreCapabilities
	Health(ctx context.Context) Health
	ListFeatureViews(ctx context.Context) ([]ViewInfo, error)
	GetOnline(ctx context.Context, request Request) (Response, error)
}

// Config is a connection's non-secret settings plus its resolved secret.
type Config struct {
	Settings map[string]string
	Secret   string
}

// Provider describes one registry entry. Providers without New are listed
// as "contract available — no adapter".
type Provider struct {
	Name         string
	Title        string
	Kind         string
	Capabilities api.FeatureStoreCapabilities
	ConfigKeys   []string
	Validate     func(settings map[string]string) []Issue
	New          func(Config) (Adapter, error)
}

var registry = map[string]Provider{}

// Register adds a provider; registering a name twice panics at init.
func Register(provider Provider) {
	if _, exists := registry[provider.Name]; exists {
		panic("feature store provider registered twice: " + provider.Name)
	}
	registry[provider.Name] = provider
}

// Lookup returns a registered provider.
func LookupProvider(name string) (Provider, bool) {
	provider, ok := registry[name]
	return provider, ok
}

// Providers lists the registry for the API, adapters first.
func Providers() []api.FeatureStoreProvider {
	out := make([]api.FeatureStoreProvider, 0, len(registry))
	for _, provider := range registry {
		status := "adapter available"
		if provider.New == nil {
			status = "contract available — no adapter"
		}
		out = append(out, api.FeatureStoreProvider{Name: provider.Name, Title: provider.Title, Kind: provider.Kind, Adapter: provider.New != nil, Status: status, Capabilities: provider.Capabilities, ConfigKeys: append([]string{}, provider.ConfigKeys...)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Adapter != out[j].Adapter {
			return out[i].Adapter
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Validate checks a provider configuration: known keys only, no embedded
// credentials, then the provider's own rules.
func Validate(providerName string, settings map[string]string) []Issue {
	provider, ok := registry[providerName]
	if !ok {
		return []Issue{{Field: "provider", Message: "unknown feature store provider"}}
	}
	if provider.New == nil {
		return []Issue{{Field: "provider", Message: provider.Title + ": contract available — no adapter. Connections cannot be created until an adapter ships."}}
	}
	issues := []Issue{}
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !contains(provider.ConfigKeys, key) {
			issues = append(issues, Issue{Field: "config." + key, Message: "unsupported setting; allowed: " + strings.Join(provider.ConfigKeys, ", ")})
			continue
		}
		if issue := secretLooking(key, settings[key]); issue != "" {
			issues = append(issues, Issue{Field: "config." + key, Message: issue})
		}
	}
	if provider.Validate != nil {
		issues = append(issues, provider.Validate(settings)...)
	}
	return issues
}

// New builds a ready adapter for a validated configuration.
func New(providerName string, config Config) (Adapter, error) {
	provider, ok := registry[providerName]
	if !ok || provider.New == nil {
		return nil, fmt.Errorf("no adapter for provider %q", providerName)
	}
	if issues := Validate(providerName, config.Settings); len(issues) > 0 {
		return nil, fmt.Errorf("%s: %s", issues[0].Field, issues[0].Message)
	}
	return provider.New(config)
}

var secretRefPattern = regexp.MustCompile(`^env:[A-Z_][A-Z0-9_]{0,127}$`)

// ValidSecretRef reports whether ref is empty or an env:VAR reference.
func ValidSecretRef(ref string) bool { return ref == "" || secretRefPattern.MatchString(ref) }

func secretLooking(key, value string) string {
	lowered := strings.ToLower(key)
	for _, word := range []string{"password", "secret", "token", "credential", "apikey", "api_key", "access_key"} {
		if strings.Contains(lowered, word) {
			return "secrets are not accepted in config; use secret_ref (env:VAR)"
		}
	}
	if parsed, err := url.Parse(value); err == nil && parsed.User != nil {
		return "URLs must not embed credentials; use secret_ref (env:VAR)"
	}
	return ""
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func validHTTPURL(field, value string, required bool) []Issue {
	if value == "" {
		if required {
			return []Issue{{Field: "config." + field, Message: "is required"}}
		}
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return []Issue{{Field: "config." + field, Message: "must be an http(s) URL without query or fragment"}}
	}
	return nil
}

// Freshness compares the last successful materialization with the view's
// TTL. A zero TTL never goes stale once materialized.
func Freshness(materializedAt *time.Time, ttlSeconds int, now time.Time) api.FeatureFreshness {
	freshness := api.FeatureFreshness{State: "never", TTLSeconds: ttlSeconds}
	if materializedAt == nil || materializedAt.IsZero() {
		return freshness
	}
	age := now.Sub(*materializedAt)
	if age < 0 {
		age = 0
	}
	freshness.AgeSeconds = int64(age / time.Second)
	freshness.State = "fresh"
	if ttlSeconds > 0 && age > time.Duration(ttlSeconds)*time.Second {
		freshness.State = "stale"
	}
	return freshness
}

func init() {
	for _, provider := range []Provider{
		{Name: "tecton", Title: "Tecton", Kind: "external"},
		{Name: "hopsworks", Title: "Hopsworks", Kind: "external"},
		{Name: "vertex-ai", Title: "Vertex AI Feature Store", Kind: "external"},
		{Name: "sagemaker", Title: "Amazon SageMaker Feature Store", Kind: "external"},
		{Name: "databricks", Title: "Databricks Feature Engineering", Kind: "external"},
	} {
		Register(provider)
	}
}
