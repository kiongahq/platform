package httpapi

import (
	"fmt"
	"sort"

	"github.com/kiongahq/platform/internal/agentgraph"
	"github.com/kiongahq/platform/internal/store"
)

// Map, loop and subflow nodes run published versions (agv-…) as nested
// graphs. The gateway resolves every version a run or a published artifact
// needs, transitively, confines them to the design's project, and re-checks
// their resources against the caller's current access. The runner and the
// deployed adapter never look versions up themselves.
const (
	maxFlowDepth = 3 // the root graph is level 1
	maxFlows     = 10
)

type resolvedFlow struct {
	ContentSHA256 string           `json:"content_sha256"`
	Graph         agentgraph.Graph `json:"graph"`
}

func flowRefs(graph agentgraph.Graph) []string {
	refs := []string{}
	for _, node := range graph.Nodes {
		if (node.Kind == "map" || node.Kind == "loop" || node.Kind == "subflow") && node.Config["subflow"] != "" {
			refs = append(refs, node.Config["subflow"])
		}
	}
	sort.Strings(refs)
	return refs
}

// resolveFlows returns every version the graph reaches, or the issues that
// make it unrunnable (missing, foreign, too deep, too many, or a nested
// graph whose resources the caller can no longer use).
func (s *Server) resolveFlows(projectID string, graph agentgraph.Graph, allowed agentgraph.ResourceChecker) (map[string]resolvedFlow, []agentgraph.Issue) {
	flows := map[string]resolvedFlow{}
	issues := []agentgraph.Issue{}
	fail := func(nodeID, format string, args ...any) {
		issues = append(issues, agentgraph.Issue{Severity: agentgraph.SeverityError, Code: "invalid_flow", NodeID: nodeID, Field: "subflow", Message: fmt.Sprintf(format, args...)})
	}
	type pending struct {
		id, via string
		depth   int
	}
	queue := []pending{}
	for _, node := range graph.Nodes {
		if ref := node.Config["subflow"]; ref != "" && (node.Kind == "map" || node.Kind == "loop" || node.Kind == "subflow") {
			queue = append(queue, pending{id: ref, via: node.ID, depth: 2})
		}
	}
	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		if _, done := flows[item.id]; done {
			continue
		}
		if item.depth > maxFlowDepth {
			fail(item.via, "Flows can nest at most %d levels; %s goes deeper.", maxFlowDepth, item.id)
			continue
		}
		if len(flows) >= maxFlows {
			fail(item.via, "A run can use at most %d published versions.", maxFlows)
			break
		}
		version, err := store.GetDoc[agentVersion](s.store, agentVersionKind, item.id)
		if err != nil || version.ProjectID != projectID {
			fail(item.via, "Published version %s is not available in this project.", item.id)
			continue
		}
		nested := version.Graph
		agentgraph.Default().Normalize(&nested)
		for _, issue := range agentgraph.Default().Validate(nested, allowed) {
			if issue.Severity == agentgraph.SeverityError && issue.Code != "invalid_flow" {
				fail(item.via, "Version %s can no longer run: %s", item.id, issue.Message)
				break
			}
		}
		flows[item.id] = resolvedFlow{ContentSHA256: version.ContentSHA256, Graph: nested}
		for _, ref := range flowRefs(nested) {
			queue = append(queue, pending{id: ref, via: item.via, depth: item.depth + 1})
		}
	}
	return flows, issues
}

func flowGraphs(flows map[string]resolvedFlow) map[string]agentgraph.Graph {
	graphs := make(map[string]agentgraph.Graph, len(flows))
	for id, flow := range flows {
		graphs[id] = flow.Graph
	}
	return graphs
}
