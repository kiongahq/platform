package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// Sealed account credentials, as the credential rewrap command sees them.
//
// Records are keyed by sha256(subject) and do not store the subject, but the
// subject is the associated data the credential was sealed with. It is
// recovered from the audit trail: every write of a record is audited with the
// writing subject as actor, and a candidate is accepted only if its hash is
// the record id, so a wrong candidate can never be used.

// Kinds of sealed credential records.
const (
	HubAccountKind    = "hub_account"
	GitHubAccountKind = "github_account"
)

// SealedCredentialKinds lists every kind the rewrap command converts.
var SealedCredentialKinds = []string{HubAccountKind, GitHubAccountKind}

// RewrapActor is the audit actor of rewrap writes; it never hashes to a
// record id, so it is never mistaken for a subject.
const RewrapActor = "system:credential-rewrap"

// ErrCredentialChanged means a record changed between read and rewrite.
var ErrCredentialChanged = errors.New("credential changed since it was read")

// SealedCredential is one stored ciphertext and, when known, its subject.
type SealedCredential struct {
	Kind       string
	ID         string
	Subject    string // empty when no audited actor hashes to ID
	Ciphertext string
}

// SealedCredentialStore reads and conditionally rewrites sealed credentials.
// Both repository backends implement it.
type SealedCredentialStore interface {
	SealedCredentials() ([]SealedCredential, error)
	// ReplaceSealedCredential swaps the ciphertext only if it still equals
	// old; otherwise it returns ErrCredentialChanged and writes nothing.
	ReplaceSealedCredential(kind, id, old, replacement string) error
}

func subjectID(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:])
}

func knownKind(kind string) bool { return kind == HubAccountKind || kind == GitHubAccountKind }

// resolveSubjects fills Subject from candidate actors per kind and id.
func resolveSubjects(records []SealedCredential, actors map[[2]string][]string) {
	for i := range records {
		for _, actor := range actors[[2]string{records[i].Kind, records[i].ID}] {
			if subjectID(actor) == records[i].ID {
				records[i].Subject = actor
				break
			}
		}
	}
}

// swapCiphertext replaces the "ciphertext" field of a JSON document, keeping
// every other field as stored.
func swapCiphertext(current json.RawMessage, old, replacement string) (map[string]json.RawMessage, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(current, &doc); err != nil {
		return nil, err
	}
	var stored string
	if err := json.Unmarshal(doc["ciphertext"], &stored); err != nil || stored != old {
		return nil, ErrCredentialChanged
	}
	encoded, err := json.Marshal(replacement)
	if err != nil {
		return nil, err
	}
	doc["ciphertext"] = encoded
	return doc, nil
}

// --- file/memory backend ---

func (s *Store) SealedCredentials() ([]SealedCredential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []SealedCredential
	for id, account := range s.data.HubAccounts {
		if account.Ciphertext != "" {
			out = append(out, SealedCredential{Kind: HubAccountKind, ID: id, Ciphertext: account.Ciphertext})
		}
	}
	for id, doc := range s.data.Documents[GitHubAccountKind] {
		var account struct {
			Ciphertext string `json:"ciphertext"`
		}
		if json.Unmarshal(doc.Payload, &account) == nil && account.Ciphertext != "" {
			out = append(out, SealedCredential{Kind: GitHubAccountKind, ID: id, Ciphertext: account.Ciphertext})
		}
	}
	actors := map[[2]string][]string{}
	for _, event := range s.data.Audit {
		if knownKind(event.Resource) {
			key := [2]string{event.Resource, event.ResourceID}
			actors[key] = append(actors[key], event.Actor)
		}
	}
	resolveSubjects(out, actors)
	return out, nil
}

func (s *Store) ReplaceSealedCredential(kind, id, old, replacement string) error {
	switch kind {
	case HubAccountKind:
		s.mu.Lock()
		defer s.mu.Unlock()
		account, ok := s.data.HubAccounts[id]
		if !ok || account.Ciphertext != old {
			return ErrCredentialChanged
		}
		account.Ciphertext = replacement
		s.data.HubAccounts[id] = account
		s.record("credential.rewrapped", kind, id, RewrapActor, nil)
		return s.persist()
	case GitHubAccountKind:
		_, err := s.UpdateDocument(kind, id, func(current json.RawMessage) (any, error) {
			if current == nil {
				return nil, ErrCredentialChanged
			}
			return swapCiphertext(current, old, replacement)
		}, "credential.rewrapped", RewrapActor)
		if errors.Is(err, ErrNotFound) {
			return ErrCredentialChanged
		}
		return err
	default:
		return fmt.Errorf("unknown credential kind %q", kind)
	}
}

// --- PostgreSQL backend ---

func (p *Postgres) SealedCredentials() ([]SealedCredential, error) {
	ctx := context.Background()
	rows, err := p.pool.Query(ctx, `SELECT kind, id, COALESCE(payload->>'ciphertext', '') FROM platform_resources
		WHERE tenant_id=$1 AND kind = ANY($2) ORDER BY kind, id`, p.tenant, SealedCredentialKinds)
	if err != nil {
		return nil, err
	}
	var out []SealedCredential
	for rows.Next() {
		var record SealedCredential
		if err := rows.Scan(&record.Kind, &record.ID, &record.Ciphertext); err != nil {
			rows.Close()
			return nil, err
		}
		if record.Ciphertext != "" {
			out = append(out, record)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	actorRows, err := p.pool.Query(ctx, `SELECT DISTINCT resource, resource_id, actor FROM audit_events
		WHERE tenant_id=$1 AND resource = ANY($2)`, p.tenant, SealedCredentialKinds)
	if err != nil {
		return nil, err
	}
	defer actorRows.Close()
	actors := map[[2]string][]string{}
	for actorRows.Next() {
		var kind, id, actor string
		if err := actorRows.Scan(&kind, &id, &actor); err != nil {
			return nil, err
		}
		key := [2]string{kind, id}
		actors[key] = append(actors[key], actor)
	}
	if err := actorRows.Err(); err != nil {
		return nil, err
	}
	resolveSubjects(out, actors)
	return out, nil
}

func (p *Postgres) ReplaceSealedCredential(kind, id, old, replacement string) error {
	if !knownKind(kind) {
		return fmt.Errorf("unknown credential kind %q", kind)
	}
	_, err := p.UpdateDocument(kind, id, func(current json.RawMessage) (any, error) {
		if current == nil {
			return nil, ErrCredentialChanged
		}
		return swapCiphertext(current, old, replacement)
	}, "credential.rewrapped", RewrapActor)
	if errors.Is(err, ErrNotFound) {
		return ErrCredentialChanged
	}
	return err
}
