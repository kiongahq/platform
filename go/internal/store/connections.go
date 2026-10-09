package store

import (
	"github.com/kiongahq/platform/pkg/api"
	"time"
)

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
