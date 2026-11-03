package storetest

import (
	"testing"

	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// TokenRevocation checks that suspending or removing a user revokes every
// API token they hold, with an api_token.revoked audit event per token, and
// leaves other users' tokens alone.
func TokenRevocation(t *testing.T, repo store.Repository, suffix string) {
	t.Helper()
	suspended, removed, other := "suspended-"+suffix, "removed-"+suffix, "other-"+suffix
	grant := api.UpsertUserAccessRequest{Email: "user@example.com", Role: "user", Services: []string{"projects"}}
	tokens := map[string][]string{}
	for _, subject := range []string{suspended, removed, other} {
		if _, err := repo.UpsertUserAccess(subject, grant, "admin"); err != nil {
			t.Fatalf("grant %s: %v", subject, err)
		}
		for _, name := range []string{"laptop", "ci-runner"} {
			created, err := repo.CreateAPIToken(subject, api.CreateAPITokenRequest{Name: name, Services: []string{"projects"}, ExpiresInDays: 30})
			if err != nil {
				t.Fatalf("create token for %s: %v", subject, err)
			}
			tokens[subject] = append(tokens[subject], created.Secret)
		}
	}
	auditBefore := len(repo.Audit())
	disabled := grant
	disabled.Disabled = true
	if _, err := repo.UpsertUserAccess(suspended, disabled, "admin"); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if err := repo.DeleteUserAccess(removed, "admin"); err != nil {
		t.Fatalf("delete access: %v", err)
	}
	for _, subject := range []string{suspended, removed} {
		for _, secret := range tokens[subject] {
			if _, err := repo.ResolveAPIToken(secret); err == nil {
				t.Errorf("token of %s still resolves after access was withdrawn", subject)
			}
		}
		for _, token := range repo.APITokensFor(subject) {
			if token.RevokedAt == nil {
				t.Errorf("token %s of %s was not marked revoked", token.ID, subject)
			}
		}
	}
	for _, secret := range tokens[other] {
		if _, err := repo.ResolveAPIToken(secret); err != nil {
			t.Errorf("an unrelated user's token was revoked: %v", err)
		}
	}
	revocations := 0
	for _, event := range repo.Audit()[:len(repo.Audit())-auditBefore] {
		if event.Action == "api_token.revoked" && event.Actor == "admin" {
			revocations++
		}
	}
	if revocations != 4 {
		t.Errorf("api_token.revoked audit events = %d, want 4", revocations)
	}
}
