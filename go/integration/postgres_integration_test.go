//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/internal/store/storetest"
	"github.com/kiongahq/platform/pkg/api"
)

func TestPostgresPersistsResourceAuditAndOutboxAtomically(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	repository, err := store.OpenPostgres(context.Background(), databaseURL, "integration-test")
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Integration project"}, "test-user")
	if err != nil && err != store.ErrConflict {
		t.Fatal(err)
	}
	if project.ID != "" {
		if _, err := repository.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: project.ID, Name: "train"}, "test-user"); err != nil {
			t.Fatal(err)
		}
	}
	if len(repository.Audit()) == 0 {
		t.Fatal("expected durable audit event")
	}
	events, err := repository.PendingOutbox(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected transactional outbox events")
	}
}

func TestPostgresDocumentsConformance(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	repository, err := store.OpenPostgres(context.Background(), databaseURL, "integration-docs")
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	kind := fmt.Sprintf("conformance_%d", time.Now().UnixNano())
	storetest.Documents(t, repository, kind)
}

func TestPostgresPipelineRevisions(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	repository, err := store.OpenPostgres(context.Background(), databaseURL, fmt.Sprintf("revisions-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	project, err := repository.CreateProject(api.CreateProjectRequest{Name: "Revisions project"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	storetest.PipelineRevisions(t, repository, project.ID)
	storetest.ConcurrentStepReports(t, repository, project.ID)
	storetest.Logs(t, repository, fmt.Sprintf("run-logs-%d", time.Now().UnixNano()))
}

func TestPostgresIAMPolicies(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	repository, err := store.OpenPostgres(context.Background(), databaseURL, fmt.Sprintf("iam-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	storetest.IAM(t, repository)
}

func TestPostgresFeatureCatalogAndScaffoldJobs(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	repository, err := store.OpenPostgres(context.Background(), databaseURL, "integration-test")
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	storetest.FeatureCatalog(t, repository)
	storetest.ScaffoldJobs(t, repository)
}

func TestPostgresEditorialRevisionsMediaAndSchedule(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	repository, err := store.OpenPostgres(context.Background(), databaseURL, fmt.Sprintf("editorial-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	storetest.Editorial(t, repository)
}

func TestPostgresRevokesTokensWhenAccessIsWithdrawn(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	repository, err := store.OpenPostgres(context.Background(), databaseURL, fmt.Sprintf("tokens-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	storetest.TokenRevocation(t, repository, fmt.Sprint(time.Now().UnixNano()))
}

// Two connections stand for two gateway replicas on one database.
func TestPostgresSingleUseTokensAcrossReplicas(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	tenant := "sign-in-" + suffix
	first, err := store.OpenPostgres(context.Background(), databaseURL, tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := store.OpenPostgres(context.Background(), databaseURL, tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	storetest.SingleUseTokens(t, first, second, suffix)
	storetest.LocalSessions(t, first, second, suffix)

	// Another tenant on the same database shares nothing.
	other, err := store.OpenPostgres(context.Background(), databaseURL, tenant+"-other")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.RedeemOnce(context.Background(), "ticket", "once-"+suffix, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("another tenant was refused a token id used elsewhere: %v", err)
	}
}

// Two gateways, each with its own connection pool and session manager, on
// one database: a local session issued by one is accepted by the other, and
// signing out on one ends it on both.
func TestPostgresLocalSessionsAcrossGateways(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	tenant := fmt.Sprintf("local-sessions-%d", time.Now().UnixNano())
	gateways := make([]http.Handler, 2)
	for i := range gateways {
		repository, err := store.OpenPostgres(context.Background(), databaseURL, tenant)
		if err != nil {
			t.Fatal(err)
		}
		defer repository.Close()
		gateways[i] = auth.NewLocalSessionManager("admin", "correct-password", repository).Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	}
	call := func(gateway http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, request)
		return response
	}
	login := call(gateways[0], http.MethodPost, "/auth/local/login", "username=admin&password=correct-password", nil)
	if login.Code != http.StatusFound || len(login.Result().Cookies()) != 1 {
		t.Fatalf("sign-in: %d %v", login.Code, login.Result().Cookies())
	}
	cookie := login.Result().Cookies()[0]
	for i, gateway := range gateways {
		if response := call(gateway, http.MethodGet, "/api/v1/me", "", cookie); response.Code != http.StatusOK {
			t.Fatalf("gateway %d refused a session issued by gateway 0: %d", i, response.Code)
		}
	}
	forged := &http.Cookie{Name: cookie.Name, Value: cookie.Value[:len(cookie.Value)-1]}
	if response := call(gateways[1], http.MethodGet, "/api/v1/me", "", forged); response.Code != http.StatusUnauthorized {
		t.Fatalf("a tampered session was accepted: %d", response.Code)
	}
	if response := call(gateways[1], http.MethodGet, "/auth/logout", "", cookie); response.Code != http.StatusFound {
		t.Fatalf("sign-out: %d", response.Code)
	}
	for i, gateway := range gateways {
		if response := call(gateway, http.MethodGet, "/api/v1/me", "", cookie); response.Code != http.StatusUnauthorized {
			t.Fatalf("gateway %d accepted the session after sign-out on gateway 1: %d", i, response.Code)
		}
	}
}
