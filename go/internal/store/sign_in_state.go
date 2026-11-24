package store

import (
	"context"
	"errors"
	"sync"
	"time"
)

// SignInState is the short-lived sign-in state every gateway replica must
// agree on: which single-use tokens were already redeemed. PostgreSQL shares
// it between replicas; the file store keeps it in the one process that can
// serve that store.
//
// Callers treat every error as a refusal: a store that cannot answer never
// lets a sign-in through.
type SignInState interface {
	// RedeemOnce records that the token id was used for purpose. The first
	// call returns nil; every later call, on any replica, returns
	// ErrAlreadyRedeemed until the record expires. expires must be later
	// than the token's own expiry, or a replay could outlive the record.
	RedeemOnce(ctx context.Context, purpose, id string, expires time.Time) error
	// PurgeExpiredSignInState drops expired records. The write paths also
	// purge, so the state never grows without bound.
	PurgeExpiredSignInState(ctx context.Context) error
}

// ErrAlreadyRedeemed reports a second use of a single-use token.
var ErrAlreadyRedeemed = errors.New("token already redeemed")

const signInStateTimeout = 5 * time.Second

// --- file/memory backend ---

// signInMemory is the file store's sign-in state. It is never written to the
// data file: the file store serves one gateway process. A restart of that
// process forgets which tokens were redeemed, so a token could be replayed
// after a restart until its own signed expiry. PostgreSQL has no such limit.
type signInMemory struct {
	mu       sync.Mutex
	redeemed map[string]time.Time
}

// purgeLocked drops expired entries. The caller holds m.mu.
func (m *signInMemory) purgeLocked(now time.Time) {
	for key, expires := range m.redeemed {
		if now.After(expires) {
			delete(m.redeemed, key)
		}
	}
}

func (s *Store) RedeemOnce(_ context.Context, purpose, id string, expires time.Time) error {
	m := &s.signIn
	m.mu.Lock()
	defer m.mu.Unlock()
	key := purpose + "\x00" + id
	_, used := m.redeemed[key]
	if !used {
		if m.redeemed == nil {
			m.redeemed = map[string]time.Time{}
		}
		m.redeemed[key] = expires
	}
	m.purgeLocked(time.Now())
	if used {
		return ErrAlreadyRedeemed
	}
	return nil
}

func (s *Store) PurgeExpiredSignInState(context.Context) error {
	m := &s.signIn
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeLocked(time.Now())
	return nil
}

// --- PostgreSQL backend ---

// RedeemOnce relies on the primary key: of two replicas inserting the same
// id at once, one waits for the other's commit and then inserts nothing.
func (p *Postgres) RedeemOnce(ctx context.Context, purpose, id string, expires time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, signInStateTimeout)
	defer cancel()
	tag, err := p.pool.Exec(ctx, `INSERT INTO redeemed_tokens (tenant_id,purpose,token_id,expires_at)
		VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, p.tenant, purpose, id, expires)
	if err != nil {
		return err
	}
	// Best effort: the decision above stands, and the hourly purge reports
	// a store that cannot delete.
	_, _ = p.pool.Exec(ctx, `DELETE FROM redeemed_tokens WHERE tenant_id=$1 AND expires_at<now()`, p.tenant)
	if tag.RowsAffected() == 0 {
		return ErrAlreadyRedeemed
	}
	return nil
}

func (p *Postgres) PurgeExpiredSignInState(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, signInStateTimeout)
	defer cancel()
	_, err := p.pool.Exec(ctx, `DELETE FROM redeemed_tokens WHERE tenant_id=$1 AND expires_at<now()`, p.tenant)
	return err
}
