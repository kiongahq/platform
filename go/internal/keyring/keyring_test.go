package keyring

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/keyring/keyringtest"
)

const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes

// legacySeal and legacyOpen are the gateway's credential encryption exactly
// as it was before this package (httpapi hubCipher, sealHubToken and
// openGitHubSecret), kept here as the compatibility oracle.
func legacyAEAD(t *testing.T) cipher.AEAD {
	key, _ := base64.StdEncoding.DecodeString(testKey)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, _ := cipher.NewGCM(block)
	return aead
}

func legacySeal(t *testing.T, nonce []byte, plaintext, aad string) string {
	aead := legacyAEAD(t)
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(plaintext), []byte(aad)))
}

func legacyOpen(t *testing.T, encrypted, aad string) (string, error) {
	aead := legacyAEAD(t)
	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil || len(data) < aead.NonceSize() {
		return "", errors.New("bad")
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(aad))
	return string(plain), err
}

func TestStaticIsByteCompatibleWithTheOriginalEncryption(t *testing.T) {
	static, err := NewStatic(testKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// A record written by the old code opens.
	old := legacySeal(t, []byte("0123456789ab"), "hf_secret", "alice")
	plain, err := static.Open(ctx, []byte(old), []byte("alice"))
	if err != nil || string(plain) != "hf_secret" {
		t.Fatalf("legacy record did not open: %q %v", plain, err)
	}
	// A record written now opens with the old code, and re-sealing its nonce
	// with the old code yields the identical bytes.
	sealed, err := static.Seal(ctx, []byte("ghp_token"), []byte("github:alice"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := legacyOpen(t, string(sealed), "github:alice"); err != nil || got != "ghp_token" {
		t.Fatalf("old code cannot open new record: %q %v", got, err)
	}
	raw, _ := base64.StdEncoding.DecodeString(string(sealed))
	if again := legacySeal(t, raw[:12], "ghp_token", "github:alice"); again != string(sealed) {
		t.Fatalf("format changed:\n new %s\n old %s", sealed, again)
	}
	// Associated data is bound.
	if _, err := static.Open(ctx, sealed, []byte("github:bob")); !errors.Is(err, ErrOpen) {
		t.Fatalf("other subject opened the record: %v", err)
	}
	for _, bad := range []string{"", "%%%", "AAAA"} {
		if _, err := static.Open(ctx, []byte(bad), nil); !errors.Is(err, ErrOpen) {
			t.Fatalf("malformed %q: %v", bad, err)
		}
	}
	for _, key := range []string{"", "short", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := NewStatic(key); !errors.Is(err, ErrStaticKey) {
			t.Fatalf("key %q accepted: %v", key, err)
		}
	}
}

func TestFormat(t *testing.T) {
	cases := map[string]string{"": "empty", "vault:v1:abc": "openbao:v1", "vault:v12:abc": "openbao:v12", "AbC+/=": "static", "vault:v0:x": "static", "vault:vx:": "static"}
	for in, want := range cases {
		if got := Format([]byte(in)); got != want {
			t.Errorf("Format(%q) = %q, want %q", in, got, want)
		}
	}
	if TransitVersion([]byte("vault:v7:abc")) != 7 || TransitVersion([]byte("abc")) != 0 {
		t.Fatal("TransitVersion")
	}
}

func TestOpenBaoRoundTripBindsContext(t *testing.T) {
	stub := keyringtest.New(t)
	client := stubClient(t, stub)
	ctx := context.Background()
	sealed, err := client.Seal(ctx, []byte("hf_private"), []byte("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(sealed), "vault:v1:") || bytes.Contains(sealed, []byte("hf_private")) {
		t.Fatalf("unexpected ciphertext %q", sealed)
	}
	if body := stub.LastBody("/v1/transit/encrypt/kionga-credential"); !strings.Contains(body, base64.StdEncoding.EncodeToString([]byte("alice"))) {
		t.Fatalf("associated data was not sent as context: %s", body)
	}
	plain, err := client.Open(ctx, sealed, []byte("alice"))
	if err != nil || string(plain) != "hf_private" {
		t.Fatalf("open: %q %v", plain, err)
	}
	if _, err := client.Open(ctx, sealed, []byte("bob")); !errors.Is(err, ErrOpen) {
		t.Fatalf("wrong context opened: %v", err)
	}
	if _, err := client.Seal(ctx, []byte("x"), nil); err == nil {
		t.Fatal("sealing without associated data must fail")
	}
	if _, err := client.Open(ctx, []byte("bm90LXRyYW5zaXQ="), []byte("alice")); !errors.Is(err, ErrOpen) {
		t.Fatalf("static value sent to transit: %v", err)
	}
	if logins, _, _, _ := stub.Counts(); logins != 1 {
		t.Fatalf("token was not reused: %d logins", logins)
	}
}

func TestOpenBaoRenewsBeforeExpiryAndLogsInAgainWhenRenewalFails(t *testing.T) {
	stub := keyringtest.New(t)
	client := stubClient(t, stub)
	now := time.Unix(1_000_000, 0)
	client.now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := client.Seal(ctx, []byte("a"), []byte("s")); err != nil {
		t.Fatal(err)
	}
	// Before two thirds of the hour-long lease: cached token, no renewal.
	now = now.Add(39 * time.Minute)
	if _, err := client.Seal(ctx, []byte("a"), []byte("s")); err != nil {
		t.Fatal(err)
	}
	if logins, renews, _, _ := stub.Counts(); logins != 1 || renews != 0 {
		t.Fatalf("early renewal: logins %d renews %d", logins, renews)
	}
	// Past the renewal point and before expiry: renew-self, same login.
	now = now.Add(2 * time.Minute)
	if _, err := client.Seal(ctx, []byte("a"), []byte("s")); err != nil {
		t.Fatal(err)
	}
	if logins, renews, _, _ := stub.Counts(); logins != 1 || renews != 1 {
		t.Fatalf("expected one renewal: logins %d renews %d", logins, renews)
	}
	// Renewal refused: fall back to a fresh login.
	stub.Set(func(s *keyringtest.Bao) { s.FailRenew = true })
	now = now.Add(41 * time.Minute)
	if _, err := client.Seal(ctx, []byte("a"), []byte("s")); err != nil {
		t.Fatal(err)
	}
	if logins, renews, _, _ := stub.Counts(); logins != 2 || renews != 2 {
		t.Fatalf("expected re-login after failed renewal: logins %d renews %d", logins, renews)
	}
	// Expired outright: login, no renewal attempt.
	now = now.Add(2 * time.Hour)
	if _, err := client.Seal(ctx, []byte("a"), []byte("s")); err != nil {
		t.Fatal(err)
	}
	if logins, renews, _, _ := stub.Counts(); logins != 3 || renews != 2 {
		t.Fatalf("expected login after expiry: logins %d renews %d", logins, renews)
	}
}

func TestOpenBaoBackgroundRenewal(t *testing.T) {
	stub := keyringtest.New(t)
	stub.Set(func(s *keyringtest.Bao) { s.Lease = 2 }) // renew after ~1.3 s
	client := stubClient(t, stub)
	if _, err := client.ensureToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { client.RunRenewal(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, renews, _, _ := stub.Counts(); renews > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if logins, renews, _, _ := stub.Counts(); renews == 0 || logins != 1 {
		t.Fatalf("background renewal did not run: logins %d renews %d", logins, renews)
	}
}

func TestOpenBaoLogsInAgainOnceWhenTokenIsRevoked(t *testing.T) {
	stub := keyringtest.New(t)
	client := stubClient(t, stub)
	ctx := context.Background()
	sealed, err := client.Seal(ctx, []byte("v"), []byte("s"))
	if err != nil {
		t.Fatal(err)
	}
	stub.Set(func(s *keyringtest.Bao) { s.RevokeNext = true })
	if _, err := client.Open(ctx, sealed, []byte("s")); err != nil {
		t.Fatalf("open after revocation: %v", err)
	}
	if logins, _, _, _ := stub.Counts(); logins != 2 {
		t.Fatalf("expected one re-login, got %d logins", logins)
	}
	// A policy that keeps refusing is an error after one retry, not a loop.
	stub.Set(func(s *keyringtest.Bao) { s.FailStatus["encrypt/kionga-credential"] = 403 })
	if _, err := client.Seal(ctx, []byte("v"), []byte("s")); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("expected 403 error, got %v", err)
	}
	if logins, _, _, _ := stub.Counts(); logins != 3 {
		t.Fatalf("expected exactly one more login, got %d", logins)
	}
}

func TestOpenBaoErrorsNeverCarrySecrets(t *testing.T) {
	stub := keyringtest.New(t)
	stub.Set(func(s *keyringtest.Bao) { s.FailStatus["/login"] = 500 }) // the stub echoes the jwt into its error
	client := stubClient(t, stub)
	_, err := client.Seal(context.Background(), []byte("hf_plaintext_value"), []byte("s"))
	if err == nil || strings.Contains(err.Error(), stub.JWT) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("login error leaked or lost: %v", err)
	}
	stub.Set(func(s *keyringtest.Bao) {
		delete(s.FailStatus, "/login")
		s.FailStatus["encrypt/kionga-credential"] = 500
	})
	_, err = client.Seal(context.Background(), []byte("hf_plaintext_value"), []byte("s"))
	if err == nil || strings.Contains(err.Error(), "hf_plaintext_value") || strings.Contains(err.Error(), "s.token") {
		t.Fatalf("transit error leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("status missing: %v", err)
	}
}

func TestOpenBaoLoginFailures(t *testing.T) {
	stub := keyringtest.New(t)
	cfg := stubConfig(t, stub)
	cfg.AuthRole = "other-role"
	client, err := NewOpenBao(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Seal(context.Background(), []byte("v"), []byte("s")); err == nil || !strings.Contains(err.Error(), "login") {
		t.Fatalf("wrong role: %v", err)
	}
	cfg = stubConfig(t, stub)
	cfg.JWTFile = filepath.Join(t.TempDir(), "missing")
	client, _ = NewOpenBao(cfg)
	if _, err := client.Seal(context.Background(), []byte("v"), []byte("s")); err == nil || !strings.Contains(err.Error(), "service-account token") {
		t.Fatalf("missing jwt file: %v", err)
	}
}

func TestOpenBaoTimeoutsAreBounded(t *testing.T) {
	stub := keyringtest.New(t)
	stub.Set(func(s *keyringtest.Bao) { s.Delay["encrypt/kionga-credential"] = 3 * time.Second })
	cfg := stubConfig(t, stub)
	cfg.Timeout = 300 * time.Millisecond
	client, _ := NewOpenBao(cfg)
	start := time.Now()
	_, err := client.Seal(context.Background(), []byte("v"), []byte("s"))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout not bounded: %v", elapsed)
	}
}

func TestOpenBaoUnreachableFailsClosed(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	addr := dead.URL
	dead.Close()
	t.Setenv("KIONGA_KEY_PROVIDER", "openbao")
	t.Setenv("OPENBAO_ADDR", addr)
	t.Setenv("OPENBAO_TRANSIT_KEY", "kionga-credential")
	t.Setenv("OPENBAO_AUTH_ROLE", "kionga-gateway")
	t.Setenv("OPENBAO_AUTH_JWT_FILE", keyringtest.WriteJWT(t, "jwt"))
	t.Setenv("KIONGA_CREDENTIAL_KEY", testKey)
	ring, err := FromEnv(context.Background(), os.Getenv)
	if err == nil || ring != nil {
		t.Fatalf("openbao provider started without OpenBao: %v", err)
	}
	if !strings.Contains(err.Error(), "could not be reached") {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestCheckRefusesKeyThatIgnoresContext(t *testing.T) {
	stub := keyringtest.New(t)
	stub.Set(func(s *keyringtest.Bao) { s.Derived = false })
	err := stubClient(t, stub).Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "derived=true") {
		t.Fatalf("non-derived key accepted: %v", err)
	}
	stub.Set(func(s *keyringtest.Bao) { s.Derived = true })
	if err := stubClient(t, stub).Check(context.Background()); err != nil {
		t.Fatalf("derived key refused: %v", err)
	}
}

func TestNewOpenBaoValidation(t *testing.T) {
	ok := OpenBaoConfig{Addr: "https://bao.example:8200", TransitKey: "k", AuthRole: "r"}
	if _, err := NewOpenBao(ok); err != nil {
		t.Fatal(err)
	}
	bad := []OpenBaoConfig{
		{Addr: "http://bao.example:8200", TransitKey: "k", AuthRole: "r"},
		{Addr: "https://user:pw@bao.example", TransitKey: "k", AuthRole: "r"},
		{Addr: "https://bao.example/v1", TransitKey: "k", AuthRole: "r"},
		{Addr: "https://bao.example", TransitKey: "../k", AuthRole: "r"},
		{Addr: "https://bao.example", TransitKey: "k"},
		{Addr: "https://bao.example", TransitKey: "k", AuthRole: "r", TransitMount: "a/../b"},
		{Addr: "https://bao.example", TransitKey: "k", AuthRole: "r", CACertFile: "/nonexistent"},
	}
	for _, cfg := range bad {
		if _, err := NewOpenBao(cfg); err == nil {
			t.Errorf("accepted %+v", cfg)
		}
	}
	notPEM := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(notPEM, []byte("nope"), 0o600)
	if _, err := NewOpenBao(OpenBaoConfig{Addr: "https://bao.example", TransitKey: "k", AuthRole: "r", CACertFile: notPEM}); err == nil {
		t.Fatal("accepted a CA file with no certificate")
	}
}

func TestOpenBaoTrustsOnlyTheConfiguredCA(t *testing.T) {
	stub := keyringtest.New(t)
	tlsServer := httptest.NewTLSServer(stub.Server.Config.Handler)
	defer tlsServer.Close()
	cfg := stubConfig(t, stub)
	cfg.Addr, cfg.AllowHTTP = tlsServer.URL, false
	client, _ := NewOpenBao(cfg)
	if _, err := client.Seal(context.Background(), []byte("v"), []byte("s")); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, keyringtest.PEM(tlsServer), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.CACertFile = caFile
	client, err := NewOpenBao(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Seal(context.Background(), []byte("v"), []byte("s")); err != nil {
		t.Fatalf("configured CA refused: %v", err)
	}
}

func openBaoEnv(t *testing.T, stub *keyringtest.Bao) {
	t.Setenv("KIONGA_KEY_PROVIDER", "openbao")
	t.Setenv("OPENBAO_ADDR", stub.URL())
	t.Setenv("OPENBAO_TRANSIT_KEY", "kionga-credential")
	t.Setenv("OPENBAO_AUTH_ROLE", "kionga-gateway")
	t.Setenv("OPENBAO_AUTH_JWT_FILE", keyringtest.WriteJWT(t, stub.JWT))
}

func TestRingAcrossProviders(t *testing.T) {
	ctx := context.Background()
	stub := keyringtest.New(t)

	// A static install writes records.
	t.Setenv("KIONGA_CREDENTIAL_KEY", testKey)
	staticRing, err := FromEnv(ctx, os.Getenv)
	if err != nil || staticRing.Provider() != ProviderStatic || !staticRing.Configured() {
		t.Fatalf("static ring: %v", err)
	}
	old, err := staticRing.Seal(ctx, []byte("hf_old"), []byte("alice"))
	if err != nil || Format(old) != "static" {
		t.Fatalf("static seal: %q %v", old, err)
	}

	// Switch to openbao, keeping the credential key for legacy reads.
	openBaoEnv(t, stub)
	ring, err := FromEnv(ctx, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := ring.Open(ctx, old, []byte("alice")); err != nil || string(plain) != "hf_old" {
		t.Fatalf("old static record did not open after the switch: %q %v", plain, err)
	}
	fresh, err := ring.Seal(ctx, []byte("hf_new"), []byte("alice"))
	if err != nil || Format(fresh) != "openbao:v1" {
		t.Fatalf("openbao ring sealed %q (%v); writes must use transit even with the credential key present", fresh, err)
	}
	if ring.Current(old) || !ring.Current(fresh) {
		t.Fatal("Current misreports formats")
	}

	// End state: the credential key is gone from the gateway.
	t.Setenv("KIONGA_CREDENTIAL_KEY", "")
	ring, err = FromEnv(ctx, os.Getenv)
	if err != nil {
		t.Fatalf("openbao without the credential key: %v", err)
	}
	if ring.HasStatic() {
		t.Fatal("ring holds a static key it was not given")
	}
	if plain, err := ring.Open(ctx, fresh, []byte("alice")); err != nil || string(plain) != "hf_new" {
		t.Fatalf("transit record: %q %v", plain, err)
	}
	if _, err := ring.Open(ctx, old, []byte("alice")); !errors.Is(err, ErrStaticUnavailable) {
		t.Fatalf("expected ErrStaticUnavailable, got %v", err)
	}

	// Rollback: a static gateway with OpenBao still configured reads transit.
	t.Setenv("KIONGA_KEY_PROVIDER", "static")
	t.Setenv("KIONGA_CREDENTIAL_KEY", testKey)
	back, err := FromEnv(ctx, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := back.Open(ctx, fresh, []byte("alice")); err != nil || string(plain) != "hf_new" {
		t.Fatalf("rollback read: %q %v", plain, err)
	}
	if sealed, _ := back.Seal(ctx, []byte("x"), []byte("alice")); Format(sealed) != "static" {
		t.Fatal("static ring sealed with transit")
	}

	// Static without OpenBao cannot read transit records.
	for _, name := range []string{"OPENBAO_ADDR", "OPENBAO_TRANSIT_KEY", "OPENBAO_AUTH_ROLE"} {
		t.Setenv(name, "")
	}
	plain, err := FromEnv(ctx, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Open(ctx, fresh, []byte("alice")); !errors.Is(err, ErrTransitUnavailable) {
		t.Fatalf("expected ErrTransitUnavailable, got %v", err)
	}
}

func TestFromEnvProviderRules(t *testing.T) {
	ctx := context.Background()
	t.Setenv("KIONGA_KEY_PROVIDER", "vault")
	if _, err := FromEnv(ctx, os.Getenv); err == nil {
		t.Fatal("unknown provider accepted")
	}
	// Static with no key keeps today's behaviour: the gateway runs and
	// account connections report unconfigured.
	t.Setenv("KIONGA_KEY_PROVIDER", "")
	t.Setenv("KIONGA_CREDENTIAL_KEY", "")
	ring, err := FromEnv(ctx, os.Getenv)
	if err != nil || ring.Configured() {
		t.Fatalf("static without key: configured=%v err=%v", ring != nil && ring.Configured(), err)
	}
	if _, err := ring.Seal(ctx, []byte("x"), []byte("a")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("seal without key: %v", err)
	}
	if _, err := ring.Open(ctx, []byte("AAAA"), []byte("a")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("open without key: %v", err)
	}
	// openbao with a malformed legacy key is a configuration error.
	stub := keyringtest.New(t)
	openBaoEnv(t, stub)
	t.Setenv("KIONGA_CREDENTIAL_KEY", "not-a-key")
	if _, err := FromEnv(ctx, os.Getenv); !errors.Is(err, ErrStaticKey) {
		t.Fatalf("malformed legacy key accepted: %v", err)
	}
}

// TestOpenBaoSealingNeverReadsTheCredentialKey records every variable FromEnv
// and the ring read while an openbao gateway seals and opens a transit
// record, with no credential key in the environment.
func TestOpenBaoSealingNeverReadsTheCredentialKey(t *testing.T) {
	stub := keyringtest.New(t)
	jwt := keyringtest.WriteJWT(t, stub.JWT)
	env := map[string]string{"KIONGA_KEY_PROVIDER": "openbao", "OPENBAO_ADDR": stub.URL(), "OPENBAO_TRANSIT_KEY": "kionga-credential", "OPENBAO_AUTH_ROLE": "kionga-gateway", "OPENBAO_AUTH_JWT_FILE": jwt}
	seen := map[string]string{}
	getenv := func(name string) string { seen[name] = env[name]; return env[name] }
	ring, err := FromEnv(context.Background(), getenv)
	if err != nil {
		t.Fatal(err)
	}
	if value := seen["KIONGA_CREDENTIAL_KEY"]; value != "" {
		t.Fatalf("credential key value reached the keyring: %q", value)
	}
	sealed, err := ring.Seal(context.Background(), []byte("v"), []byte("s"))
	if err != nil || !IsTransit(sealed) {
		t.Fatalf("seal: %q %v", sealed, err)
	}
	if ring.HasStatic() {
		t.Fatal("static key present")
	}
}

func stubConfig(t *testing.T, stub *keyringtest.Bao) OpenBaoConfig {
	return OpenBaoConfig{Addr: stub.URL(), TransitKey: "kionga-credential", AuthRole: "kionga-gateway", JWTFile: keyringtest.WriteJWT(t, stub.JWT), AllowHTTP: true, Timeout: 2 * time.Second}
}

func stubClient(t *testing.T, stub *keyringtest.Bao) *OpenBao {
	t.Helper()
	client, err := NewOpenBao(stubConfig(t, stub))
	if err != nil {
		t.Fatal(err)
	}
	return client
}
