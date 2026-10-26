package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/agentgraph"
	"github.com/kiongahq/platform/internal/store"
)

const agentDesignKind = "agent_design"

var designIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Graph nodes and edges follow the versioned contract in kiongahq/contracts
// (agent-graph). Stored version 1 drafts are upgraded in memory on read and
// never rewritten, so revision history keeps exactly what was saved.
type designNode = agentgraph.Node
type designEdge = agentgraph.Edge
type designRevision struct {
	Revision      int                  `json:"revision"`
	SchemaVersion int                  `json:"schema_version,omitempty"`
	Name          string               `json:"name"`
	Description   string               `json:"description,omitempty"`
	Nodes         []designNode         `json:"nodes"`
	Edges         []designEdge         `json:"edges"`
	Settings      *agentgraph.Settings `json:"settings,omitempty"`
	SavedAt       time.Time            `json:"saved_at"`
}
type agentDesign struct {
	ID            string               `json:"id"`
	ProjectID     string               `json:"project_id"`
	OwnerSubject  string               `json:"owner_subject"`
	SchemaVersion int                  `json:"schema_version,omitempty"`
	Name          string               `json:"name"`
	Description   string               `json:"description,omitempty"`
	Revision      int                  `json:"revision"`
	Nodes         []designNode         `json:"nodes"`
	Edges         []designEdge         `json:"edges"`
	Settings      *agentgraph.Settings `json:"settings,omitempty"`
	History       []designRevision     `json:"history,omitempty"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
}
type saveDesignRequest struct {
	ProjectID   string               `json:"project_id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Revision    int                  `json:"revision"`
	Nodes       []designNode         `json:"nodes"`
	Edges       []designEdge         `json:"edges"`
	Settings    *agentgraph.Settings `json:"settings,omitempty"`
}

func (d agentDesign) graph() agentgraph.Graph {
	return agentgraph.Graph{SchemaVersion: d.SchemaVersion, Name: d.Name, Description: d.Description, Nodes: d.Nodes, Edges: d.Edges, Settings: d.Settings}
}

// upgraded returns the design with its graph and every revision normalized
// to the current schema for API responses. Storage is not modified.
func (d agentDesign) upgraded() agentDesign {
	registry := agentgraph.Default()
	graph := d.graph()
	graph.Nodes = cloneNodes(graph.Nodes)
	graph.Edges = append([]designEdge(nil), graph.Edges...)
	registry.Normalize(&graph)
	d.SchemaVersion, d.Nodes, d.Edges, d.Settings = graph.SchemaVersion, graph.Nodes, graph.Edges, graph.Settings
	history := make([]designRevision, len(d.History))
	for index, revision := range d.History {
		old := agentgraph.Graph{Nodes: cloneNodes(revision.Nodes), Edges: append([]designEdge(nil), revision.Edges...), Settings: revision.Settings}
		registry.Normalize(&old)
		revision.SchemaVersion, revision.Nodes, revision.Edges, revision.Settings = old.SchemaVersion, old.Nodes, old.Edges, old.Settings
		history[index] = revision
	}
	d.History = history
	return d
}

func cloneNodes(nodes []designNode) []designNode {
	out := make([]designNode, len(nodes))
	for index, node := range nodes {
		if node.Config != nil {
			config := make(map[string]string, len(node.Config))
			for key, value := range node.Config {
				config[key] = value
			}
			node.Config = config
		}
		out[index] = node
	}
	return out
}

// normalizedRequest upgrades a save request to the current schema so new
// revisions are always stored as version 2.
func normalizedRequest(req saveDesignRequest) (saveDesignRequest, agentgraph.Graph) {
	graph := agentgraph.Graph{Name: req.Name, Description: req.Description, Nodes: cloneNodes(req.Nodes), Edges: append([]designEdge(nil), req.Edges...), Settings: req.Settings}
	agentgraph.Default().Normalize(&graph)
	req.Nodes, req.Edges, req.Settings = graph.Nodes, graph.Edges, graph.Settings
	return req, graph
}

func validateAgentDesign(req saveDesignRequest) []string {
	issues := []string{}
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 100 {
		issues = append(issues, "Name must contain 1–100 characters.")
	}
	if len(req.Description) > 1000 {
		issues = append(issues, "Description must be at most 1000 characters.")
	}
	if len(req.Nodes) < 2 || len(req.Nodes) > 100 {
		issues = append(issues, "A design needs 2–100 nodes.")
	}
	if len(req.Edges) > 300 {
		issues = append(issues, "A design can have at most 300 connections.")
	}
	nodes := map[string]designNode{}
	inputCount, outputCount := 0, 0
	for _, node := range req.Nodes {
		if !designIDPattern.MatchString(node.ID) || nodes[node.ID].ID != "" {
			issues = append(issues, "Every node needs a unique lowercase ID.")
		}
		if agentgraph.Default().Component(node.Kind) == nil {
			issues = append(issues, fmt.Sprintf("Node %q has an unsupported type.", node.ID))
		}
		if strings.TrimSpace(node.Name) == "" || len(node.Name) > 100 {
			issues = append(issues, fmt.Sprintf("Node %q needs a name of at most 100 characters.", node.ID))
		}
		if node.X < 0 || node.X > 10000 || node.Y < 0 || node.Y > 10000 {
			issues = append(issues, fmt.Sprintf("Node %q is outside the canvas.", node.ID))
		}
		if len(node.Group) > 60 {
			issues = append(issues, fmt.Sprintf("Node %q has a group name over 60 characters.", node.ID))
		}
		if len(node.Config) > 30 {
			issues = append(issues, fmt.Sprintf("Node %q has too many settings.", node.ID))
		}
		for key, value := range node.Config {
			if len(key) > 80 || len(value) > 16000 {
				issues = append(issues, fmt.Sprintf("Node %q has an oversized setting.", node.ID))
				break
			}
		}
		if node.Kind == "input" {
			inputCount++
		}
		if node.Kind == "output" {
			outputCount++
		}
		nodes[node.ID] = node
	}
	if inputCount != 1 || outputCount != 1 {
		issues = append(issues, "A design needs exactly one input and one output node.")
	}
	children := map[string][]string{}
	indegree := map[string]int{}
	seen := map[string]bool{}
	for id := range nodes {
		indegree[id] = 0
	}
	for _, edge := range req.Edges {
		if nodes[edge.From].ID == "" || nodes[edge.To].ID == "" || edge.From == edge.To {
			issues = append(issues, "Connections must link two different existing nodes.")
			continue
		}
		key := edge.From + ">" + edge.To
		if edge.FromPort != "" || edge.ToPort != "" {
			key = edge.From + "." + edge.FromPort + ">" + edge.To + "." + edge.ToPort
		}
		if seen[key] {
			issues = append(issues, "Duplicate connection: "+key)
			continue
		}
		seen[key] = true
		if nodes[edge.From].Kind == "output" || nodes[edge.To].Kind == "input" {
			issues = append(issues, "Connections cannot leave output or enter input.")
		}
		if issue := designPortIssue(nodes[edge.From], nodes[edge.To], edge); issue != "" {
			issues = append(issues, issue)
		}
		children[edge.From] = append(children[edge.From], edge.To)
		indegree[edge.To]++
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
		for _, to := range children[id] {
			indegree[to]--
			if indegree[to] == 0 {
				queue = append(queue, to)
			}
		}
	}
	if visited != len(nodes) {
		issues = append(issues, "Connections contain a cycle.")
	}
	if settings := req.Settings; settings != nil && (settings.MaxSteps < 0 || settings.MaxSteps > 200 || settings.TimeoutSeconds < 0 || settings.TimeoutSeconds > 300 || settings.MaxTokens < 0 || settings.MaxTokens > 1000000) {
		issues = append(issues, "Run limits must be 1–200 steps, 1–300 seconds and 1–1,000,000 tokens.")
	}
	return issues
}

// designPortIssue rejects explicit ports the components do not have. Type
// compatibility and required settings are reported by validate, because a
// draft may be saved while it is still incomplete.
func designPortIssue(from, to designNode, edge designEdge) string {
	registry := agentgraph.Default()
	source, target := registry.Component(from.Kind), registry.Component(to.Kind)
	if source == nil || target == nil {
		return ""
	}
	if edge.FromPort != "" {
		found := false
		for _, port := range source.Outputs {
			found = found || port.Name == edge.FromPort
		}
		if !found {
			return fmt.Sprintf("Node %q has no output named %q.", from.ID, edge.FromPort)
		}
	}
	if edge.ToPort != "" {
		found := false
		for _, port := range target.Inputs {
			found = found || port.Name == edge.ToPort
		}
		if !found {
			return fmt.Sprintf("Node %q has no input named %q.", to.ID, edge.ToPort)
		}
	}
	return ""
}
func (s *Server) agentDesigns(w http.ResponseWriter, r *http.Request) {
	items := []agentDesign{}
	for _, item := range store.ListDocs[agentDesign](s.store, agentDesignKind) {
		if projectAllowed(s.store, principal(r), item.ProjectID) {
			items = append(items, item.upgraded())
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (s *Server) agentDesign(w http.ResponseWriter, r *http.Request) {
	item, err := store.GetDoc[agentDesign](s.store, agentDesignKind, r.PathValue("id"))
	if err != nil || !projectAllowed(s.store, principal(r), item.ProjectID) {
		writeError(w, http.StatusNotFound, "not_found", "design not found")
		return
	}
	writeJSON(w, http.StatusOK, item.upgraded())
}
func (s *Server) createAgentDesign(w http.ResponseWriter, r *http.Request) {
	var req saveDesignRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !projectAllowed(s.store, principal(r), req.ProjectID) || req.ProjectID == "" {
		writeError(w, http.StatusForbidden, "access_denied", "project is not assigned to this user")
		return
	}
	if _, err := s.store.Project(req.ProjectID); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	if issues := validateAgentDesign(req); len(issues) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "validation_error", "issues": issues})
		return
	}
	req, _ = normalizedRequest(req)
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create design ID")
		return
	}
	now := time.Now().UTC()
	item := agentDesign{ID: "agd-" + hex.EncodeToString(raw), ProjectID: req.ProjectID, OwnerSubject: principal(r).Subject, Name: strings.TrimSpace(req.Name), Description: strings.TrimSpace(req.Description), Revision: 1, SchemaVersion: agentgraph.SchemaVersion, Nodes: req.Nodes, Edges: req.Edges, Settings: req.Settings, CreatedAt: now, UpdatedAt: now}
	saved, err := store.UpdateDoc(s.store, agentDesignKind, item.ID, func(_ agentDesign, exists bool) (agentDesign, error) {
		if exists {
			return agentDesign{}, store.ErrConflict
		}
		return item, nil
	}, "agent_design.created", actor(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, saved.upgraded())
}
func (s *Server) saveAgentDesign(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := store.GetDoc[agentDesign](s.store, agentDesignKind, id)
	if err != nil || !projectAllowed(s.store, principal(r), existing.ProjectID) {
		writeError(w, http.StatusNotFound, "not_found", "design not found")
		return
	}
	var req saveDesignRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.ProjectID != existing.ProjectID {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "project cannot be changed")
		return
	}
	if issues := validateAgentDesign(req); len(issues) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "validation_error", "issues": issues})
		return
	}
	req, _ = normalizedRequest(req)
	saved, err := store.UpdateDoc(s.store, agentDesignKind, id, func(current agentDesign, exists bool) (agentDesign, error) {
		if !exists {
			return agentDesign{}, store.ErrNotFound
		}
		if current.Revision != req.Revision {
			return agentDesign{}, store.ErrConflict
		}
		current.History = append(current.History, designRevision{Revision: current.Revision, SchemaVersion: current.SchemaVersion, Name: current.Name, Description: current.Description, Nodes: current.Nodes, Edges: current.Edges, Settings: current.Settings, SavedAt: current.UpdatedAt})
		if len(current.History) > 20 {
			current.History = current.History[len(current.History)-20:]
		}
		current.Name = strings.TrimSpace(req.Name)
		current.Description = strings.TrimSpace(req.Description)
		current.SchemaVersion = agentgraph.SchemaVersion
		current.Nodes = req.Nodes
		current.Edges = req.Edges
		current.Settings = req.Settings
		current.Revision++
		current.UpdatedAt = time.Now().UTC()
		return current, nil
	}, "agent_design.updated", actor(r))
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "revision_conflict", "design changed elsewhere; reload before saving")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved.upgraded())
}
