package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/store"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const langflowSnapshotKind = "langflow_snapshot"
const maxLangflowFlowBytes = 1 << 20

var langflowFlowID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)
var langflowSnapshotID = regexp.MustCompile(`^lfs-[a-f0-9]{24}$`)

type flowRevision struct {
	Revision int             `json:"revision"`
	Flow     json.RawMessage `json:"flow"`
	SavedAt  time.Time       `json:"saved_at"`
}
type langflowSnapshotRecord struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"project_id"`
	OwnerSubject string          `json:"owner_subject"`
	WorkspaceID  string          `json:"workspace_id"`
	FlowID       string          `json:"flow_id"`
	Name         string          `json:"name"`
	Revision     int             `json:"revision"`
	Flow         json.RawMessage `json:"flow"`
	History      []flowRevision  `json:"history,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}
type flowSummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	VersionToken string `json:"version_token,omitempty"`
}

func (s *Server) langflowSnapshotSource(r *http.Request) (*workspaceDestination, string, error) {
	id := r.PathValue("id")
	if id == "local" && langflowLocalMode() {
		target, err := workspaceTarget("langflow", r)
		return target, "", err
	}
	_, item, err := s.ownedLangflowWorkspace(r)
	if err != nil {
		return nil, "", err
	}
	disabled, _, _ := unstructured.NestedBool(item.Object, "spec", "disabled")
	if disabled {
		return nil, "", fmt.Errorf("workspace is stopped")
	}
	project, _, _ := unstructured.NestedString(item.Object, "spec", "projectID")
	client, namespace, err := langflowClient()
	if err != nil {
		return nil, "", err
	}
	target, err := discoverWorkspaceNamed(r.Context(), client, namespace, principal(r).Subject, "langflow", item.GetName())
	return target, project, err
}
func langflowLocalMode() bool {
	return strings.TrimSpace(os.Getenv("KIONGA_WORKSPACE_NAMESPACE")) == ""
}
func readLangflowFlow(r *http.Request, target *workspaceDestination, path string) (json.RawMessage, error) {
	ctx := r.Context()
	upstream := strings.TrimRight(target.String(), "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Langflow is unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("Langflow returned %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxLangflowFlowBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxLangflowFlowBytes {
		return nil, fmt.Errorf("flow exceeds 1 MiB export limit")
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("Langflow returned invalid JSON")
	}
	return raw, nil
}
func (s *Server) langflowFlows(w http.ResponseWriter, r *http.Request) {
	target, _, err := s.langflowSnapshotSource(r)
	if err != nil {
		writeError(w, 503, "workspace_unavailable", err.Error())
		return
	}
	raw, err := readLangflowFlow(r, target, "/api/v1/flows/?size=100")
	if err != nil {
		writeError(w, 502, "langflow_unavailable", err.Error())
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		writeError(w, 502, "langflow_unavailable", "Could not decode flow list")
		return
	}
	list := []any{}
	switch value := payload.(type) {
	case []any:
		list = value
	case map[string]any:
		if items, ok := value["items"].([]any); ok {
			list = items
		}
		if items, ok := value["flows"].([]any); ok {
			list = items
		}
	}
	result := []flowSummary{}
	for _, rawItem := range list {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		id, _ := item["id"].(string)
		name, _ := item["name"].(string)
		if !langflowFlowID.MatchString(id) {
			continue
		}
		description, _ := item["description"].(string)
		token, _ := item["version_token"].(string)
		result = append(result, flowSummary{ID: id, Name: name, Description: description, VersionToken: token})
	}
	writeJSON(w, 200, map[string]any{"items": result})
}
func validateLangflowExport(raw json.RawMessage) (string, error) {
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil {
		return "", err
	}
	name, _ := item["name"].(string)
	data, ok := item["data"].(map[string]any)
	if !ok {
		return "", errors.New("flow has no graph data")
	}
	nodes, ok := data["nodes"].([]any)
	if !ok || len(nodes) == 0 || len(nodes) > 200 {
		return "", errors.New("flow must have 1–200 nodes")
	}
	edges, ok := data["edges"].([]any)
	if !ok || len(edges) > 500 {
		return "", errors.New("flow has too many edges")
	}
	if hasEmbeddedSecret(item) {
		return "", errors.New("flow export contains an embedded credential; use managed connections or variables")
	}
	if len(name) > 100 {
		return "", errors.New("flow name is too long")
	}
	return name, nil
}
func hasEmbeddedSecret(value any) bool {
	switch data := value.(type) {
	case map[string]any:
		for key, item := range data {
			normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
			if strings.Contains(normalized, "password") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "api_key") || strings.Contains(normalized, "access_token") {
				if embeddedSecretValue(item) {
					return true
				}
			}
			if hasEmbeddedSecret(item) {
				return true
			}
		}
	case []any:
		for _, item := range data {
			if hasEmbeddedSecret(item) {
				return true
			}
		}
	}
	return false
}

func embeddedSecretValue(value any) bool {
	switch item := value.(type) {
	case string:
		return item != "" && item != "********" && !strings.HasPrefix(item, "${")
	case map[string]any:
		if nested, exists := item["value"]; exists {
			return embeddedSecretValue(nested)
		}
	}
	return false
}
func snapshotSummary(item langflowSnapshotRecord) map[string]any {
	revisions := []map[string]any{}
	for _, old := range item.History {
		revisions = append(revisions, map[string]any{"revision": old.Revision, "saved_at": old.SavedAt})
	}
	return map[string]any{"id": item.ID, "project_id": item.ProjectID, "workspace_id": item.WorkspaceID, "flow_id": item.FlowID, "owner_subject": item.OwnerSubject, "name": item.Name, "revision": item.Revision, "created_at": item.CreatedAt, "updated_at": item.UpdatedAt, "history": revisions}
}
func (s *Server) langflowSnapshots(w http.ResponseWriter, r *http.Request) {
	items := []map[string]any{}
	for _, item := range store.ListDocs[langflowSnapshotRecord](s.store, langflowSnapshotKind) {
		if projectAllowed(s.store, principal(r), item.ProjectID) {
			items = append(items, snapshotSummary(item))
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) langflowSnapshot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !langflowSnapshotID.MatchString(id) {
		writeError(w, 404, "not_found", "snapshot not found")
		return
	}
	item, err := store.GetDoc[langflowSnapshotRecord](s.store, langflowSnapshotKind, id)
	if err != nil || !projectAllowed(s.store, principal(r), item.ProjectID) {
		writeError(w, 404, "not_found", "snapshot not found")
		return
	}
	writeJSON(w, 200, snapshotSummary(item))
}
func (s *Server) captureLangflowSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FlowID     string `json:"flow_id"`
		ProjectID  string `json:"project_id"`
		SnapshotID string `json:"snapshot_id"`
		Revision   int    `json:"revision"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := decode(r, &req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	if !langflowFlowID.MatchString(req.FlowID) {
		writeError(w, 422, "validation_error", "Invalid flow ID")
		return
	}
	target, project, err := s.langflowSnapshotSource(r)
	if err != nil {
		writeError(w, 503, "workspace_unavailable", err.Error())
		return
	}
	if project == "" {
		project = req.ProjectID
	}
	if project == "" || !projectAllowed(s.store, principal(r), project) {
		writeError(w, 403, "access_denied", "Project is not assigned to this user")
		return
	}
	if _, err := s.store.Project(project); err != nil {
		writeError(w, 404, "not_found", "Project not found")
		return
	}
	raw, err := readLangflowFlow(r, target, "/api/v1/flows/"+req.FlowID)
	if err != nil {
		writeError(w, 502, "langflow_unavailable", err.Error())
		return
	}
	name, err := validateLangflowExport(raw)
	if err != nil {
		writeError(w, 422, "validation_error", err.Error())
		return
	}
	if req.SnapshotID != "" && !langflowSnapshotID.MatchString(req.SnapshotID) {
		writeError(w, 422, "validation_error", "Invalid snapshot ID")
		return
	}
	id := req.SnapshotID
	if id == "" {
		random := make([]byte, 12)
		if _, err := rand.Read(random); err != nil {
			writeError(w, 500, "internal_error", "Could not create snapshot ID")
			return
		}
		id = "lfs-" + hex.EncodeToString(random)
	}
	now := time.Now().UTC()
	item, err := store.UpdateDoc(s.store, langflowSnapshotKind, id, func(current langflowSnapshotRecord, exists bool) (langflowSnapshotRecord, error) {
		if !exists {
			if req.SnapshotID != "" {
				return current, store.ErrNotFound
			}
			return langflowSnapshotRecord{ID: id, ProjectID: project, WorkspaceID: r.PathValue("id"), OwnerSubject: principal(r).Subject, FlowID: req.FlowID, Name: name, Revision: 1, Flow: raw, CreatedAt: now, UpdatedAt: now}, nil
		}
		if current.ProjectID != project || current.FlowID != req.FlowID || current.WorkspaceID != r.PathValue("id") || current.OwnerSubject != principal(r).Subject || !projectAllowed(s.store, principal(r), current.ProjectID) {
			return current, store.ErrNotFound
		}
		if current.Revision != req.Revision {
			return current, store.ErrConflict
		}
		current.History = append(current.History, flowRevision{Revision: current.Revision, Flow: current.Flow, SavedAt: current.UpdatedAt})
		if len(current.History) > 20 {
			current.History = current.History[len(current.History)-20:]
		}
		current.Flow = raw
		current.Name = name
		current.Revision++
		current.UpdatedAt = now
		return current, nil
	}, "langflow.snapshot_saved", actor(r))
	if errors.Is(err, store.ErrConflict) {
		writeError(w, 409, "revision_conflict", "Snapshot changed elsewhere; reload before saving")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "not_found", "Snapshot not found")
		return
	}
	if err != nil {
		writeError(w, 500, "save_failed", "Could not save flow snapshot")
		return
	}
	status := 200
	if req.SnapshotID == "" {
		status = 201
	}
	writeJSON(w, status, snapshotSummary(item))
}
