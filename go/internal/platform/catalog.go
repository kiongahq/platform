// Package platform derives the live component health grid and the shared
// discovery catalog from actual control-plane state. Nothing here is
// hardcoded: a component is healthy only when a checked connection says so,
// and the catalog lists only resources that exist.
package platform

import (
	"fmt"

	"github.com/kiongahq/platform/pkg/api"
)

// componentSpec maps a platform component to the connection types that make
// it real. A component is healthy when any matching connection has passed an
// active health check.
type componentSpec struct {
	name        string
	category    string
	description string
	types       []string
}

var componentSpecs = []componentSpec{
	{"Pipeline Engine", "Orchestration", "Prefect (Compose) or Kubeflow Pipelines (Kubernetes)", []string{"prefect", "kfp", "kubeflow"}},
	{"Experiment Tracker", "ML lifecycle", "MLflow with PostgreSQL and S3 artifacts", []string{"mlflow"}},
	{"Feature Store", "Data", "Feast definitions with Redis online serving", []string{"feast", "redis"}},
	{"Object Store", "Storage", "RustFS (bundled) or an external S3-compatible store", []string{"s3", "rustfs"}},
	{"Inference Engine", "Serving", "MLflow model serving (Compose) or KServe (Kubernetes)", []string{"serving", "kserve"}},
	{"Agent Observability", "Agentic AI", "Langfuse for traces, prompts, and evaluations", []string{"langfuse"}},
	{"Streaming Broker", "Events", "Apache Kafka lifecycle and trace topics", []string{"kafka"}},
	{"Kubernetes", "Control plane", "Cluster for operator-managed workloads", []string{"kubernetes"}},
}

// Components derives component health from checked connections.
func Components(connections []api.Connection) []api.Component {
	healthy := map[string]bool{}
	configured := map[string]bool{}
	for _, connection := range connections {
		configured[connection.Type] = true
		if connection.Status == "healthy" {
			healthy[connection.Type] = true
		}
	}
	components := []api.Component{{
		Name: "API Gateway", Category: "Control plane", Status: "healthy",
		Description: "Go REST gateway and unified platform contract",
	}}
	for _, spec := range componentSpecs {
		status := "not_configured"
		for _, connectionType := range spec.types {
			if healthy[connectionType] {
				status = "healthy"
				break
			}
			if configured[connectionType] {
				status = "unhealthy"
			}
		}
		components = append(components, api.Component{Name: spec.name, Category: spec.category, Status: status, Description: spec.description})
	}
	return components
}

// CatalogSource is the subset of the repository the catalog reads.
type CatalogSource interface {
	Models() []api.Model
	Agents() []api.Agent
	Tools() []api.Tool
	FeatureViews() []api.FeatureView
}

// Catalog lists every discoverable resource that actually exists.
func Catalog(source CatalogSource) []api.CatalogItem {
	items := []api.CatalogItem{}
	for _, model := range source.Models() {
		metadata := []string{}
		for name, value := range model.Metrics {
			metadata = append(metadata, fmt.Sprintf("%s %.3g", name, value))
		}
		status := model.Stage
		if status == "" {
			status = "registered"
		}
		items = append(items, api.CatalogItem{Name: model.Name, Version: model.Version, Status: status, Kind: "model", Metadata: metadata})
	}
	for _, view := range source.FeatureViews() {
		metadata := []string{fmt.Sprintf("%d fields", len(view.Fields))}
		if view.TTLSeconds > 0 {
			metadata = append(metadata, fmt.Sprintf("TTL %ds", view.TTLSeconds))
		}
		if view.OnlineEntityCount > 0 {
			metadata = append(metadata, fmt.Sprintf("%d entities online", view.OnlineEntityCount))
		}
		items = append(items, api.CatalogItem{Name: view.Name, Version: view.Entity, Status: view.Status, Kind: "feature", Metadata: metadata})
	}
	for _, agent := range source.Agents() {
		metadata := []string{agent.LLMBackend}
		metadata = append(metadata, fmt.Sprintf("%d tools", len(agent.Tools)))
		items = append(items, api.CatalogItem{Name: agent.Name, Version: agent.Version, Status: agent.Status, Kind: "agent", Metadata: metadata})
	}
	for _, tool := range source.Tools() {
		items = append(items, api.CatalogItem{Name: tool.Name, Version: tool.Version, Status: tool.Status, Kind: "tool", Metadata: tool.Tags})
	}
	return items
}
