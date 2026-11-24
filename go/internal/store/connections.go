package store

import (
	"errors"
	"strings"
	"time"

	"github.com/kiongahq/platform/pkg/api"
)

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
// reference of a connection. The row is locked for the read-modify-write.
func (p *Postgres) UpdateConnection(connectionID string, req api.CreateConnectionRequest, verified bool, actor string) (api.Connection, error) {
	if err := ValidateConnectionRequest(req); err != nil {
		return api.Connection{}, err
	}
	current, err := get[api.Connection](p, "connection", connectionID)
	if err != nil {
		return api.Connection{}, err
	}
	if !strings.EqualFold(current.Name, req.Name) && connectionNameTaken(p.Connections(), req.Name, connectionID) {
		return api.Connection{}, ErrConflict
	}
	metadata := map[string]any{}
	updated, err := mutate(p, "connection", connectionID, func(value *api.Connection) error {
		next, changed, applyErr := applyConnectionUpdate(*value, req, verified, time.Now().UTC())
		if applyErr != nil {
			return applyErr
		}
		*value, metadata["changed"] = next, changed
		return nil
	}, "connection.updated", actor, metadata)
	if err != nil {
		return api.Connection{}, err
	}
	return updated, nil
}

func (s *Store) ActivateConnection(connectionID, actor string) (api.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Connections {
		c := &s.data.Connections[i]
		if c.ID != connectionID {
			continue
		}
		now := time.Now().UTC()
		c.ActivatedAt = &now
		c.Status, c.CheckedAt, c.Message = "healthy", &now, "Verified and activated for new operations"
		s.record("connection.activated", "connection", c.ID, actor, nil)
		return *c, s.persist()
	}
	return api.Connection{}, ErrNotFound
}

func (p *Postgres) ActivateConnection(connectionID, actor string) (api.Connection, error) {
	c, err := get[api.Connection](p, "connection", connectionID)
	if err != nil {
		return c, err
	}
	now := time.Now().UTC()
	c.ActivatedAt = &now
	c.Status, c.CheckedAt, c.Message = "healthy", &now, "Verified and activated for new operations"
	return c, p.write("connection", c.ID, c, "connection.activated", actor, nil)
}
