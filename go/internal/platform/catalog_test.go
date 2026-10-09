package platform

import (
	"testing"

	"github.com/kiongahq/platform/pkg/api"
)

type fakeSource struct {
	models   []api.Model
	agents   []api.Agent
	tools    []api.Tool
	features []api.FeatureView
}

func (f fakeSource) Models() []api.Model             { return f.models }
func (f fakeSource) Agents() []api.Agent             { return f.agents }
func (f fakeSource) Tools() []api.Tool               { return f.tools }
func (f fakeSource) FeatureViews() []api.FeatureView { return f.features }

func TestComponentsDeriveFromConnections(t *testing.T) {
	components := Components([]api.Connection{
		{Type: "mlflow", Status: "healthy"},
		{Type: "kafka", Status: "unhealthy"},
	})
	byName := map[string]string{}
	for _, component := range components {
		byName[component.Name] = component.Status
	}
	if byName["API Gateway"] != "healthy" {
		t.Fatalf("gateway must self-report healthy: %v", byName)
	}
	if byName["Experiment Tracker"] != "healthy" {
		t.Fatalf("mlflow connection must mark tracker healthy: %v", byName)
	}
	if byName["Streaming Broker"] != "unhealthy" {
		t.Fatalf("failed check must show unhealthy, not hidden: %v", byName)
	}
	if byName["Feature Store"] != "not_configured" {
		t.Fatalf("unconnected component must be not_configured: %v", byName)
	}
}

func TestCatalogListsOnlyRealResources(t *testing.T) {
	if items := Catalog(fakeSource{}); len(items) != 0 {
		t.Fatalf("empty platform must have an empty catalog, got %v", items)
	}
	items := Catalog(fakeSource{
		models:   []api.Model{{Name: "churn", Version: "2", Stage: "production", Metrics: map[string]float64{"auc": 0.91}}},
		features: []api.FeatureView{{Name: "customer_profile", Entity: "user_id", Status: "materialized", Fields: []api.FeatureField{{Name: "plan", Type: "string"}}, OnlineEntityCount: 42}},
		agents:   []api.Agent{{Name: "support", Version: "1.0", Status: "ready", LLMBackend: "openai"}},
		tools:    []api.Tool{{Name: "kb_search", Version: "1.0", Status: "ready", Tags: []string{"retrieval"}}},
	})
	kinds := map[string]int{}
	for _, item := range items {
		kinds[item.Kind]++
	}
	if kinds["model"] != 1 || kinds["feature"] != 1 || kinds["agent"] != 1 || kinds["tool"] != 1 {
		t.Fatalf("catalog must list each resource kind once: %v", kinds)
	}
}
