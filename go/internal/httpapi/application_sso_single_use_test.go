package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/store"
)

// ticketFor signs alice in on the console and returns the application-host
// URL that carries her ticket.
func (h *ssoHarness) ticketFor(t *testing.T) string {
	t.Helper()
	handoff := h.get("https://console.example.com/applications/billing/sso?next=%2Fdashboard", person("alice", "prj-demo"))
	location, err := url.Parse(handoff.Header().Get("Location"))
	if err != nil || handoff.Code != http.StatusFound || location.Query().Get("ticket") == "" {
		t.Fatalf("handoff: %d %s", handoff.Code, handoff.Header().Get("Location"))
	}
	return "http://billing.apps.example.com" + location.RequestURI()
}

func redeem(router http.Handler, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

func assertTicketRefused(t *testing.T, what string, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusUnauthorized || strings.TrimSpace(response.Body.String()) != appTicketRefused || len(response.Result().Cookies()) != 0 {
		t.Fatalf("%s: %d %q cookies=%v, want 401 with the constant refusal and no session", what, response.Code, response.Body.String(), response.Result().Cookies())
	}
}

func TestApplicationTicketWorksOnce(t *testing.T) {
	h := newSSOHarness(t)
	target := h.ticketFor(t)
	first := redeem(h.router, target)
	if first.Code != http.StatusSeeOther || len(first.Result().Cookies()) != 1 {
		t.Fatalf("first redemption: %d %v", first.Code, first.Result().Cookies())
	}
	assertTicketRefused(t, "replay", redeem(h.router, target))
	assertTicketRefused(t, "second replay", redeem(h.router, target))

	// The refusal sends nobody anywhere: opening the application again
	// starts a new sign-in, and a new ticket works.
	if again := redeem(h.router, "http://billing.apps.example.com/dashboard"); again.Code != http.StatusFound || !strings.HasPrefix(again.Header().Get("Location"), "https://console.example.com/applications/billing/sso") {
		t.Fatalf("after a refused replay: %d %s", again.Code, again.Header().Get("Location"))
	}
	if fresh := redeem(h.router, h.ticketFor(t)); fresh.Code != http.StatusSeeOther {
		t.Fatalf("a new ticket: %d", fresh.Code)
	}
}

// Two gateways over one store, as two replicas over one database: a ticket
// redeemed on either is refused by the other.
func TestApplicationTicketReplayIsRefusedOnAnotherGateway(t *testing.T) {
	h := newSSOHarness(t)
	other := ApplicationHostRouter(http.NotFoundHandler(), h.repo)
	for name, order := range map[string][2]http.Handler{"first then second": {h.router, other}, "second then first": {other, h.router}} {
		target := h.ticketFor(t)
		if first := redeem(order[0], target); first.Code != http.StatusSeeOther {
			t.Fatalf("%s: first redemption: %d %s", name, first.Code, first.Body.String())
		}
		assertTicketRefused(t, name+": replay on the other gateway", redeem(order[1], target))
	}
}

func TestApplicationTicketConcurrentRedemptionsYieldOneSession(t *testing.T) {
	h := newSSOHarness(t)
	other := ApplicationHostRouter(http.NotFoundHandler(), h.repo)
	target := h.ticketFor(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	sessions := 0
	for i := 0; i < 16; i++ {
		router := h.router
		if i%2 == 1 {
			router = other
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := redeem(router, target)
			mu.Lock()
			defer mu.Unlock()
			sessions += len(response.Result().Cookies())
		}()
	}
	wg.Wait()
	if sessions != 1 {
		t.Fatalf("one ticket produced %d sessions", sessions)
	}
}

// failingSignInState is a store whose sign-in state cannot be reached.
type failingSignInState struct {
	store.Repository
	down  bool
	calls int
}

func (f *failingSignInState) RedeemOnce(ctx context.Context, purpose, id string, expires time.Time) error {
	f.calls++
	if f.down {
		return errors.New("connection refused")
	}
	return f.Repository.RedeemOnce(ctx, purpose, id, expires)
}

func TestApplicationTicketIsRefusedWhenTheStoreIsUnreachable(t *testing.T) {
	h := newSSOHarness(t)
	failing := &failingSignInState{Repository: h.repo, down: true}
	router := ApplicationHostRouter(http.NotFoundHandler(), failing)
	target := h.ticketFor(t)
	refused := redeem(router, target)
	if refused.Code != http.StatusServiceUnavailable || len(refused.Result().Cookies()) != 0 || refused.Header().Get("Location") != "" {
		t.Fatalf("store down: %d cookies=%v location=%q, want 503 and no session", refused.Code, refused.Result().Cookies(), refused.Header().Get("Location"))
	}
	// The ticket was not recorded, so it still works once the store is back.
	failing.down = false
	if recovered := redeem(router, target); recovered.Code != http.StatusSeeOther {
		t.Fatalf("store back: %d", recovered.Code)
	}
	assertTicketRefused(t, "replay after recovery", redeem(router, target))
}

func TestApplicationTicketRefusalsRecordNothing(t *testing.T) {
	h := newSSOHarness(t)
	counting := &failingSignInState{Repository: h.repo}
	router := ApplicationHostRouter(http.NotFoundHandler(), counting)
	now := time.Now()
	valid := appToken{Kind: "ticket", ID: "id-1", App: "billing", Subject: "alice", Next: "/", Expires: now.Add(appTicketTTL).Unix()}
	expired := valid
	expired.Expires = now.Add(-time.Second).Unix()
	withoutID := valid
	withoutID.ID = ""
	otherApp := valid
	otherApp.App = "docs"
	session := valid
	session.Kind = "session"
	forged := signAppToken(valid)
	forged = forged[:len(forged)-2] + "xx"
	for name, raw := range map[string]string{
		"expired":            signAppToken(expired),
		"without an id":      signAppToken(withoutID),
		"for another app":    signAppToken(otherApp),
		"a session token":    signAppToken(session),
		"a forged signature": forged,
		"not a token":        "nonsense",
		"empty":              "",
	} {
		response := redeem(router, "http://billing.apps.example.com/_kionga/sso?ticket="+url.QueryEscape(raw))
		assertTicketRefused(t, name, response)
	}
	if counting.calls != 0 {
		t.Fatalf("refused tickets reached the store %d times; only a verified ticket with an id may be recorded", counting.calls)
	}
	// The valid ticket was never presented above and still works.
	if ok := redeem(router, "http://billing.apps.example.com/_kionga/sso?ticket="+url.QueryEscape(signAppToken(valid))); ok.Code != http.StatusSeeOther || counting.calls != 1 {
		t.Fatalf("valid ticket: %d after %d store calls", ok.Code, counting.calls)
	}
}

// The redemption record must outlive the ticket, or a replay could follow a
// purge; and it must be scoped to the application.
func TestApplicationTicketRecordOutlivesTheTicket(t *testing.T) {
	h := newSSOHarness(t)
	recording := &recordingSignInState{Repository: h.repo}
	router := ApplicationHostRouter(http.NotFoundHandler(), recording)
	expires := time.Now().Add(appTicketTTL)
	ticket := appToken{Kind: "ticket", ID: "id-2", App: "billing", Subject: "alice", Next: "/", Expires: expires.Unix()}
	if ok := redeem(router, "http://billing.apps.example.com/_kionga/sso?ticket="+url.QueryEscape(signAppToken(ticket))); ok.Code != http.StatusSeeOther {
		t.Fatalf("redeem: %d", ok.Code)
	}
	if recording.purpose != appTicketPurpose || recording.id != "billing/id-2" {
		t.Fatalf("recorded %q %q", recording.purpose, recording.id)
	}
	if want := time.Unix(expires.Unix(), 0).Add(appTicketRecordMargin); !recording.expires.Equal(want) || !recording.expires.After(expires) {
		t.Fatalf("record expires %s, want %s (after the ticket's %s)", recording.expires, want, expires)
	}
}

type recordingSignInState struct {
	store.Repository
	purpose, id string
	expires     time.Time
}

func (r *recordingSignInState) RedeemOnce(ctx context.Context, purpose, id string, expires time.Time) error {
	r.purpose, r.id, r.expires = purpose, id, expires
	return r.Repository.RedeemOnce(ctx, purpose, id, expires)
}
