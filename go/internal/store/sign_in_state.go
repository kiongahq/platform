package store

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// SignInState is the short-lived sign-in state every gateway replica must
// agree on: which single-use tokens were already redeemed, and which local
// (password) sessions are live. PostgreSQL shares it between replicas; the
// file store keeps it in the one process that can serve that store.
//
// Callers treat every error as a refusal: a store that cannot answer never
// lets a sign-in through.
type SignInState interface {
	// RedeemOnce records that the token id was used for purpose. The first
	// call returns nil; every later call, on any replica, returns
	// ErrAlreadyRedeemed until the record expires. expires must be later
	// than the token's own expiry, or a replay could outlive the record.
	RedeemOnce(ctx context.Context, purpose, id string, expires time.Time) error
	// CreateLocalSession stores a session under the hash of its token.
	CreateLocalSession(ctx context.Context, tokenHash, subject string, expires time.Time) error
	// LocalSession returns the session's subject and expiry. found is false
	// for an unknown or revoked session. The caller checks the expiry.
	LocalSession(ctx context.Context, tokenHash string) (subject string, expires time.Time, found bool, err error)
	// RevokeLocalSession removes a session; an unknown hash is not an error.
	RevokeLocalSession(ctx context.Context, tokenHash string) error
	// PurgeExpiredSignInState drops expired redemption records and sessions.
	// The write paths also purge, so neither set grows without bound.
	PurgeExpiredSignInState(ctx context.Context) error
}

// ErrAlreadyRedeemed reports a second use of a single-use token.
var ErrAlreadyRedeemed = errors.New("token already redeemed")

const signInStateTimeout = 5 * time.Second

// --- file/memory backend ---

// signInMemory is the file store's sign-in state. It is never written to the
// data file: the file store serves one gateway process. A restart of that
// process ends its local sessions (as before) and forgets which tokens were
// redeemed, so a token could be replayed after a restart until its own signed
// expiry. PostgreSQL has neither limit.
type signInMemory struct {
	mu       sync.Mutex
	redeemed map[string]time.Time
	sessions map[string]memorySession
}

type memorySession struct {
	subject string
	expires time.Time
}

// purgeLocked drops expired entries. The caller holds m.mu.
func (m *signInMemory) purgeLocked(now time.Time) {
	for key, expires := range m.redeemed {
		if now.After(expires) {
			delete(m.redeemed, key)
		}
	}
	for key, session := range m.sessions {
		if now.After(session.expires) {
			delete(m.sessions, key)
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

func (s *Store) CreateLocalSession(_ context.Context, tokenHash, subject string, expires time.Time) error {
	m := &s.signIn
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeLocked(time.Now())
	if m.sessions == nil {
		m.sessions = map[string]memorySession{}
	}
	m.sessions[tokenHash] = memorySession{subject: subject, expires: expires}
	return nil
}

func (s *Store) LocalSession(_ context.Context, tokenHash string) (string, time.Time, bool, error) {
	m := &s.signIn
	m.mu.Lock()
	defer m.mu.Unlock()
	session, found := m.sessions[tokenHash]
	return session.subject, session.expires, found, nil
}

func (s *Store) RevokeLocalSession(_ context.Context, tokenHash string) error {
	m := &s.signIn
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, tokenHash)
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

func (p *Postgres) CreateLocalSession(ctx context.Context, tokenHash, subject string, expires time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, signInStateTimeout)
	defer cancel()
	_, _ = p.pool.Exec(ctx, `DELETE FROM local_sessions WHERE tenant_id=$1 AND expires_at<now()`, p.tenant)
	_, err := p.pool.Exec(ctx, `INSERT INTO local_sessions (tenant_id,token_hash,subject,expires_at)
		VALUES ($1,$2,$3,$4)`, p.tenant, tokenHash, subject, expires)
	return err
}

func (p *Postgres) LocalSession(ctx context.Context, tokenHash string) (string, time.Time, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, signInStateTimeout)
	defer cancel()
	var subject string
	var expires time.Time
	err := p.pool.QueryRow(ctx, `SELECT subject,expires_at FROM local_sessions WHERE tenant_id=$1 AND token_hash=$2`,
		p.tenant, tokenHash).Scan(&subject, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, err
	}
	return subject, expires, true, nil
}

func (p *Postgres) RevokeLocalSession(ctx context.Context, tokenHash string) error {
	ctx, cancel := context.WithTimeout(ctx, signInStateTimeout)
	defer cancel()
	_, err := p.pool.Exec(ctx, `DELETE FROM local_sessions WHERE tenant_id=$1 AND token_hash=$2`, p.tenant, tokenHash)
	return err
}

func (p *Postgres) PurgeExpiredSignInState(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, signInStateTimeout)
	defer cancel()
	if _, err := p.pool.Exec(ctx, `DELETE FROM redeemed_tokens WHERE tenant_id=$1 AND expires_at<now()`, p.tenant); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, `DELETE FROM local_sessions WHERE tenant_id=$1 AND expires_at<now()`, p.tenant)
	return err
}
