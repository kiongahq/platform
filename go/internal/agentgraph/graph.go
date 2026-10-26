package agentgraph

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// SchemaVersion is the graph version writers emit.
const SchemaVersion = 2

// Default run budgets (contracts agent-graph/v2.schema.json).
const (
	DefaultMaxSteps       = 50
	DefaultTimeoutSeconds = 60
	DefaultMaxTokens      = 50000
)

// Node is one component instance on the canvas.
type Node struct {
	ID     string            `json:"id"`
	Kind   string            `json:"kind"`
	Name   string            `json:"name"`
	X      float64           `json:"x"`
	Y      float64           `json:"y"`
	Group  string            `json:"group,omitempty"`
	Config map[string]string `json:"config,omitempty"`
}

// Edge joins an output port to an input port. Empty ports mean the
// component's first output or input (version 1 drafts store none).
type Edge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	FromPort string `json:"from_port,omitempty"`
	ToPort   string `json:"to_port,omitempty"`
}

// Settings are run budgets; zero means the default.
type Settings struct {
	MaxSteps       int `json:"max_steps,omitempty"`
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	MaxTokens      int `json:"max_tokens,omitempty"`
}

// Graph is the executable part of a design.
type Graph struct {
	SchemaVersion int       `json:"schema_version,omitempty"`
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	Nodes         []Node    `json:"nodes"`
	Edges         []Edge    `json:"edges"`
	Settings      *Settings `json:"settings,omitempty"`
}

// Normalize upgrades a graph in place to schema version 2 without changing
// what it does: default ports, default settings and the llm model default.
// It is idempotent and leaves unknown kinds and ports for Validate to report.
func (r *Registry) Normalize(graph *Graph) {
	graph.SchemaVersion = SchemaVersion
	kinds := make(map[string]string, len(graph.Nodes))
	for index := range graph.Nodes {
		node := &graph.Nodes[index]
		kinds[node.ID] = node.Kind
		if node.Kind == "model" && strings.TrimSpace(node.Config["model"]) == "" {
			if node.Config == nil {
				node.Config = map[string]string{}
			}
			node.Config["model"] = "llm"
		}
	}
	for index := range graph.Edges {
		edge := &graph.Edges[index]
		if edge.FromPort == "" {
			if component := r.Component(kinds[edge.From]); component != nil && len(component.Outputs) > 0 {
				edge.FromPort = component.Outputs[0].Name
			}
		}
		if edge.ToPort == "" {
			if component := r.Component(kinds[edge.To]); component != nil && len(component.Inputs) > 0 {
				edge.ToPort = component.Inputs[0].Name
			}
		}
	}
	settings := Settings{}
	if graph.Settings != nil {
		settings = *graph.Settings
	}
	if settings.MaxSteps == 0 {
		settings.MaxSteps = DefaultMaxSteps
	}
	if settings.TimeoutSeconds == 0 {
		settings.TimeoutSeconds = DefaultTimeoutSeconds
	}
	if settings.MaxTokens == 0 {
		settings.MaxTokens = DefaultMaxTokens
	}
	graph.Settings = &settings
}

// Canonical returns the canonical behavior document defined in the
// contract: layout, labels and empty settings are excluded so moving a node
// never changes the hash.
func Canonical(graph Graph) ([]byte, error) {
	nodes := make([]map[string]any, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		config := map[string]string{}
		for key, value := range node.Config {
			if value != "" {
				config[key] = value
			}
		}
		nodes = append(nodes, map[string]any{"id": node.ID, "kind": node.Kind, "config": config})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i]["id"].(string) < nodes[j]["id"].(string) })
	edges := append([]Edge(nil), graph.Edges...)
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.FromPort != b.FromPort {
			return a.FromPort < b.FromPort
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.ToPort < b.ToPort
	})
	edgeDocs := make([]map[string]string, 0, len(edges))
	for _, edge := range edges {
		item := map[string]string{"from": edge.From, "to": edge.To}
		if edge.FromPort != "" {
			item["from_port"] = edge.FromPort
		}
		if edge.ToPort != "" {
			item["to_port"] = edge.ToPort
		}
		edgeDocs = append(edgeDocs, item)
	}
	settings := map[string]int{}
	if graph.Settings != nil {
		if graph.Settings.MaxSteps != 0 {
			settings["max_steps"] = graph.Settings.MaxSteps
		}
		if graph.Settings.TimeoutSeconds != 0 {
			settings["timeout_seconds"] = graph.Settings.TimeoutSeconds
		}
		if graph.Settings.MaxTokens != 0 {
			settings["max_tokens"] = graph.Settings.MaxTokens
		}
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]any{"schema_version": SchemaVersion, "nodes": nodes, "edges": edgeDocs, "settings": settings}); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// Hash is the hex SHA-256 of Canonical(graph).
func Hash(graph Graph) (string, error) {
	canonical, err := Canonical(graph)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
