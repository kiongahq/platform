package storetest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/kiongahq/platform/internal/store"
)

// SealedCredentialRepository is what the credential rewrap command needs.
type SealedCredentialRepository interface {
	store.Repository
	store.SealedCredentialStore
}

func credentialID(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:])
}

// SealedCredentials checks listing with subject recovery from the audit
// trail, and the compare-and-swap rewrite that keeps other fields intact.
func SealedCredentials(t *testing.T, repo SealedCredentialRepository, suffix string) {
	t.Helper()
	alice, orphan := "alice-"+suffix, "orphan-"+suffix
	if err := repo.SaveHubAccount(alice, store.HubAccount{Username: "alice-hf", Ciphertext: "hub-before"}); err != nil {
		t.Fatal(err)
	}
	type gh struct {
		Login          string `json:"login"`
		Ciphertext     string `json:"ciphertext"`
		InstallationID int64  `json:"installation_id"`
	}
	if _, err := store.UpdateDoc(repo, store.GitHubAccountKind, credentialID(alice), func(gh, bool) (gh, error) {
		return gh{Login: "alice-gh", Ciphertext: "gh-before", InstallationID: 42}, nil
	}, "github.account.updated", alice); err != nil {
		t.Fatal(err)
	}
	// A record whose writer is not in the audit trail.
	if _, err := store.UpdateDoc(repo, store.GitHubAccountKind, credentialID(orphan), func(gh, bool) (gh, error) {
		return gh{Login: "o", Ciphertext: "orphan-before"}, nil
	}, "github.account.updated", "someone-else"); err != nil {
		t.Fatal(err)
	}
	// A disconnected account has no ciphertext and is not listed.
	if err := repo.SaveHubAccount("gone-"+suffix, store.HubAccount{}); err != nil {
		t.Fatal(err)
	}
	records, err := repo.SealedCredentials()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]store.SealedCredential{}
	for _, record := range records {
		found[record.Kind+"/"+record.ID] = record
	}
	if r := found[store.HubAccountKind+"/"+credentialID(alice)]; r.Subject != alice || r.Ciphertext != "hub-before" {
		t.Fatalf("hub record: %+v", r)
	}
	if r := found[store.GitHubAccountKind+"/"+credentialID(alice)]; r.Subject != alice || r.Ciphertext != "gh-before" {
		t.Fatalf("github record: %+v", r)
	}
	if r, ok := found[store.GitHubAccountKind+"/"+credentialID(orphan)]; !ok || r.Subject != "" {
		t.Fatalf("orphan record: %+v %v", r, ok)
	}
	if _, ok := found[store.HubAccountKind+"/"+credentialID("gone-"+suffix)]; ok {
		t.Fatal("disconnected account listed")
	}
	// Compare-and-swap.
	if err := repo.ReplaceSealedCredential(store.GitHubAccountKind, credentialID(alice), "stale", "gh-after"); !errors.Is(err, store.ErrCredentialChanged) {
		t.Fatalf("stale swap: %v", err)
	}
	if err := repo.ReplaceSealedCredential(store.HubAccountKind, credentialID("nobody-"+suffix), "x", "y"); !errors.Is(err, store.ErrCredentialChanged) {
		t.Fatalf("missing record: %v", err)
	}
	if err := repo.ReplaceSealedCredential(store.GitHubAccountKind, credentialID(alice), "gh-before", "gh-after"); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceSealedCredential(store.HubAccountKind, credentialID(alice), "hub-before", "hub-after"); err != nil {
		t.Fatal(err)
	}
	account, err := repo.HubAccount(alice)
	if err != nil || account.Ciphertext != "hub-after" || account.Username != "alice-hf" {
		t.Fatalf("hub after swap: %+v %v", account, err)
	}
	raw, err := repo.GetDocument(store.GitHubAccountKind, credentialID(alice))
	if err != nil {
		t.Fatal(err)
	}
	var after gh
	if err := json.Unmarshal(raw, &after); err != nil || after != (gh{Login: "alice-gh", Ciphertext: "gh-after", InstallationID: 42}) {
		t.Fatalf("github after swap: %+v %v", after, err)
	}
	// The rewrite is audited, and its actor never resolves as a subject.
	records, _ = repo.SealedCredentials()
	for _, record := range records {
		if record.Kind == store.HubAccountKind && record.ID == credentialID(alice) && record.Subject != alice {
			t.Fatalf("subject lost after rewrite: %+v", record)
		}
	}
	audited := false
	for _, event := range repo.Audit() {
		audited = audited || (event.Action == "credential.rewrapped" && event.Actor == store.RewrapActor)
	}
	if !audited {
		t.Fatal("rewrite not audited")
	}
}
