package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kiongahq/platform/pkg/api"
)

// Documents is the generic, tenant-scoped resource API that newer domains
// (policies, groups, revisions, schedules, media, local accounts) build on.
// Both backends implement it once, so a new resource kind needs no new
// Repository methods. Every mutation writes an audit event in the same
// transaction as the change.
type Documents interface {
	// GetDocument returns the stored JSON or ErrNotFound.
	GetDocument(kind, id string) (json.RawMessage, error)
	// ListDocuments returns every document of a kind, newest first.
	ListDocuments(kind string) []json.RawMessage
	// UpdateDocument runs fn on the current value (nil when absent) while
	// holding a row lock, and stores what fn returns. Returning
	// ErrSkipWrite leaves the document unchanged without error. An empty
	// action skips the audit record; use it only for internal bookkeeping
	// such as leases, never for user-visible changes.
	UpdateDocument(kind, id string, fn func(current json.RawMessage) (any, error), action, actor string) (json.RawMessage, error)
	// DeleteDocument removes a document or returns ErrNotFound.
	DeleteDocument(kind, id, action, actor string) error
}

// ErrSkipWrite lets an UpdateDocument callback decline to write.
var ErrSkipWrite = errors.New("skip write")

// GetDoc decodes one document.
func GetDoc[T any](docs Documents, kind, id string) (T, error) {
	var value T
	raw, err := docs.GetDocument(kind, id)
	if err != nil {
		return value, err
	}
	return value, json.Unmarshal(raw, &value)
}

// ListDocs decodes every document of a kind, skipping undecodable rows.
func ListDocs[T any](docs Documents, kind string) []T {
	out := []T{}
	for _, raw := range docs.ListDocuments(kind) {
		var value T
		if json.Unmarshal(raw, &value) == nil {
			out = append(out, value)
		}
	}
	return out
}

// UpdateDoc is the typed form of UpdateDocument. exists reports whether a
// current value was found.
func UpdateDoc[T any](docs Documents, kind, id string, fn func(current T, exists bool) (T, error), action, actor string) (T, error) {
	var result T
	raw, err := docs.UpdateDocument(kind, id, func(current json.RawMessage) (any, error) {
		var value T
		exists := current != nil
		if exists {
			if err := json.Unmarshal(current, &value); err != nil {
				return nil, err
			}
		}
		return fn(value, exists)
	}, action, actor)
	if err != nil {
		return result, err
	}
	return result, json.Unmarshal(raw, &result)
}

// --- file/memory backend ---

type storedDocument struct {
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

func (s *Store) GetDocument(kind, id string) (json.RawMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	doc, ok := s.data.Documents[kind][id]
	if !ok {
		return nil, ErrNotFound
	}
	return append(json.RawMessage(nil), doc.Payload...), nil
}

func (s *Store) ListDocuments(kind string) []json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	docs := make([]storedDocument, 0, len(s.data.Documents[kind]))
	for _, doc := range s.data.Documents[kind] {
		docs = append(docs, doc)
	}
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].CreatedAt.After(docs[j].CreatedAt) })
	out := make([]json.RawMessage, len(docs))
	for i, doc := range docs {
		out[i] = append(json.RawMessage(nil), doc.Payload...)
	}
	return out
}

func (s *Store) UpdateDocument(kind, id string, fn func(json.RawMessage) (any, error), action, actor string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.data.Documents[kind][id]
	var current json.RawMessage
	if ok {
		current = append(json.RawMessage(nil), existing.Payload...)
	}
	value, err := fn(current)
	if errors.Is(err, ErrSkipWrite) {
		if !ok {
			return nil, ErrNotFound
		}
		return current, nil
	}
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if s.data.Documents == nil {
		s.data.Documents = map[string]map[string]storedDocument{}
	}
	if s.data.Documents[kind] == nil {
		s.data.Documents[kind] = map[string]storedDocument{}
	}
	created := time.Now().UTC()
	if ok {
		created = existing.CreatedAt
	}
	s.data.Documents[kind][id] = storedDocument{Payload: payload, CreatedAt: created}
	if action != "" {
		s.record(action, kind, id, actor, nil)
	}
	return payload, s.persist()
}

func (s *Store) DeleteDocument(kind, id, action, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Documents[kind][id]; !ok {
		return ErrNotFound
	}
	delete(s.data.Documents[kind], id)
	s.record(action, kind, id, actor, nil)
	return s.persist()
}

// --- PostgreSQL backend ---

func (p *Postgres) GetDocument(kind, resourceID string) (json.RawMessage, error) {
	var raw []byte
	err := p.pool.QueryRow(context.Background(), `SELECT payload FROM platform_resources WHERE tenant_id=$1 AND kind=$2 AND id=$3`, p.tenant, kind, resourceID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return raw, err
}

func (p *Postgres) ListDocuments(kind string) []json.RawMessage {
	rows, err := p.pool.Query(context.Background(), `SELECT payload FROM platform_resources WHERE tenant_id=$1 AND kind=$2 ORDER BY created_at DESC`, p.tenant, kind)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) == nil {
			out = append(out, raw)
		}
	}
	return out
}

func (p *Postgres) UpdateDocument(kind, resourceID string, fn func(json.RawMessage) (any, error), action, actor string) (json.RawMessage, error) {
	ctx := context.Background()
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize concurrent creators of the same id as well as updaters: a
	// row lock alone cannot lock a row that does not exist yet.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1 || '/' || $2 || '/' || $3))`, p.tenant, kind, resourceID); err != nil {
		return nil, err
	}
	var current json.RawMessage
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT payload FROM platform_resources WHERE tenant_id=$1 AND kind=$2 AND id=$3 FOR UPDATE`, p.tenant, kind, resourceID).Scan(&raw)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, err
	default:
		current = raw
	}
	value, err := fn(current)
	if errors.Is(err, ErrSkipWrite) {
		if current == nil {
			return nil, ErrNotFound
		}
		return current, nil
	}
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform_resources (tenant_id,kind,id,payload) VALUES ($1,$2,$3,$4)
		ON CONFLICT (tenant_id,kind,id) DO UPDATE SET payload=EXCLUDED.payload, updated_at=now()`, p.tenant, kind, resourceID, payload); err != nil {
		return nil, err
	}
	if action != "" {
		if err = auditTx(ctx, tx, p.tenant, action, kind, resourceID, actor); err != nil {
			return nil, err
		}
	}
	return payload, tx.Commit(ctx)
}

func (p *Postgres) DeleteDocument(kind, resourceID, action, actor string) error {
	ctx := context.Background()
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM platform_resources WHERE tenant_id=$1 AND kind=$2 AND id=$3`, p.tenant, kind, resourceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err = auditTx(ctx, tx, p.tenant, action, kind, resourceID, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func auditTx(ctx context.Context, tx pgx.Tx, tenant, action, kind, resourceID, actor string) error {
	event := api.AuditEvent{ID: id("evt"), Action: action, Resource: kind, ResourceID: resourceID, Actor: actorOrAnonymous(actor), CreatedAt: time.Now().UTC()}
	payload, _ := json.Marshal(event)
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events (tenant_id,id,action,resource,resource_id,actor,metadata,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		tenant, event.ID, event.Action, event.Resource, event.ResourceID, event.Actor, event.Metadata, event.CreatedAt); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO outbox_events (tenant_id,id,topic,event_key,payload) VALUES ($1,$2,$3,$4,$5)`,
		tenant, id("out"), "mlaiops.audit.operations", resourceID, payload)
	return err
}
