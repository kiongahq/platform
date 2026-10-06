package store

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// Only ciphertext is persisted. No account API or audit event exposes it.
type HubAccount struct {
	Username   string    `json:"username"`
	Ciphertext string    `json:"ciphertext"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func hubAccountID(subject string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(subject))) }

func (s *Store) HubAccount(subject string) (HubAccount, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	account, ok := s.data.HubAccounts[hubAccountID(subject)]
	if !ok {
		return HubAccount{}, ErrNotFound
	}
	return account, nil
}
func (s *Store) SaveHubAccount(subject string, account HubAccount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.HubAccounts == nil {
		s.data.HubAccounts = map[string]HubAccount{}
	}
	account.UpdatedAt = time.Now().UTC()
	s.data.HubAccounts[hubAccountID(subject)] = account
	s.record("huggingface.account.updated", "hub_account", hubAccountID(subject), subject, nil)
	return s.persist()
}
func (p *Postgres) HubAccount(subject string) (HubAccount, error) {
	return get[HubAccount](p, "hub_account", hubAccountID(subject))
}
func (p *Postgres) SaveHubAccount(subject string, account HubAccount) error {
	account.UpdatedAt = time.Now().UTC()
	return p.write("hub_account", hubAccountID(subject), account, "huggingface.account.updated", subject, nil)
}
