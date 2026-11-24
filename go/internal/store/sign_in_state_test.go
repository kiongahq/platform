package store

import (
	"context"
	"fmt"
	"testing"
	"time"
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
