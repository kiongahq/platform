package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/store"
)

// replica is one gateway process: its own session manager (own memory, own
// throttling counters) over a store that may be shared with other replicas.
func replica(sessions LocalSessionStore, accounts ...LocalAccounts) (*LocalSessionManager, http.Handler) {
	manager := NewLocalSessionManager("admin", "correct-password", sessions, accounts...)
	return manager, manager.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principal, ok := PrincipalFrom(r.Context()); ok {
			w.Header().Set("X-Subject", principal.Subject)
		}
		w.WriteHeader(http.StatusOK)
	}))
}

func sessionCookie(t *testing.T, handler http.Handler, username, password string) *http.Cookie {
	t.Helper()
	response := postLogin(handler, username, password, nil)
	cookies := response.Result().Cookies()
	if response.Code != http.StatusFound || len(cookies) != 1 || cookies[0].Name != localSessionCookie || cookies[0].Value == "" {
		t.Fatalf("sign-in: %d %s cookies=%v", response.Code, response.Header().Get("Location"), cookies)
	}
	return cookies[0]
}

func apiAs(handler http.Handler, cookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func signOut(handler http.Handler, cookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/auth/logout", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// The defect: with two replicas, a session issued by one was unknown to the
// other, so every request the load balancer sent elsewhere was signed out.
func TestLocalSessionIsValidOnEveryReplica(t *testing.T) {
	shared := store.New()
	_, first := replica(shared)
	_, second := replica(shared)
	cookie := sessionCookie(t, first, "admin", "correct-password")
	for name, handler := range map[string]http.Handler{"issuing replica": first, "other replica": second} {
		if response := apiAs(handler, cookie); response.Code != http.StatusOK || response.Header().Get("X-Subject") != "admin" {
			t.Fatalf("%s: %d subject=%q", name, response.Code, response.Header().Get("X-Subject"))
		}
	}
	// Replicas that do not share a store share nothing.
	_, stranger := replica(store.New())
	if response := apiAs(stranger, cookie); response.Code != http.StatusUnauthorized {
		t.Fatalf("a gateway on another store accepted the session: %d", response.Code)
	}
}

func TestLocalSignOutOnOneReplicaRevokesTheSessionOnAll(t *testing.T) {
	shared := store.New()
	_, first := replica(shared)
	_, second := replica(shared)
	cookie := sessionCookie(t, first, "admin", "correct-password")
	kept := sessionCookie(t, second, "admin", "correct-password")
	if response := signOut(second, cookie); response.Code != http.StatusFound {
		t.Fatalf("sign-out on the other replica: %d", response.Code)
	}
	for name, handler := range map[string]http.Handler{"issuing replica": first, "replica that signed out": second} {
		if response := apiAs(handler, cookie); response.Code != http.StatusUnauthorized {
			t.Fatalf("%s still accepts the signed-out session: %d", name, response.Code)
		}
	}
	// Signing one session out leaves the subject's other session alone.
	if response := apiAs(first, kept); response.Code != http.StatusOK {
		t.Fatalf("another session of the same subject was revoked: %d", response.Code)
	}
}

func TestLocalSessionExpiresOnEveryReplica(t *testing.T) {
	shared := store.New()
	clock := time.Now()
	now := func() time.Time { return clock }
	firstManager, first := replica(shared)
	secondManager, second := replica(shared)
	firstManager.now, secondManager.now = now, now
	cookie := sessionCookie(t, first, "admin", "correct-password")
	if cookie.MaxAge != int((8 * time.Hour).Seconds()) {
		t.Fatalf("cookie lifetime = %ds, want 8 hours", cookie.MaxAge)
	}
	clock = clock.Add(8*time.Hour - time.Second)
	for name, handler := range map[string]http.Handler{"issuing replica": first, "other replica": second} {
		if response := apiAs(handler, cookie); response.Code != http.StatusOK {
			t.Fatalf("%s just before expiry: %d", name, response.Code)
		}
	}
	clock = clock.Add(2 * time.Second)
	for name, handler := range map[string]http.Handler{"issuing replica": first, "other replica": second} {
		if response := apiAs(handler, cookie); response.Code != http.StatusUnauthorized {
			t.Fatalf("%s after expiry: %d", name, response.Code)
		}
	}
}

func TestTamperedLocalSessionIsRefused(t *testing.T) {
	shared := store.New()
	_, first := replica(shared)
	_, second := replica(shared)
	cookie := sessionCookie(t, first, "admin", "correct-password")
	flipped := "A"
	if strings.HasSuffix(cookie.Value, "A") {
		flipped = "B"
	}
	for name, value := range map[string]string{
		"last character changed": cookie.Value[:len(cookie.Value)-1] + flipped,
		"truncated":              cookie.Value[:len(cookie.Value)-1],
		"extended":               cookie.Value + "A",
		"the stored hash":        localSessionKey(cookie.Value),
		"empty":                  "",
		"another subject's name": "admin",
	} {
		for replicaName, handler := range map[string]http.Handler{"issuing replica": first, "other replica": second} {
			forged := &http.Cookie{Name: localSessionCookie, Value: value}
			if response := apiAs(handler, forged); response.Code != http.StatusUnauthorized {
				t.Fatalf("%s accepted a session cookie with %s: %d", replicaName, name, response.Code)
			}
		}
	}
	if response := apiAs(second, cookie); response.Code != http.StatusOK {
		t.Fatalf("the untouched session stopped working: %d", response.Code)
	}
}

// The store never sees the cookie's token, only its hash.
func TestLocalSessionStoreHoldsOnlyTheTokenHash(t *testing.T) {
	recording := &recordingSessions{LocalSessionStore: store.New()}
	_, handler := replica(recording)
	cookie := sessionCookie(t, handler, "admin", "correct-password")
	apiAs(handler, cookie)
	signOut(handler, cookie)
	if len(recording.keys) != 3 {
		t.Fatalf("store calls = %d, want create, lookup, revoke", len(recording.keys))
	}
	for _, key := range recording.keys {
		if key == cookie.Value || strings.Contains(key, cookie.Value) || key != localSessionKey(cookie.Value) {
			t.Fatalf("the store was given %q for token %q", key, cookie.Value)
		}
	}
}

type recordingSessions struct {
	LocalSessionStore
	keys []string
}

func (r *recordingSessions) CreateLocalSession(ctx context.Context, key, subject string, expires time.Time) error {
	r.keys = append(r.keys, key)
	return r.LocalSessionStore.CreateLocalSession(ctx, key, subject, expires)
}

func (r *recordingSessions) LocalSession(ctx context.Context, key string) (string, time.Time, bool, error) {
	r.keys = append(r.keys, key)
	return r.LocalSessionStore.LocalSession(ctx, key)
}

func (r *recordingSessions) RevokeLocalSession(ctx context.Context, key string) error {
	r.keys = append(r.keys, key)
	return r.LocalSessionStore.RevokeLocalSession(ctx, key)
}

// downSessions is a shared store that stops answering.
type downSessions struct {
	LocalSessionStore
	down bool
}

var errStoreDown = errors.New("connection refused")

func (d *downSessions) CreateLocalSession(ctx context.Context, key, subject string, expires time.Time) error {
	if d.down {
		return errStoreDown
	}
	return d.LocalSessionStore.CreateLocalSession(ctx, key, subject, expires)
}

func (d *downSessions) LocalSession(ctx context.Context, key string) (string, time.Time, bool, error) {
	if d.down {
		return "", time.Time{}, false, errStoreDown
	}
	return d.LocalSessionStore.LocalSession(ctx, key)
}

func (d *downSessions) RevokeLocalSession(ctx context.Context, key string) error {
	if d.down {
		return errStoreDown
	}
	return d.LocalSessionStore.RevokeLocalSession(ctx, key)
}

func TestLocalSessionsFailClosedWhenTheStoreIsUnreachable(t *testing.T) {
	sessions := &downSessions{LocalSessionStore: store.New()}
	_, handler := replica(sessions)
	cookie := sessionCookie(t, handler, "admin", "correct-password")

	sessions.down = true
	if response := apiAs(handler, cookie); response.Code != http.StatusUnauthorized {
		t.Fatalf("a session was accepted without the store: %d", response.Code)
	}
	// A correct password yields no session the store did not record.
	if response := postLogin(handler, "admin", "correct-password", nil); response.Code != http.StatusServiceUnavailable || len(response.Result().Cookies()) != 0 {
		t.Fatalf("sign-in without the store: %d cookies=%v", response.Code, response.Result().Cookies())
	}
	// Sign-out does not claim success, and keeps the cookie so it can be retried.
	if response := signOut(handler, cookie); response.Code != http.StatusServiceUnavailable || len(response.Result().Cookies()) != 0 {
		t.Fatalf("sign-out without the store: %d cookies=%v", response.Code, response.Result().Cookies())
	}

	sessions.down = false
	if response := apiAs(handler, cookie); response.Code != http.StatusOK {
		t.Fatalf("session after the store came back: %d", response.Code)
	}
	if response := signOut(handler, cookie); response.Code != http.StatusFound {
		t.Fatalf("sign-out retry: %d", response.Code)
	}
	if response := apiAs(handler, cookie); response.Code != http.StatusUnauthorized {
		t.Fatalf("session after sign-out: %d", response.Code)
	}
}

// The same holds behind the Authenticator, which is what the gateway mounts.
func TestAuthenticatorLocalSessionsAreSharedAndSignOutFailsClosed(t *testing.T) {
	sessions := &downSessions{LocalSessionStore: store.New()}
	handlers := make([]http.Handler, 2)
	for i := range handlers {
		authenticator, err := NewAuthenticator(nil, nil, NewLocalSessionManager("admin", "correct-password", sessions), nil)
		if err != nil {
			t.Fatal(err)
		}
		handlers[i] = authenticator.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, _ := PrincipalFrom(r.Context())
			w.Header().Set("X-Credential", principal.Credential)
			w.WriteHeader(http.StatusOK)
		}))
	}
	cookie := sessionCookie(t, handlers[0], "admin", "correct-password")
	if response := apiAs(handlers[1], cookie); response.Code != http.StatusOK || response.Header().Get("X-Credential") != CredentialLocalSession {
		t.Fatalf("other replica: %d credential=%q", response.Code, response.Header().Get("X-Credential"))
	}
	sessions.down = true
	if response := signOut(handlers[1], cookie); response.Code != http.StatusServiceUnavailable || len(response.Result().Cookies()) != 0 {
		t.Fatalf("sign-out without the store: %d cookies=%v", response.Code, response.Result().Cookies())
	}
	sessions.down = false
	if response := signOut(handlers[1], cookie); response.Code != http.StatusFound {
		t.Fatalf("sign-out: %d", response.Code)
	}
	if response := apiAs(handlers[0], cookie); response.Code != http.StatusUnauthorized {
		t.Fatalf("issuing replica after sign-out elsewhere: %d", response.Code)
	}
}

// Removing a provisioned account's password ends its sessions on every
// replica, and the session record is dropped.
func TestRemovedLocalAccountLosesItsSessionOnEveryReplica(t *testing.T) {
	shared := store.New()
	accounts := &fakeAccounts{passwords: map[string]string{"dana": "dana-password-123"}}
	_, first := replica(shared, accounts)
	_, second := replica(shared, accounts)
	cookie := sessionCookie(t, first, "dana", "dana-password-123")
	if response := apiAs(second, cookie); response.Code != http.StatusOK || response.Header().Get("X-Subject") != "dana" {
		t.Fatalf("other replica: %d", response.Code)
	}
	delete(accounts.passwords, "dana")
	if response := apiAs(second, cookie); response.Code != http.StatusUnauthorized {
		t.Fatalf("removed account: %d", response.Code)
	}
	if _, _, found, _ := shared.LocalSession(context.Background(), localSessionKey(cookie.Value)); found {
		t.Fatal("the removed account's session record was kept")
	}
}

// Throttling is unchanged: counted per account in each replica, and a
// throttled attempt creates no session.
func TestLocalThrottlingStillAppliesWithASharedStore(t *testing.T) {
	recording := &recordingSessions{LocalSessionStore: store.New()}
	_, handler := replica(recording)
	for i := 0; i < loginFreeFailures; i++ {
		postLogin(handler, "admin", "wrong", nil)
	}
	response := postLogin(handler, "admin", "correct-password", nil)
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "error=throttled") || response.Header().Get("Retry-After") == "" || len(response.Result().Cookies()) != 0 {
		t.Fatalf("throttled sign-in: %d %s", response.Code, response.Header().Get("Location"))
	}
	if len(recording.keys) != 0 {
		t.Fatalf("failed and throttled sign-ins reached the session store %d times", len(recording.keys))
	}
}
