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

// LocalSessions checks the contract behind shared local sign-in sessions.
// first and second are two handles on the same shared state, as two gateway
// replicas hold: a session created through one is found through the other,
// revoking it through either removes it for both, and expired sessions are
// purged while live ones are kept.
func LocalSessions(t *testing.T, first, second store.SignInState, suffix string) {
	t.Helper()
	ctx := context.Background()
	expires := time.Now().Add(8 * time.Hour).UTC().Truncate(time.Second)

	key := "hash-" + suffix
	if _, _, found, err := second.LocalSession(ctx, key); err != nil || found {
		t.Fatalf("unknown session: found=%v err=%v", found, err)
	}
	if err := first.CreateLocalSession(ctx, key, "alice", expires); err != nil {
		t.Fatalf("create: %v", err)
	}
	for name, handle := range map[string]store.SignInState{"creating handle": first, "other handle": second} {
		subject, got, found, err := handle.LocalSession(ctx, key)
		if err != nil || !found || subject != "alice" || !got.Equal(expires) {
			t.Fatalf("%s: subject=%q expires=%s found=%v err=%v", name, subject, got, found, err)
		}
	}
	if _, _, found, _ := second.LocalSession(ctx, key+"x"); found {
		t.Fatal("a different key found the session")
	}

	kept := "kept-" + suffix
	if err := second.CreateLocalSession(ctx, kept, "alice", expires); err != nil {
		t.Fatalf("create second session: %v", err)
	}
	if err := second.RevokeLocalSession(ctx, key); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	for name, handle := range map[string]store.SignInState{"creating handle": first, "revoking handle": second} {
		if _, _, found, err := handle.LocalSession(ctx, key); err != nil || found {
			t.Fatalf("%s after revocation: found=%v err=%v", name, found, err)
		}
	}
	if _, _, found, _ := first.LocalSession(ctx, kept); !found {
		t.Fatal("revoking one session removed another")
	}
	if err := first.RevokeLocalSession(ctx, key); err != nil {
		t.Fatalf("revoking an unknown session must not fail: %v", err)
	}

	// Expired sessions are purged, by the next sign-in or an explicit purge.
	stale := "stale-" + suffix
	if err := first.CreateLocalSession(ctx, stale, "bob", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if err := second.PurgeExpiredSignInState(ctx); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, _, found, _ := first.LocalSession(ctx, stale); found {
		t.Fatal("the expired session was not purged")
	}
	stale2 := "stale2-" + suffix
	if err := first.CreateLocalSession(ctx, stale2, "bob", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if err := second.CreateLocalSession(ctx, "fresh-"+suffix, "bob", expires); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, found, _ := first.LocalSession(ctx, stale2); found {
		t.Fatal("a new sign-in did not purge the expired session")
	}
	if _, _, found, _ := second.LocalSession(ctx, kept); !found {
		t.Fatal("the purge removed a live session")
	}
}
