package agentgraph

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The embedded registry and templates must be byte-identical to the
// contracts submodule, so the gateway and agent-runtime agree.
func TestEmbeddedContractMatchesSubmodule(t *testing.T) {
	root := "../../../contracts/agent-graph"
	if _, err := os.Stat(root); err != nil {
		t.Skip("contracts submodule is not checked out")
	}
	pairs := map[string]string{"components.v1.json": "components.v1.json"}
	entries, err := os.ReadDir(filepath.Join(root, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		pairs["templates/"+entry.Name()] = "templates/" + entry.Name()
	}
	embedded, _ := contractFiles.ReadDir("templates")
	if len(embedded) != len(entries) {
		t.Fatalf("embedded %d templates, contracts has %d", len(embedded), len(entries))
	}
	for local, upstream := range pairs {
		want, err := os.ReadFile(filepath.Join(root, upstream))
		if err != nil {
			t.Fatal(err)
		}
		got, err := contractFiles.ReadFile(local)
		if err != nil {
			t.Fatalf("%s is not embedded: %v", local, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s differs from contracts; copy it from contracts/agent-graph", local)
		}
	}
}

func readGraph(t *testing.T, path string) Graph {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var graph Graph
	if err := json.Unmarshal(raw, &graph); err != nil {
		t.Fatal(err)
	}
	return graph
}

func TestCanonicalHashMatchesContractFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/hash-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Graph          Graph  `json:"graph"`
		Canonical      string `json:"canonical"`
		ExpectedSHA256 string `json:"expected_sha256"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	canonical, err := Canonical(fixture.Graph)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != fixture.Canonical {
		t.Fatalf("canonical mismatch\n got %s\nwant %s", canonical, fixture.Canonical)
	}
	hash, _ := Hash(fixture.Graph)
	if hash != fixture.ExpectedSHA256 {
		t.Fatalf("hash %s, want %s", hash, fixture.ExpectedSHA256)
	}
	moved := fixture.Graph
	moved.Nodes = append([]Node(nil), fixture.Graph.Nodes...)
	moved.Nodes[0].X, moved.Nodes[0].Name, moved.Name = 9999, "Renamed", "Other"
	if again, _ := Hash(moved); again != hash {
		t.Fatal("layout or labels changed the behavior hash")
	}
}

func TestVersionOneDraftMigratesWithoutLosingContent(t *testing.T) {
	registry := Default()
	graph := readGraph(t, "testdata/v1-draft.json")
	before, _ := json.Marshal(graph.Nodes)
	registry.Normalize(&graph)
	if graph.SchemaVersion != 2 || graph.Settings == nil || graph.Settings.MaxSteps != DefaultMaxSteps {
		t.Fatalf("not upgraded: %+v", graph)
	}
	for _, edge := range graph.Edges {
		if edge.FromPort == "" || edge.ToPort == "" {
			t.Fatalf("edge ports not defaulted: %+v", edge)
		}
	}
	if graph.Edges[2].FromPort != "true" || graph.Edges[2].From != "condition-1" {
		t.Fatalf("condition edge should default to the true branch: %+v", graph.Edges[2])
	}
	if graph.Nodes[4].Config["model"] != "llm" {
		t.Fatalf("empty model should default to llm: %+v", graph.Nodes[4])
	}
	again := graph
	registry.Normalize(&again)
	if first, _ := Hash(graph); first != mustHash(t, again) {
		t.Fatal("Normalize is not idempotent")
	}
	issues := registry.Validate(graph, nil)
	codes := map[string]string{}
	for _, issue := range issues {
		codes[issue.Code] = issue.Severity
	}
	if codes["invalid_condition"] != SeverityError || codes["legacy_setting"] != SeverityWarning {
		t.Fatalf("legacy free text should be reported, got %+v", issues)
	}
	after, _ := json.Marshal(graph.Nodes)
	if !bytes.Contains(after, []byte("route urgent tickets to a human")) || len(after) < len(before) {
		t.Fatal("migration dropped stored configuration")
	}
}

func mustHash(t *testing.T, graph Graph) string {
	t.Helper()
	hash, err := Hash(graph)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestTemplatesValidateExceptForResourcesTheUserPicks(t *testing.T) {
	registry := Default()
	templates := Templates()
	if len(templates) < 6 {
		t.Fatalf("expected the starter templates, got %d", len(templates))
	}
	for _, template := range templates {
		graph := template.Graph
		registry.Normalize(&graph)
		for _, issue := range registry.Validate(graph, func(string, string) bool { return true }) {
			if issue.Severity == SeverityError && issue.Code != "missing_setting" {
				t.Errorf("%s: %+v", template.ID, issue)
			}
		}
	}
}

func validGraph() Graph {
	return Graph{Name: "t", Nodes: []Node{
		{ID: "input", Kind: "input", Name: "In"},
		{ID: "route", Kind: "condition", Name: "Route", Config: map[string]string{"expression": "contains:refund"}},
		{ID: "kb", Kind: "knowledge", Name: "KB", Config: map[string]string{"knowledge_base": "kb-1", "limit": "4"}},
		{ID: "model", Kind: "model", Name: "Model", Config: map[string]string{"model": "llm"}},
		{ID: "output", Kind: "output", Name: "Out"},
	}, Edges: []Edge{{From: "input", To: "route"}, {From: "route", To: "kb", FromPort: "true"}, {From: "kb", To: "model"}, {From: "route", To: "model", FromPort: "false"}, {From: "model", To: "output"}}}
}

func TestValidateReportsEachDefect(t *testing.T) {
	registry := Default()
	cases := []struct {
		name   string
		mutate func(*Graph)
		code   string
	}{
		{"unknown port", func(g *Graph) { g.Edges[1].FromPort = "maybe" }, "unknown_port"},
		{"unknown component", func(g *Graph) { g.Nodes[1].Kind = "python" }, "unknown_component"},
		{"bad condition", func(g *Graph) { g.Nodes[1].Config["expression"] = "eval:__import__('os')" }, "invalid_condition"},
		{"bad regex", func(g *Graph) { g.Nodes[1].Config["expression"] = "matches:(" }, "invalid_condition"},
		{"limit range", func(g *Graph) { g.Nodes[2].Config["limit"] = "99" }, "invalid_setting"},
		{"missing kb", func(g *Graph) { delete(g.Nodes[2].Config, "knowledge_base") }, "missing_setting"},
		{"cycle", func(g *Graph) { g.Edges = append(g.Edges, Edge{From: "model", To: "route"}) }, "cycle"},
		{"two inputs", func(g *Graph) { g.Nodes = append(g.Nodes, Node{ID: "input-2", Kind: "input", Name: "In"}) }, "singleton"},
		{"unconnected output", func(g *Graph) { g.Edges = g.Edges[:4] }, "missing_input"},
		{"dangling edge", func(g *Graph) { g.Edges = append(g.Edges, Edge{From: "ghost", To: "model"}) }, "invalid_edge"},
		{"settings", func(g *Graph) { g.Settings = &Settings{MaxSteps: 500} }, "invalid_setting"},
		{"unauthorized resource", func(g *Graph) { g.Nodes[2].Config["knowledge_base"] = "kb-other" }, "resource_not_authorized"},
	}
	allowed := func(kind, id string) bool { return kind == "knowledge_base" && id == "kb-1" }
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			graph := validGraph()
			tc.mutate(&graph)
			registry.Normalize(&graph)
			issues := registry.Validate(graph, allowed)
			for _, issue := range issues {
				if issue.Code == tc.code && issue.Severity == SeverityError {
					return
				}
			}
			t.Fatalf("expected %s, got %+v", tc.code, issues)
		})
	}
}

func TestIncompatiblePortsAreRejected(t *testing.T) {
	registry, err := ParseRegistry([]byte(`{"registry_version":"test","port_types":{"text":"","documents":"","json":""},
	"conversions":[{"from":"json","to":"text"}],
	"components":[
	{"kind":"input","outputs":[{"name":"out","type":"documents"}],"singleton":true,"label":"Input"},
	{"kind":"parse","inputs":[{"name":"in","accepts":["json"],"required":true}],"outputs":[{"name":"out","type":"json"}]},
	{"kind":"output","inputs":[{"name":"in","accepts":["text"],"required":true}],"singleton":true,"label":"Output"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	graph := Graph{Nodes: []Node{{ID: "input", Kind: "input"}, {ID: "parse", Kind: "parse"}, {ID: "output", Kind: "output"}},
		Edges: []Edge{{From: "input", To: "parse"}, {From: "parse", To: "output"}}}
	registry.Normalize(&graph)
	issues := registry.Validate(graph, nil)
	if len(issues) != 1 || issues[0].Code != "incompatible_ports" || issues[0].NodeID != "parse" {
		t.Fatalf("documents into a json-only input must be rejected once; json→text must convert: %+v", issues)
	}
}

func TestValidGraphWithConversionIsRunnable(t *testing.T) {
	registry := Default()
	graph := validGraph()
	// documents → prompt (accepts documents) → model is the supported path.
	graph.Nodes = append(graph.Nodes, Node{ID: "prompt", Kind: "prompt", Name: "P", Config: map[string]string{"template": "{kb}\n{input}"}})
	graph.Edges[2] = Edge{From: "kb", To: "prompt"}
	graph.Edges = append(graph.Edges, Edge{From: "prompt", To: "model"})
	registry.Normalize(&graph)
	issues := registry.Validate(graph, func(string, string) bool { return true })
	if !Runnable(issues) {
		t.Fatalf("expected runnable, got %+v", issues)
	}
	if !registry.Compatible("json", []string{"text"}) || registry.Compatible("documents", []string{"json"}) {
		t.Fatal("conversion table not honoured")
	}
}

func TestConditionGrammar(t *testing.T) {
	registry := Default()
	for _, ok := range []string{"contains:refund", "not_contains:x", "equals:yes", "matches:^a+$", "empty", "not_empty", "longer_than:10", " contains : spaced"} {
		if _, err := registry.ParseCondition(ok); err != nil && !strings.HasPrefix(ok, " contains ") {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "contains", "contains:", "empty:yes", "longer_than:ten", "longer_than:-1", "matches:" + strings.Repeat("a", 201), "route urgent tickets"} {
		if _, err := registry.ParseCondition(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestErrorPortsAndExtract(t *testing.T) {
	registry := Default()
	graph := Graph{Nodes: []Node{
		{ID: "input", Kind: "input", Name: "In"},
		{ID: "call", Kind: "api", Name: "Call", Config: map[string]string{"connection": "c-1"}},
		{ID: "pick", Kind: "extract", Name: "Pick", Config: map[string]string{"path": "result.items.0.name"}},
		{ID: "fallback", Kind: "prompt", Name: "Fallback", Config: map[string]string{"template": "Sorry: {call}"}},
		{ID: "output", Kind: "output", Name: "Out"},
	}, Edges: []Edge{{From: "input", To: "call"}, {From: "call", To: "pick"}, {From: "call", To: "fallback", FromPort: "error"}, {From: "pick", To: "output"}, {From: "fallback", To: "output"}}}
	registry.Normalize(&graph)
	if issues := registry.Validate(graph, func(string, string) bool { return true }); !Runnable(issues) {
		t.Fatalf("error branch and extract should be runnable: %+v", issues)
	}
	graph.Nodes[2].Config["path"] = "a..b"
	found := false
	for _, issue := range registry.Validate(graph, nil) {
		found = found || (issue.Code == "invalid_setting" && issue.NodeID == "pick")
	}
	if !found {
		t.Fatal("an invalid extract path was accepted")
	}
	graph.Edges[2].FromPort = "error"
	graph.Edges[0] = Edge{From: "input", To: "call", FromPort: "error", ToPort: "in"}
	if Runnable(registry.Validate(graph, nil)) {
		t.Fatal("input has no error port")
	}
	if registry.Version != "1.1.0" || registry.ErrorPort == "" {
		t.Fatalf("registry %s", registry.Version)
	}
}
