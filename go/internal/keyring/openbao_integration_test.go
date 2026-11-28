package keyring

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Integration with a real OpenBao server, run when OPENBAO_TEST_ADDR points
// at one (CI starts `bao server -dev`; locally:
// bao server -dev -dev-root-token-id=root, then
// OPENBAO_TEST_ADDR=http://127.0.0.1:8200 OPENBAO_TEST_TOKEN=root go test ./internal/keyring -run Real).
//
// Each run mounts its own transit engine and auth method. Login goes through
// the JWT auth method, whose login request is the same {role, jwt} body as
// the Kubernetes method, because a dev server has no Kubernetes API to review
// service-account tokens. The token's only policy is the least-privilege
// policy the deployment bootstrap gives the gateway, so the test also proves
// that policy is sufficient.

type baoAdmin struct {
	t     *testing.T
	addr  string
	token string
}

func (a baoAdmin) do(method, path string, body any) map[string]any {
	a.t.Helper()
	var reader io.Reader
	if body != nil {
		payload, _ := json.Marshal(body)
		reader = bytes.NewReader(payload)
	}
	req, _ := http.NewRequest(method, a.addr+"/v1/"+path, reader)
	req.Header.Set("X-Vault-Token", a.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		a.t.Fatalf("%s %s: HTTP %d %s", method, path, resp.StatusCode, raw)
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// GatewayTransitPolicy is the policy the gateway's auth role must carry:
// encrypt and decrypt on its one transit key and nothing else.
func gatewayTransitPolicy(mount, key string) string {
	return fmt.Sprintf("path \"%s/encrypt/%s\" {\n  capabilities = [\"update\"]\n}\n\npath \"%s/decrypt/%s\" {\n  capabilities = [\"update\"]\n}\n", mount, key, mount, key)
}

func realOpenBao(t *testing.T) (baoAdmin, string) {
	t.Helper()
	addr := os.Getenv("OPENBAO_TEST_ADDR")
	if addr == "" {
		t.Skip("OPENBAO_TEST_ADDR is not set")
	}
	token := os.Getenv("OPENBAO_TEST_TOKEN")
	if token == "" {
		t.Fatal("OPENBAO_TEST_TOKEN is required with OPENBAO_TEST_ADDR")
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	return baoAdmin{t: t, addr: strings.TrimSuffix(addr, "/"), token: token}, hex.EncodeToString(suffix)
}

func TestRealOpenBaoTransitAndLogin(t *testing.T) {
	admin, suffix := realOpenBao(t)
	mount, authMount, key := "transit-"+suffix, "jwt-"+suffix, "kionga-credential"
	admin.do("POST", "sys/mounts/"+mount, map[string]any{"type": "transit"})
	t.Cleanup(func() { admin.do("DELETE", "sys/mounts/"+mount, nil) })
	admin.do("POST", mount+"/keys/"+key, map[string]any{"derived": true})
	admin.do("POST", mount+"/keys/plain", map[string]any{})
	policy := "kionga-gateway-" + suffix
	admin.do("PUT", "sys/policies/acl/"+policy, map[string]any{"policy": gatewayTransitPolicy(mount, key)})

	signer, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&signer.PublicKey)
	public := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	admin.do("POST", "sys/auth/"+authMount, map[string]any{"type": "jwt"})
	t.Cleanup(func() { admin.do("DELETE", "sys/auth/"+authMount, nil) })
	admin.do("POST", "auth/"+authMount+"/config", map[string]any{"jwt_validation_pubkeys": []string{public}})
	admin.do("POST", "auth/"+authMount+"/role/kionga-gateway", map[string]any{
		"role_type": "jwt", "bound_audiences": []string{"kionga-test"}, "user_claim": "sub",
		"token_policies": []string{policy}, "token_ttl": "60s", "token_max_ttl": "10m",
	})
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "system:serviceaccount:kionga:kionga-gateway", "aud": "kionga-test",
		"iat": time.Now().Unix(), "exp": time.Now().Add(10 * time.Minute).Unix(),
	}).SignedString(signer)
	if err != nil {
		t.Fatal(err)
	}
	jwtFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(jwtFile, []byte(signed), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := OpenBaoConfig{Addr: admin.addr, TransitMount: mount, TransitKey: key, AuthMount: authMount, AuthRole: "kionga-gateway", JWTFile: jwtFile, AllowHTTP: true}
	client, err := NewOpenBao(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Login, then the startup check: round trip and context binding.
	if err := client.Check(ctx); err != nil {
		t.Fatalf("check against real OpenBao: %v", err)
	}
	sealed, err := client.Seal(ctx, []byte("hf_real"), []byte("alice"))
	if err != nil || Format(sealed) != "openbao:v1" {
		t.Fatalf("seal: %q %v", sealed, err)
	}
	if plain, err := client.Open(ctx, sealed, []byte("alice")); err != nil || string(plain) != "hf_real" {
		t.Fatalf("open: %q %v", plain, err)
	}
	if _, err := client.Open(ctx, sealed, []byte("bob")); !errors.Is(err, ErrOpen) {
		t.Fatalf("other subject opened a real transit record: %v", err)
	}

	// Renewal against the real token endpoint (renew-self is in the default
	// policy): move the clock past the renewal point.
	client.mu.Lock()
	tokenBefore := client.token
	client.now = func() time.Time { return time.Now().Add(45 * time.Second) }
	client.mu.Unlock()
	if _, err := client.Seal(ctx, []byte("x"), []byte("alice")); err != nil {
		t.Fatalf("seal after renewal point: %v", err)
	}
	client.mu.Lock()
	renewedSame := client.token == tokenBefore
	client.mu.Unlock()
	if !renewedSame {
		t.Fatal("token was replaced instead of renewed")
	}

	// Rotation: new writes use v2, v1 records still open, and transit rewrap
	// (which takes the derivation context) upgrades a record server-side.
	admin.do("POST", mount+"/keys/"+key+"/rotate", nil)
	fresh, err := client.Seal(ctx, []byte("hf_v2"), []byte("alice"))
	if err != nil || Format(fresh) != "openbao:v2" {
		t.Fatalf("seal after rotation: %q %v", fresh, err)
	}
	if plain, err := client.Open(ctx, sealed, []byte("alice")); err != nil || string(plain) != "hf_real" {
		t.Fatalf("v1 record after rotation: %q %v", plain, err)
	}
	rewrapped := admin.do("POST", mount+"/rewrap/"+key, map[string]any{"ciphertext": string(sealed), "context": "YWxpY2U="}) // base64("alice")
	upgraded := rewrapped["data"].(map[string]any)["ciphertext"].(string)
	if plain, err := client.Open(ctx, []byte(upgraded), []byte("alice")); err != nil || string(plain) != "hf_real" || Format([]byte(upgraded)) != "openbao:v2" {
		t.Fatalf("server-side rewrap: %q %v", plain, err)
	}

	// Least privilege: the gateway's token cannot use another key.
	other := cfg
	other.TransitKey = "plain"
	otherClient, _ := NewOpenBao(other)
	if _, err := otherClient.Seal(ctx, []byte("x"), []byte("alice")); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("policy allowed another key: %v", err)
	}

	// A key created without derived=true is refused at startup. The admin
	// token stands in for the gateway's to reach it.
	plainClient, _ := NewOpenBao(other)
	plainClient.token = admin.token
	if err := plainClient.Check(ctx); err == nil || !strings.Contains(err.Error(), "derived=true") {
		t.Fatalf("non-derived key accepted: %v", err)
	}

	// A wrong role fails login.
	wrong := cfg
	wrong.AuthRole = "nobody"
	wrongClient, _ := NewOpenBao(wrong)
	if err := wrongClient.Check(ctx); err == nil || !strings.Contains(err.Error(), "login") {
		t.Fatalf("wrong role logged in: %v", err)
	}
}
