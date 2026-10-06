package store

import (
	"context"
	"os"
	"testing"
)

func TestWorkspaceEdgeTicketsAcrossPostgresConnections(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration")
	}
	ctx := context.Background()
	marker, _, err := newEdgeToken()
	if err != nil {
		t.Fatal(err)
	}
	tenant := "edge-test-" + marker[:12]
	first, err := OpenPostgres(ctx, databaseURL, tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenPostgres(ctx, databaseURL, tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	identity := WorkspaceEdgeIdentity{Subject: "alice", WorkspaceName: "ws-alice", Kind: "workbench"}
	ticket, err := first.IssueWorkspaceTicket(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	got, session, err := second.RedeemWorkspaceTicket(ctx, ticket)
	if err != nil || got != identity {
		t.Fatalf("redeem: %+v %v", got, err)
	}
	if _, _, err := first.RedeemWorkspaceTicket(ctx, ticket); err == nil {
		t.Fatal("single-use ticket replay succeeded")
	}
	if got, err := first.WorkspaceEdgeSession(ctx, session); err != nil || got != identity {
		t.Fatalf("shared session: %+v %v", got, err)
	}
	other, err := OpenPostgres(ctx, databaseURL, tenant+"-other")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.WorkspaceEdgeSession(ctx, session); err == nil {
		t.Fatal("cross-tenant session lookup succeeded")
	}
	if err := second.RevokeWorkspaceEdgeSessionsForSubject(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.WorkspaceEdgeSession(ctx, session); err == nil {
		t.Fatal("logout did not revoke session")
	}
	if _, err := first.pool.Exec(ctx, `DELETE FROM workspace_edge_tickets WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := first.pool.Exec(ctx, `DELETE FROM workspace_edge_sessions WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
}
