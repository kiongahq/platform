package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"
)

type WorkspaceEdgeIdentity struct {
	Subject, WorkspaceName, Kind string
}

func newEdgeToken() (string, [32]byte, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", [32]byte{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(bytes[:])
	return token, sha256.Sum256([]byte(token)), nil
}

func edgeHash(token string) ([32]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return [32]byte{}, errors.New("invalid workspace token")
	}
	return sha256.Sum256([]byte(token)), nil
}

func (p *Postgres) IssueWorkspaceTicket(ctx context.Context, identity WorkspaceEdgeIdentity) (string, error) {
	token, hash, err := newEdgeToken()
	if err != nil {
		return "", err
	}
	_, err = p.pool.Exec(ctx, `INSERT INTO workspace_edge_tickets (tenant_id,token_hash,subject,workspace_name,kind,expires_at)
		VALUES ($1,$2,$3,$4,$5,now()+interval '45 seconds')`, p.tenant, hash[:], identity.Subject, identity.WorkspaceName, identity.Kind)
	return token, err
}

// RedeemWorkspaceTicket atomically removes a ticket and creates a short-lived
// host-only session. A replay cannot mint a second session on another replica.
func (p *Postgres) RedeemWorkspaceTicket(ctx context.Context, ticket string) (WorkspaceEdgeIdentity, string, error) {
	hash, err := edgeHash(ticket)
	if err != nil {
		return WorkspaceEdgeIdentity{}, "", err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return WorkspaceEdgeIdentity{}, "", err
	}
	defer tx.Rollback(ctx)
	var identity WorkspaceEdgeIdentity
	err = tx.QueryRow(ctx, `DELETE FROM workspace_edge_tickets WHERE tenant_id=$1 AND token_hash=$2 AND expires_at>now()
		RETURNING subject,workspace_name,kind`, p.tenant, hash[:]).Scan(&identity.Subject, &identity.WorkspaceName, &identity.Kind)
	if err != nil {
		return WorkspaceEdgeIdentity{}, "", err
	}
	session, sessionHash, err := newEdgeToken()
	if err != nil {
		return WorkspaceEdgeIdentity{}, "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO workspace_edge_sessions (tenant_id,token_hash,subject,workspace_name,kind,expires_at)
		VALUES ($1,$2,$3,$4,$5,now()+interval '30 minutes')`, p.tenant, sessionHash[:], identity.Subject, identity.WorkspaceName, identity.Kind)
	if err != nil {
		return WorkspaceEdgeIdentity{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return WorkspaceEdgeIdentity{}, "", err
	}
	return identity, session, nil
}

func (p *Postgres) WorkspaceEdgeSession(ctx context.Context, token string) (WorkspaceEdgeIdentity, error) {
	hash, err := edgeHash(token)
	if err != nil {
		return WorkspaceEdgeIdentity{}, err
	}
	var identity WorkspaceEdgeIdentity
	err = p.pool.QueryRow(ctx, `SELECT subject,workspace_name,kind FROM workspace_edge_sessions
		WHERE tenant_id=$1 AND token_hash=$2 AND expires_at>now()`, p.tenant, hash[:]).Scan(&identity.Subject, &identity.WorkspaceName, &identity.Kind)
	return identity, err
}

func (p *Postgres) RevokeWorkspaceEdgeSession(ctx context.Context, token string) error {
	hash, err := edgeHash(token)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, `DELETE FROM workspace_edge_sessions WHERE tenant_id=$1 AND token_hash=$2`, p.tenant, hash[:])
	return err
}

func (p *Postgres) RevokeWorkspaceEdgeSessionsForSubject(ctx context.Context, subject string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM workspace_edge_sessions WHERE tenant_id=$1 AND subject=$2`, p.tenant, subject)
	return err
}

func (p *Postgres) PurgeExpiredWorkspaceEdgeSessions(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := p.pool.Exec(ctx, `DELETE FROM workspace_edge_tickets WHERE expires_at<now()`); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, `DELETE FROM workspace_edge_sessions WHERE expires_at<now()`)
	return err
}
