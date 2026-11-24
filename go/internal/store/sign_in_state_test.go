package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kiongahq/platform/pkg/api"
)

// The file store's redemption records are bounded: every redemption drops
// the expired ones, so the set holds only tickets still inside their window.
func TestFileStoreRedeemedTokensStayBounded(t *testing.T) {
	repo := New()
	ctx := context.Background()
	for i := 0; i < 500; i++ {
		if err := repo.RedeemOnce(ctx, "ticket", fmt.Sprint("expired-", i), time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.RedeemOnce(ctx, "ticket", "live", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := len(repo.signIn.redeemed); got != 1 {
		t.Fatalf("records kept = %d, want only the live one", got)
	}
	if err := repo.RedeemOnce(ctx, "ticket", "live", time.Now().Add(time.Hour)); err != ErrAlreadyRedeemed {
		t.Fatalf("live record lost: %v", err)
	}
}

// Local sessions are bounded the same way: every sign-in drops the expired
// ones. They are never written to the data file, so the token hashes do not
// reach disk and a restart ends the sessions, as before.
func TestFileStoreLocalSessionsStayBoundedAndOffDisk(t *testing.T) {
	path := t.TempDir() + "/state.json"
	repo := New(path)
	ctx := context.Background()
	for i := 0; i < 500; i++ {
		if err := repo.CreateLocalSession(ctx, fmt.Sprint("expired-", i), "admin", time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.CreateLocalSession(ctx, "live-session-hash", "admin", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := len(repo.signIn.sessions); got != 1 {
		t.Fatalf("sessions kept = %d, want only the live one", got)
	}
	if _, err := repo.CreateProject(api.CreateProjectRequest{Name: "Forces a write"}, "admin"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "live-session-hash") {
		t.Fatal("a session reached the data file")
	}
	if _, _, found, _ := New(path).LocalSession(ctx, "live-session-hash"); found {
		t.Fatal("a session survived a restart of the file store")
	}
}
