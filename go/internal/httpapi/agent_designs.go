package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/kiongahq/platform/internal/store"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const agentDesignKind = "agent_design"

var designIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var designNodeKinds = map[string]bool{"input": true, "prompt": true, "model": true, "tool": true, "knowledge": true, "api": true, "condition": true, "memory": true, "output": true}

type designNode struct {
	ID     string            `json:"id"`
	Kind   string            `json:"kind"`
	Name   string            `json:"name"`
	X      float64           `json:"x"`
	Y      float64           `json:"y"`
	Config map[string]string `json:"config,omitempty"`
}
type designEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}
type designRevision struct {
	Revision    int          `json:"revision"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Nodes       []designNode `json:"nodes"`
	Edges       []designEdge `json:"edges"`
	SavedAt     time.Time    `json:"saved_at"`
}
type agentDesign struct {
	ID           string           `json:"id"`
	ProjectID    string           `json:"project_id"`
	OwnerSubject string           `json:"owner_subject"`
	Name         string           `json:"name"`
	Description  string           `json:"description,omitempty"`
	Revision     int              `json:"revision"`
	Nodes        []designNode     `json:"nodes"`
	Edges        []designEdge     `json:"edges"`
	History      []designRevision `json:"history,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
}
type saveDesignRequest struct {
	ProjectID   string       `json:"project_id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Revision    int          `json:"revision"`
	Nodes       []designNode `json:"nodes"`
	Edges       []designEdge `json:"edges"`
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
		if !designNodeKinds[node.Kind] {
			issues = append(issues, fmt.Sprintf("Node %q has an unsupported type.", node.ID))
		}
		if strings.TrimSpace(node.Name) == "" || len(node.Name) > 100 {
			issues = append(issues, fmt.Sprintf("Node %q needs a name of at most 100 characters.", node.ID))
		}
		if node.X < 0 || node.X > 10000 || node.Y < 0 || node.Y > 10000 {
			issues = append(issues, fmt.Sprintf("Node %q is outside the canvas.", node.ID))
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
		if seen[key] {
			issues = append(issues, "Duplicate connection: "+key)
			continue
		}
		seen[key] = true
		if nodes[edge.From].Kind == "output" || nodes[edge.To].Kind == "input" {
			issues = append(issues, "Connections cannot leave output or enter input.")
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
	return issues
}
func (s *Server) agentDesigns(w http.ResponseWriter, r *http.Request) {
	items := []agentDesign{}
	for _, item := range store.ListDocs[agentDesign](s.store, agentDesignKind) {
		if projectAllowed(s.store, principal(r), item.ProjectID) {
			items = append(items, item)
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
	writeJSON(w, http.StatusOK, item)
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
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create design ID")
		return
	}
	now := time.Now().UTC()
	item := agentDesign{ID: "agd-" + hex.EncodeToString(raw), ProjectID: req.ProjectID, OwnerSubject: principal(r).Subject, Name: strings.TrimSpace(req.Name), Description: strings.TrimSpace(req.Description), Revision: 1, Nodes: req.Nodes, Edges: req.Edges, CreatedAt: now, UpdatedAt: now}
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
	writeJSON(w, http.StatusCreated, saved)
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
	saved, err := store.UpdateDoc(s.store, agentDesignKind, id, func(current agentDesign, exists bool) (agentDesign, error) {
		if !exists {
			return agentDesign{}, store.ErrNotFound
		}
		if current.Revision != req.Revision {
			return agentDesign{}, store.ErrConflict
		}
		current.History = append(current.History, designRevision{Revision: current.Revision, Name: current.Name, Description: current.Description, Nodes: current.Nodes, Edges: current.Edges, SavedAt: current.UpdatedAt})
		if len(current.History) > 20 {
			current.History = current.History[len(current.History)-20:]
		}
		current.Name = strings.TrimSpace(req.Name)
		current.Description = strings.TrimSpace(req.Description)
		current.Nodes = req.Nodes
		current.Edges = req.Edges
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
	writeJSON(w, http.StatusOK, saved)
}
