package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/store"
)

// SingleUseTokens checks the contract behind single-use sign-in tickets.
// first and second are two handles on the same shared state, as two gateway
// replicas hold: a token redeemed through one is refused by both, only one
// of many concurrent redemptions wins, and expired records are purged.
// suffix keeps ids unique when a database is reused between runs.
func SingleUseTokens(t *testing.T, first, second store.SignInState, suffix string) {
	t.Helper()
	ctx := context.Background()
	live := time.Now().Add(time.Hour)

	once := "once-" + suffix
	if err := first.RedeemOnce(ctx, "ticket", once, live); err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	if err := second.RedeemOnce(ctx, "ticket", once, live); !errors.Is(err, store.ErrAlreadyRedeemed) {
		t.Fatalf("replay through the other handle: %v, want ErrAlreadyRedeemed", err)
	}
	if err := first.RedeemOnce(ctx, "ticket", once, live); !errors.Is(err, store.ErrAlreadyRedeemed) {
		t.Fatalf("replay through the same handle: %v, want ErrAlreadyRedeemed", err)
	}
	if err := second.RedeemOnce(ctx, "other-purpose", once, live); err != nil {
		t.Fatalf("the same id under another purpose is a different token: %v", err)
	}

	raced := "raced-" + suffix
	var wins, replays, failures int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		handle := first
		if i%2 == 1 {
			handle = second
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := handle.RedeemOnce(ctx, "ticket", raced, live)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, store.ErrAlreadyRedeemed):
				replays++
			default:
				failures++
			}
		}()
	}
	wg.Wait()
	if wins != 1 || replays != 23 || failures != 0 {
		t.Fatalf("concurrent redemption: %d won, %d refused, %d failed; want 1, 23, 0", wins, replays, failures)
	}

	// An expired record is removed, by an explicit purge at the latest, and
	// a purge leaves live records alone. Reusing the id proves the record is
	// gone; real ids are random and never reused.
	stale := "stale-" + suffix
	if err := first.RedeemOnce(ctx, "ticket", stale, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("redeem with a past expiry: %v", err)
	}
	if err := second.PurgeExpiredSignInState(ctx); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if err := second.RedeemOnce(ctx, "ticket", stale, live); err != nil {
		t.Fatalf("the expired record was not purged: %v", err)
	}
	if err := first.RedeemOnce(ctx, "ticket", once, live); !errors.Is(err, store.ErrAlreadyRedeemed) {
		t.Fatalf("the purge removed a live record: %v", err)
	}
}
