package agentgraph

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Issue severities. Errors block running and publishing; warnings do not.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

var nodeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Issue is one validation finding, addressed to a node or field when it has one.
type Issue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	NodeID   string `json:"node_id,omitempty"`
	Field    string `json:"field,omitempty"`
	Message  string `json:"message"`
}

// ResourceChecker reports whether the caller may use a resource. kind is a
// registry resource name: model, tool, knowledge_base, dataset or connection.
type ResourceChecker func(kind, id string) bool

// Runnable reports whether issues contain no errors.
func Runnable(issues []Issue) bool {
	for _, issue := range issues {
		if issue.Severity == SeverityError {
			return false
		}
	}
	return true
}

// Validate checks a normalized graph against the registry: kinds, settings,
// typed edges, required inputs and configuration, condition grammar, cycles
// and, when allowed is not nil, resource authorization.
func (r *Registry) Validate(graph Graph, allowed ResourceChecker) []Issue {
	issues := []Issue{}
	add := func(severity, code, nodeID, field, format string, args ...any) {
		issues = append(issues, Issue{Severity: severity, Code: code, NodeID: nodeID, Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if graph.Settings != nil {
		s := graph.Settings
		if s.MaxSteps < 0 || s.MaxSteps > 200 {
			add(SeverityError, "invalid_setting", "", "max_steps", "Maximum steps must be 1–200.")
		}
		if s.TimeoutSeconds < 0 || s.TimeoutSeconds > 300 {
			add(SeverityError, "invalid_setting", "", "timeout_seconds", "Run timeout must be 1–300 seconds.")
		}
		if s.MaxTokens < 0 || s.MaxTokens > 1000000 {
			add(SeverityError, "invalid_setting", "", "max_tokens", "Token budget must be 1–1,000,000.")
		}
	}
	nodes := map[string]Node{}
	components := map[string]*Component{}
	singletons := map[string]int{}
	for _, node := range graph.Nodes {
		if !nodeIDPattern.MatchString(node.ID) {
			add(SeverityError, "invalid_node_id", node.ID, "", "Node ID %q must be lowercase letters, digits and hyphens.", node.ID)
			continue
		}
		if _, duplicate := nodes[node.ID]; duplicate {
			add(SeverityError, "duplicate_node_id", node.ID, "", "Node ID %q is used twice.", node.ID)
			continue
		}
		nodes[node.ID] = node
		component := r.Component(node.Kind)
		if component == nil {
			add(SeverityError, "unknown_component", node.ID, "", "Node %q uses an unsupported component %q.", node.ID, node.Kind)
			continue
		}
		components[node.ID] = component
		if component.Singleton {
			singletons[node.Kind]++
		}
		r.validateConfig(node, component, allowed, add)
	}
	for _, kind := range []string{"input", "output"} {
		if singletons[kind] != 1 {
			add(SeverityError, "singleton", "", "", "A graph needs exactly one %s node.", r.Component(kind).Label)
		}
	}
	seen := map[string]bool{}
	inbound := map[string]map[string]int{}
	children := map[string][]string{}
	indegree := map[string]int{}
	for id := range components {
		indegree[id] = 0
	}
	for _, edge := range graph.Edges {
		_, fromExists := nodes[edge.From]
		_, toExists := nodes[edge.To]
		if !fromExists || !toExists || edge.From == edge.To {
			add(SeverityError, "invalid_edge", "", "", "Connection %s → %s must join two different existing nodes.", edge.From, edge.To)
			continue
		}
		source, target := components[edge.From], components[edge.To]
		if source == nil || target == nil {
			continue // unknown_component is already reported
		}
		key := edge.From + "." + edge.FromPort + ">" + edge.To + "." + edge.ToPort
		if seen[key] {
			add(SeverityError, "duplicate_edge", edge.From, "", "Connection %s → %s is duplicated.", edge.From, edge.To)
			continue
		}
		seen[key] = true
		output, input := source.output(edge.FromPort), target.input(edge.ToPort)
		if output == nil {
			add(SeverityError, "unknown_port", edge.From, "", "%s has no output named %q.", edge.From, edge.FromPort)
			continue
		}
		if input == nil {
			add(SeverityError, "unknown_port", edge.To, "", "%s has no input named %q.", edge.To, edge.ToPort)
			continue
		}
		if !r.Compatible(output.Type, input.Accepts) {
			add(SeverityError, "incompatible_ports", edge.To, "", "%s produces %s, but %s accepts %s.", edge.From, output.Type, edge.To, strings.Join(input.Accepts, ", "))
		}
		if inbound[edge.To] == nil {
			inbound[edge.To] = map[string]int{}
		}
		inbound[edge.To][edge.ToPort]++
		children[edge.From] = append(children[edge.From], edge.To)
		indegree[edge.To]++
	}
	for id, component := range components {
		for _, port := range component.Inputs {
			if port.Required && inbound[id][port.Name] == 0 {
				add(SeverityError, "missing_input", id, "", "Connect something to %s's %s input.", id, port.Name)
			}
		}
	}
	queue := []string{}
	for id, count := range indegree {
		if count == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, child := range children[id] {
			indegree[child]--
			if indegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	if visited != len(components) {
		add(SeverityError, "cycle", "", "", "Connections contain a cycle. Loops are not supported yet.")
	}
	for id, component := range components {
		if component.Kind != "output" && len(children[id]) == 0 {
			add(SeverityWarning, "dead_end", id, "", "%s's result is not used by any node.", id)
		}
	}
	return issues
}

func (r *Registry) validateConfig(node Node, component *Component, allowed ResourceChecker, add func(severity, code, nodeID, field, format string, args ...any)) {
	for key := range node.Config {
		if component.field(key) == nil {
			add(SeverityWarning, "unknown_setting", node.ID, key, "%s ignores the setting %q.", node.ID, key)
		}
	}
	for _, field := range component.Config {
		value := strings.TrimSpace(node.Config[field.Key])
		if value == "" {
			if field.Required {
				add(SeverityError, "missing_setting", node.ID, field.Key, "%s needs %s.", node.ID, strings.ToLower(field.Label))
			}
			continue
		}
		if field.MaxLength > 0 && len(node.Config[field.Key]) > field.MaxLength {
			add(SeverityError, "invalid_setting", node.ID, field.Key, "%s's %s can have at most %d characters.", node.ID, strings.ToLower(field.Label), field.MaxLength)
		}
		switch field.Type {
		case "integer":
			number, err := strconv.Atoi(value)
			if err != nil || (field.Minimum != nil && number < *field.Minimum) || (field.Maximum != nil && number > *field.Maximum) {
				add(SeverityError, "invalid_setting", node.ID, field.Key, "%s's %s must be a whole number from %d to %d.", node.ID, strings.ToLower(field.Label), deref(field.Minimum), deref(field.Maximum))
			}
		case "enum":
			found := false
			for _, option := range field.Options {
				found = found || option == value
			}
			if !found {
				add(SeverityWarning, "legacy_setting", node.ID, field.Key, "%s's %s %q is not one of %s; it runs as %s.", node.ID, strings.ToLower(field.Label), value, strings.Join(field.Options, ", "), defaultOption(field))
			}
		case "expression":
			if _, err := r.ParseCondition(value); err != nil {
				add(SeverityError, "invalid_condition", node.ID, field.Key, "%s: %v.", node.ID, err)
			}
		case "resource":
			if field.Resource == "model" && value == "llm" {
				continue
			}
			if allowed != nil && !allowed(field.Resource, value) {
				add(SeverityError, "resource_not_authorized", node.ID, field.Key, "%s refers to %s %q, which is not available to this project.", node.ID, strings.ReplaceAll(field.Resource, "_", " "), value)
			}
		}
	}
}

func deref(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func defaultOption(field Field) string {
	if field.Default != "" {
		return field.Default
	}
	if len(field.Options) > 0 {
		return field.Options[0]
	}
	return ""
}
