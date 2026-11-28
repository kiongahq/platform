package httpapi

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kiongahq/platform/internal/keyring"
	"github.com/kiongahq/platform/internal/keyring/keyringtest"
	"github.com/kiongahq/platform/internal/store"
)

// originalSeal is the gateway's credential encryption before the keyring
// existed (hubCipher + sealHubToken), with a fixed nonce for the fixture.
func originalSeal(t *testing.T, encodedKey, aad, plaintext string) string {
	t.Helper()
	key, _ := base64.StdEncoding.DecodeString(encodedKey)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, _ := cipher.NewGCM(block)
	nonce := []byte("fixednonce12")
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(plaintext), []byte(aad)))
}

func useKeyringForTest(t *testing.T, ring *keyring.Ring) {
	t.Helper()
	UseKeyring(ring)
	t.Cleanup(func() { UseKeyring(nil) })
}

func openBaoRing(t *testing.T, stub *keyringtest.Bao) *keyring.Ring {
	t.Helper()
	t.Setenv("KIONGA_KEY_PROVIDER", "openbao")
	t.Setenv("OPENBAO_ADDR", stub.URL())
	t.Setenv("OPENBAO_TRANSIT_KEY", "kionga-credential")
	t.Setenv("OPENBAO_AUTH_ROLE", "kionga-gateway")
	t.Setenv("OPENBAO_AUTH_JWT_FILE", keyringtest.WriteJWT(t, stub.JWT))
	ring, err := keyring.FromEnv(context.Background(), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func TestRecordsWrittenBeforeTheKeyringStillOpen(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	t.Setenv("KIONGA_CREDENTIAL_KEY", key)
	repo := store.New(t.TempDir() + "/state.json")
	if err := repo.SaveHubAccount("alice", store.HubAccount{Username: "a", Ciphertext: originalSeal(t, key, "alice", "hf_before")}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: repo}
	if token, err := server.hubToken(context.Background(), "alice"); err != nil || token != "hf_before" {
		t.Fatalf("hugging face record: %q %v", token, err)
	}
	if err := server.saveGitHubAccount("alice", githubAccount{Login: "a", Ciphertext: originalSeal(t, key, "github:alice", "ghp_before")}); err != nil {
		t.Fatal(err)
	}
	if _, token, err := server.githubToken(context.Background(), "alice"); err != nil || token != "ghp_before" {
		t.Fatalf("github record: %q %v", token, err)
	}
	// The purposes stay separate: a GitHub record does not open as a
	// Hugging Face one for the same subject.
	if _, err := openCredential(context.Background(), hubCredentialAAD("alice"), originalSeal(t, key, "github:alice", "x")); err == nil {
		t.Fatal("associated data no longer separates purposes")
	}
}

func TestOpenBaoProviderSealsNewCredentialsInTransit(t *testing.T) {
	stub := keyringtest.New(t)
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	mockHub(t, func(*http.Request) (int, string) { return 200, `{"name":"alice-hf"}` }) // sets KIONGA_CREDENTIAL_KEY
	path := t.TempDir() + "/state.json"
	repo := store.New(path)
	// A record from before the switch.
	if err := repo.SaveHubAccount("bob", store.HubAccount{Username: "b", Ciphertext: originalSeal(t, key, "bob", "hf_bob")}); err != nil {
		t.Fatal(err)
	}
	useKeyringForTest(t, openBaoRing(t, stub))
	handler := New(repo, fstest.MapFS{})
	response := hubCall(handler, "alice", "PUT", "/api/v1/settings/huggingface", `{"token":"hf_alice"}`)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"configured":true`) {
		t.Fatalf("connect: %d %s", response.Code, response.Body)
	}
	account, _ := repo.HubAccount("alice")
	if !strings.HasPrefix(account.Ciphertext, "vault:v1:") {
		t.Fatalf("new record not sealed by transit: %q", account.Ciphertext)
	}
	disk, _ := os.ReadFile(path)
	if strings.Contains(string(disk), "hf_alice") {
		t.Fatal("plaintext on disk")
	}
	server := &Server{store: repo}
	if token, err := server.hubToken(context.Background(), "alice"); err != nil || token != "hf_alice" {
		t.Fatalf("transit record: %q %v", token, err)
	}
	if token, err := server.hubToken(context.Background(), "bob"); err != nil || token != "hf_bob" {
		t.Fatalf("pre-switch record after the switch: %q %v", token, err)
	}
	// Moving alice's transit record to bob does not open it.
	_ = repo.SaveHubAccount("bob", account)
	if _, err := server.hubToken(context.Background(), "bob"); err == nil || !strings.Contains(err.Error(), "reconnect") {
		t.Fatalf("transit record opened for another subject: %v", err)
	}
	// OpenBao failing is a retryable error, not "reconnect".
	stub.Set(func(s *keyringtest.Bao) { s.FailStatus["decrypt/kionga-credential"] = 503 })
	if _, err := server.hubToken(context.Background(), "alice"); err == nil || strings.Contains(err.Error(), "reconnect") {
		t.Fatalf("outage reported as a bad record: %v", err)
	}
}

func TestOpenBaoWithoutLegacyKeyAsksToReconnectStaticRecords(t *testing.T) {
	stub := keyringtest.New(t)
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	t.Setenv("KIONGA_CREDENTIAL_KEY", "")
	repo := store.New(t.TempDir() + "/state.json")
	server := &Server{store: repo}
	_ = server.saveGitHubAccount("carol", githubAccount{Login: "c", Ciphertext: originalSeal(t, key, "github:carol", "ghp_c")})
	useKeyringForTest(t, openBaoRing(t, stub))
	if _, _, err := server.githubToken(context.Background(), "carol"); err == nil || err.Error() != "Reconnect your GitHub account" {
		t.Fatalf("got %v", err)
	}
	if !credentialsConfigured() {
		t.Fatal("openbao ring reported unconfigured")
	}
}
