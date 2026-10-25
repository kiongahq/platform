package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kiongahq/platform/internal/store"
)

// Knowledge backends run behind the gateway so flow JSON contains a Kionga
// knowledge-base ID, never a database URL or API key.
const knowledgeKind = "knowledge_base"

var knowledgeRef = regexp.MustCompile(`^env:[A-Z][A-Z0-9_]*$`)
var knowledgeHTTP = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type knowledgeBase struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	Name       string    `json:"name"`
	Backend    string    `json:"backend"`
	Endpoint   string    `json:"endpoint,omitempty"`
	SecretRef  string    `json:"secret_ref,omitempty"`
	DSNRef     string    `json:"dsn_ref,omitempty"`
	Dimensions int       `json:"dimensions"`
	CreatedAt  time.Time `json:"created_at"`
}
type knowledgeSummary struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	Name       string `json:"name"`
	Backend    string `json:"backend"`
	Dimensions int    `json:"dimensions"`
}

func (base knowledgeBase) summary() knowledgeSummary {
	return knowledgeSummary{ID: base.ID, ProjectID: base.ProjectID, Name: base.Name, Backend: base.Backend, Dimensions: base.Dimensions}
}

type knowledgePoint struct {
	Text     string         `json:"text"`
	Vector   []float64      `json:"vector"`
	Metadata map[string]any `json:"metadata,omitempty"`
}
type knowledgeQuery struct {
	Vector []float64 `json:"vector"`
	Limit  int       `json:"limit"`
}
type knowledgeMatch struct {
	ID       string         `json:"id"`
	Text     string         `json:"text"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Score    float64        `json:"score"`
}

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/admin/knowledge-bases", s.createKnowledgeBase)
		mux.HandleFunc("GET /api/v1/knowledge-bases", s.knowledgeBases)
		mux.HandleFunc("POST /api/v1/knowledge-bases/{id}/chunks", s.addKnowledgeChunk)
		mux.HandleFunc("POST /api/v1/knowledge-bases/{id}/search", s.searchKnowledge)
	})
}
func validKnowledgeVector(vector []float64, dimensions int) bool {
	if len(vector) != dimensions {
		return false
	}
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}
func knowledgeVectorLiteral(vector []float64) string {
	parts := make([]string, len(vector))
	for i, value := range vector {
		parts[i] = fmt.Sprintf("%g", value)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
func knowledgeID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw)
}
func knowledgePointID() string {
	id := knowledgeID()
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
}
func (s *Server) knowledgeBases(w http.ResponseWriter, r *http.Request) {
	items := []knowledgeSummary{}
	for _, base := range store.ListDocs[knowledgeBase](s.store, knowledgeKind) {
		if projectAllowed(s.store, principal(r), base.ProjectID) {
			items = append(items, base.summary())
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}
func (s *Server) authorizedKnowledge(w http.ResponseWriter, r *http.Request) (knowledgeBase, bool) {
	base, err := store.GetDoc[knowledgeBase](s.store, knowledgeKind, r.PathValue("id"))
	if err != nil || !projectAllowed(s.store, principal(r), base.ProjectID) {
		writeError(w, http.StatusNotFound, "not_found", "Knowledge base not found")
		return knowledgeBase{}, false
	}
	return base, true
}
func (s *Server) createKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var base knowledgeBase
	if err := decode(r, &base); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	if base.ProjectID == "" || !projectAllowed(s.store, principal(r), base.ProjectID) {
		writeError(w, 403, "forbidden", "Project is not assigned")
		return
	}
	if !connectionName.MatchString(base.Name) || base.Dimensions < 1 || base.Dimensions > 4096 {
		writeError(w, 422, "invalid_request", "Use a lowercase knowledge-base name and 1-4096 vector dimensions")
		return
	}
	switch base.Backend {
	case "qdrant":
		target, err := url.Parse(base.Endpoint)
		if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || !knowledgeRef.MatchString(base.SecretRef) {
			writeError(w, 422, "invalid_request", "Qdrant requires an HTTP(S) endpoint and an env: API-key reference")
			return
		}
		if base.DSNRef != "" {
			writeError(w, 422, "invalid_request", "Qdrant cannot use a PostgreSQL DSN")
			return
		}
	case "pgvector":
		if !knowledgeRef.MatchString(base.DSNRef) || base.Endpoint != "" || base.SecretRef != "" {
			writeError(w, 422, "invalid_request", "pgvector requires an env: DSN reference")
			return
		}
	default:
		writeError(w, 422, "invalid_request", "Backend must be qdrant or pgvector")
		return
	}
	for _, existing := range store.ListDocs[knowledgeBase](s.store, knowledgeKind) {
		if existing.ProjectID == base.ProjectID && existing.Name == base.Name {
			writeError(w, 409, "already_exists", "Knowledge-base name already exists in this project")
			return
		}
	}
	base.ID = "kb-" + knowledgeID()
	base.CreatedAt = time.Now().UTC()
	if err := base.prepare(r.Context()); err != nil {
		writeError(w, 503, "backend_unavailable", "Knowledge backend could not be prepared")
		return
	}
	saved, err := store.UpdateDoc(s.store, knowledgeKind, base.ID, func(_ knowledgeBase, exists bool) (knowledgeBase, error) {
		if exists {
			return knowledgeBase{}, store.ErrConflict
		}
		return base, nil
	}, "knowledge_base.created", actor(r))
	writeMutation(w, saved.summary(), err, http.StatusCreated)
}
func (base knowledgeBase) credential() (string, error) {
	ref := base.SecretRef
	if base.Backend == "pgvector" {
		ref = base.DSNRef
	}
	value := os.Getenv(strings.TrimPrefix(ref, "env:"))
	if value == "" {
		return "", errors.New("knowledge credential unavailable")
	}
	return value, nil
}
func (base knowledgeBase) pgConnect(ctx context.Context) (*pgx.Conn, error) {
	dsn, err := base.credential()
	if err != nil {
		return nil, err
	}
	return pgx.Connect(ctx, dsn)
}
func (base knowledgeBase) prepare(ctx context.Context) error {
	if base.Backend == "qdrant" {
		result, err := base.qdrant(ctx, http.MethodPut, "/collections/"+base.ID, map[string]any{"vectors": map[string]any{"size": base.Dimensions, "distance": "Cosine"}})
		_ = result
		return err
	}
	conn, err := base.pgConnect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS kionga_knowledge_chunks (
  id text PRIMARY KEY, project_id text NOT NULL, knowledge_base_id text NOT NULL,
  body text NOT NULL, metadata jsonb NOT NULL DEFAULT '{}'::jsonb, embedding vector NOT NULL
 )`)
	return err
}
func (base knowledgeBase) qdrant(ctx context.Context, method, path string, payload any) (json.RawMessage, error) {
	token, err := base.credential()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	target := strings.TrimRight(base.Endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("api-key", token)
	req.Header.Set("Content-Type", "application/json")
	response, err := knowledgeHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Qdrant returned %d", response.StatusCode)
	}
	return body, nil
}
func (s *Server) addKnowledgeChunk(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	base, ok := s.authorizedKnowledge(w, r)
	if !ok {
		return
	}
	var point knowledgePoint
	if err := decode(r, &point); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	metadata, _ := json.Marshal(point.Metadata)
	if len(point.Text) == 0 || len(point.Text) > 32000 || len(metadata) > 4096 || !validKnowledgeVector(point.Vector, base.Dimensions) {
		writeError(w, 422, "invalid_request", "Text, metadata or embedding exceeds the knowledge-base limits")
		return
	}
	id := knowledgePointID()
	if base.Backend == "qdrant" {
		_, err := base.qdrant(r.Context(), http.MethodPut, "/collections/"+base.ID+"/points?wait=true", map[string]any{
			"points": []any{map[string]any{"id": id, "vector": point.Vector, "payload": map[string]any{"text": point.Text, "metadata": point.Metadata, "project_id": base.ProjectID, "knowledge_base_id": base.ID}}},
		})
		if err != nil {
			writeError(w, 503, "backend_unavailable", "Knowledge backend could not write the chunk")
			return
		}
	} else {
		conn, err := base.pgConnect(r.Context())
		if err != nil {
			writeError(w, 503, "backend_unavailable", "Knowledge backend is unavailable")
			return
		}
		defer conn.Close(r.Context())
		_, err = conn.Exec(r.Context(), `INSERT INTO kionga_knowledge_chunks (id,project_id,knowledge_base_id,body,metadata,embedding) VALUES ($1,$2,$3,$4,$5,$6::vector)`, id, base.ProjectID, base.ID, point.Text, metadata, knowledgeVectorLiteral(point.Vector))
		if err != nil {
			writeError(w, 503, "backend_unavailable", "Knowledge backend could not write the chunk")
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}
func (s *Server) searchKnowledge(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	base, ok := s.authorizedKnowledge(w, r)
	if !ok {
		return
	}
	var query knowledgeQuery
	if err := decode(r, &query); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	if !validKnowledgeVector(query.Vector, base.Dimensions) || query.Limit < 1 || query.Limit > 20 {
		writeError(w, 422, "invalid_request", "Use a matching embedding and a limit of 1-20")
		return
	}
	items := []knowledgeMatch{}
	if base.Backend == "qdrant" {
		raw, err := base.qdrant(r.Context(), http.MethodPost, "/collections/"+base.ID+"/points/query", map[string]any{
			"query": query.Vector, "limit": query.Limit, "with_payload": true,
			"filter": map[string]any{"must": []any{
				map[string]any{"key": "project_id", "match": map[string]any{"value": base.ProjectID}},
				map[string]any{"key": "knowledge_base_id", "match": map[string]any{"value": base.ID}},
			}},
		})
		if err != nil {
			writeError(w, 503, "backend_unavailable", "Knowledge backend search failed")
			return
		}
		var response struct {
			Result struct {
				Points []struct {
					ID      any     `json:"id"`
					Score   float64 `json:"score"`
					Payload struct {
						Text     string         `json:"text"`
						Metadata map[string]any `json:"metadata"`
					} `json:"payload"`
				} `json:"points"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			writeError(w, 503, "backend_unavailable", "Knowledge backend returned invalid results")
			return
		}
		for _, point := range response.Result.Points {
			items = append(items, knowledgeMatch{ID: fmt.Sprint(point.ID), Text: point.Payload.Text, Metadata: point.Payload.Metadata, Score: point.Score})
		}
	} else {
		conn, err := base.pgConnect(r.Context())
		if err != nil {
			writeError(w, 503, "backend_unavailable", "Knowledge backend is unavailable")
			return
		}
		defer conn.Close(r.Context())
		rows, err := conn.Query(r.Context(), `SELECT id, body, metadata, 1-(embedding <=> $1::vector) AS score
   FROM kionga_knowledge_chunks WHERE project_id=$2 AND knowledge_base_id=$3
   ORDER BY embedding <=> $1::vector LIMIT $4`, knowledgeVectorLiteral(query.Vector), base.ProjectID, base.ID, query.Limit)
		if err != nil {
			writeError(w, 503, "backend_unavailable", "Knowledge backend search failed")
			return
		}
		defer rows.Close()
		for rows.Next() {
			var item knowledgeMatch
			var metadata []byte
			if err := rows.Scan(&item.ID, &item.Text, &metadata, &item.Score); err != nil {
				writeError(w, 503, "backend_unavailable", "Knowledge backend returned invalid results")
				return
			}
			_ = json.Unmarshal(metadata, &item.Metadata)
			items = append(items, item)
		}
		if rows.Err() != nil {
			writeError(w, 503, "backend_unavailable", "Knowledge backend search failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
