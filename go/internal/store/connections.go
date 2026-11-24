package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kiongahq/platform/pkg/api"
)

// ErrConnectionChanged is returned when a connection no longer points at the
// service, address and credential the caller checked.
var ErrConnectionChanged = errors.New("the connection changed while it was being checked; try again")

// ErrConnectionTypeLocked is returned when an update would change the service
// of a connection that has been activated as a runtime.
var ErrConnectionTypeLocked = errors.New("an activated runtime connection cannot change its service; create a new connection instead")

// ErrConnectionUnverified is returned when an update would change the address
// or credential of an activated runtime connection without a passing check.
var ErrConnectionUnverified = errors.New("an activated runtime connection must pass its check before its address or credential changes")

// ValidateConnectionRequest holds the rules create and update share.
func ValidateConnectionRequest(req api.CreateConnectionRequest) error {
	if req.Name == "" || req.Type == "" || req.SecretRef == "" {
		return errors.New("name, type and secret_ref are required")
	}
	return nil
}

// connectionNameTaken reports whether another connection already uses name.
func connectionNameTaken(items []api.Connection, name, exceptID string) bool {
	for _, item := range items {
		if item.ID != exceptID && strings.EqualFold(item.Name, name) {
			return true
		}
	}
	return false
}

// ConnectionTargetChanged reports whether req points the connection at a
// different service, address or credential. A rename alone does not.
func ConnectionTargetChanged(current api.Connection, req api.CreateConnectionRequest) bool {
	return current.Type != req.Type || current.Endpoint != req.Endpoint || current.SecretRef != req.SecretRef
}

// applyConnectionUpdate is the update rule both stores share. The ID, creation
// time and activation are kept. When the target changes the last check no
// longer describes the connection: verified records the caller's passing
// check, otherwise the connection returns to pending until it is tested. An
// activated connection keeps its service and only accepts a verified target,
// so the active runtime never points at an address nobody checked.
func applyConnectionUpdate(current api.Connection, req api.CreateConnectionRequest, verified bool, now time.Time) (api.Connection, []string, error) {
	changed := []string{}
	if current.Name != req.Name {
		changed = append(changed, "name")
	}
	if current.Type != req.Type {
		changed = append(changed, "type")
	}
	if current.Endpoint != req.Endpoint {
		changed = append(changed, "endpoint")
	}
	if current.SecretRef != req.SecretRef {
		changed = append(changed, "secret_ref")
	}
	target := ConnectionTargetChanged(current, req)
	if current.ActivatedAt != nil && target {
		if current.Type != req.Type {
			return current, nil, ErrConnectionTypeLocked
		}
		if !verified {
			return current, nil, ErrConnectionUnverified
		}
	}
	current.Name, current.Type, current.Endpoint, current.SecretRef = req.Name, req.Type, req.Endpoint, req.SecretRef
	switch {
	case !target:
	case verified:
		current.Status, current.CheckedAt, current.Message = "healthy", &now, "Verified after the change"
		if current.ActivatedAt != nil {
			current.Message = "Verified after the change and still active for new operations"
		}
	default:
		current.Status, current.CheckedAt, current.Message = "pending", nil, "Configuration changed. Run Test to check it."
	}
	return current, changed, nil
}

// UpdateConnection replaces the name, service, address and credential
// reference of a connection. See applyConnectionUpdate for the rules.
func (s *Store) UpdateConnection(connectionID string, req api.CreateConnectionRequest, verified bool, actor string) (api.Connection, error) {
	if err := ValidateConnectionRequest(req); err != nil {
		return api.Connection{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Connections {
		if s.data.Connections[i].ID != connectionID {
			continue
		}
		current := s.data.Connections[i]
		if !strings.EqualFold(current.Name, req.Name) && connectionNameTaken(s.data.Connections, req.Name, connectionID) {
			return api.Connection{}, ErrConflict
		}
		updated, changed, err := applyConnectionUpdate(current, req, verified, time.Now().UTC())
		if err != nil {
			return api.Connection{}, err
		}
		s.data.Connections[i] = updated
		s.record("connection.updated", "connection", connectionID, actor, map[string]any{"changed": changed})
		return updated, s.persist()
	}
	return api.Connection{}, ErrNotFound
}

// UpdateConnection replaces the name, service, address and credential
// reference of a connection. See applyConnectionUpdate for the rules.
func (p *Postgres) UpdateConnection(connectionID string, req api.CreateConnectionRequest, verified bool, actor string) (api.Connection, error) {
	if err := ValidateConnectionRequest(req); err != nil {
		return api.Connection{}, err
	}
	return p.connectionWrite("connection.updated", actor, func(existing []api.Connection) (api.Connection, map[string]any, error) {
		current, err := findConnection(existing, connectionID)
		if err != nil {
			return current, nil, err
		}
		if !strings.EqualFold(current.Name, req.Name) && connectionNameTaken(existing, req.Name, connectionID) {
			return current, nil, ErrConflict
		}
		next, changed, err := applyConnectionUpdate(current, req, verified, time.Now().UTC())
		return next, map[string]any{"changed": changed}, err
	})
}

// CreateConnection adds a connection. The file store has always refused a
// second connection with a used name; this store now does too, or a rename
// could not keep names unique.
func (p *Postgres) CreateConnection(req api.CreateConnectionRequest, actor string) (api.Connection, error) {
	if err := ValidateConnectionRequest(req); err != nil {
		return api.Connection{}, err
	}
	return p.connectionWrite("connection.created", actor, func(existing []api.Connection) (api.Connection, map[string]any, error) {
		if connectionNameTaken(existing, req.Name, "") {
			return api.Connection{}, nil, ErrConflict
		}
		return api.Connection{ID: id("conn"), Name: req.Name, Type: req.Type, Endpoint: req.Endpoint, SecretRef: req.SecretRef, Status: "pending", CreatedAt: time.Now().UTC()}, nil, nil
	})
}

// UpdateConnectionStatus records a check. It takes the same lock as the other
// connection writes, so it cannot write back a record read before an update.
func (p *Postgres) UpdateConnectionStatus(connectionID, status, message, actor string) (api.Connection, error) {
	return p.connectionWrite("connection.checked", actor, func(existing []api.Connection) (api.Connection, map[string]any, error) {
		current, err := findConnection(existing, connectionID)
		if err != nil {
			return current, nil, err
		}
		now := time.Now().UTC()
		current.Status, current.Message, current.CheckedAt = status, message, &now
		return current, map[string]any{"status": status}, nil
	})
}

// sameConnectionTarget reports whether two records point at the same
// service, address and credential.
func sameConnectionTarget(a, b api.Connection) bool {
	return a.Type == b.Type && a.Endpoint == b.Endpoint && a.SecretRef == b.SecretRef
}

func activated(c api.Connection, now time.Time) api.Connection {
	c.ActivatedAt = &now
	c.Status, c.CheckedAt, c.Message = "healthy", &now, "Verified and activated for new operations"
	return c
}

func findConnection(items []api.Connection, id string) (api.Connection, error) {
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return api.Connection{}, ErrNotFound
}

// ActivateConnection activates the connection the caller verified. If its
// service, address or credential changed since, nothing is activated and
// ErrConnectionChanged is returned, so an unchecked target never becomes the
// runtime.
func (s *Store) ActivateConnection(verified api.Connection, actor string) (api.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Connections {
		c := &s.data.Connections[i]
		if c.ID != verified.ID {
			continue
		}
		if !sameConnectionTarget(*c, verified) {
			return api.Connection{}, ErrConnectionChanged
		}
		*c = activated(*c, time.Now().UTC())
		s.record("connection.activated", "connection", c.ID, actor, nil)
		return *c, s.persist()
	}
	return api.Connection{}, ErrNotFound
}

func (p *Postgres) ActivateConnection(verified api.Connection, actor string) (api.Connection, error) {
	return p.connectionWrite("connection.activated", actor, func(existing []api.Connection) (api.Connection, map[string]any, error) {
		current, err := findConnection(existing, verified.ID)
		if err != nil {
			return current, nil, err
		}
		if !sameConnectionTarget(current, verified) {
			return current, nil, ErrConnectionChanged
		}
		return activated(current, time.Now().UTC()), nil, nil
	})
}

// connectionWrite runs one change to the tenant's connections under a
// transaction-scoped advisory lock. decide sees every stored connection and
// returns the record to write, so name uniqueness and the activation rules
// hold across concurrent requests and gateway replicas. Connections are
// documents, so there is no index to enforce this.
func (p *Postgres) connectionWrite(action, actor string, decide func([]api.Connection) (api.Connection, map[string]any, error)) (api.Connection, error) {
	ctx := context.Background()
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return api.Connection{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1::text || '/connections'))`, p.tenant); err != nil {
		return api.Connection{}, err
	}
	rows, err := tx.Query(ctx, `SELECT payload FROM platform_resources WHERE tenant_id=$1 AND kind='connection' ORDER BY created_at DESC`, p.tenant)
	if err != nil {
		return api.Connection{}, err
	}
	existing := []api.Connection{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return api.Connection{}, err
		}
		value, decodeErr := decodeStoredValue[api.Connection](raw)
		if decodeErr != nil {
			rows.Close()
			return api.Connection{}, decodeErr
		}
		existing = append(existing, value)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return api.Connection{}, err
	}
	next, metadata, err := decide(existing)
	if err != nil {
		return api.Connection{}, err
	}
	if err = p.writeTx(ctx, tx, "connection", next.ID, next, action, actor, metadata); err != nil {
		return api.Connection{}, err
	}
	return next, tx.Commit(ctx)
}
