package keyring

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubBao is an in-process stand-in for the parts of OpenBao the keyring
// uses: Kubernetes auth login, renew-self, and transit encrypt/decrypt with a
// derived (or deliberately non-derived) key. Ciphertext uses the real
// "vault:v<N>:" framing and real AES-GCM, so tampering and wrong contexts fail
// the way they do on a real server.
type stubBao struct {
	t          *testing.T
	server     *httptest.Server
	mu         sync.Mutex
	master     []byte
	derived    bool
	role, jwt  string
	lease      int64
	renewable  bool
	tokens     map[string]bool
	logins     int
	renews     int
	encrypts   int
	decrypts   int
	failRenew  bool
	failStatus map[string]int           // path suffix -> status to return
	delay      map[string]time.Duration // path suffix -> delay before answering
	revokeNext bool                     // answer the next transit call 403 and forget tokens
	lastBodies map[string]string
}

func newStubBao(t *testing.T) *stubBao {
	t.Helper()
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	s := &stubBao{t: t, master: master, derived: true, role: "kionga-gateway", jwt: "header.payload.signature", lease: 3600, renewable: true,
		tokens: map[string]bool{}, failStatus: map[string]int{}, delay: map[string]time.Duration{}, lastBodies: map[string]string{}}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stubBao) reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *stubBao) fail(w http.ResponseWriter, status int, msg string) {
	s.reply(w, status, map[string]any{"errors": []string{msg}})
}

func (s *stubBao) key(context []byte) []byte {
	if !s.derived {
		return s.master
	}
	mac := hmac.New(sha256.New, s.master)
	mac.Write(context)
	return mac.Sum(nil)
}

func (s *stubBao) handle(w http.ResponseWriter, r *http.Request) {
	// Read the body first: the server notices a client hang-up (and cancels
	// r.Context) only once the request body has been consumed.
	requestBody, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	var delay time.Duration
	for suffix, d := range s.delay {
		if strings.HasSuffix(r.URL.Path, suffix) {
			delay = d
		}
	}
	s.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var body map[string]string
	if r.Method != http.MethodPost || json.Unmarshal(requestBody, &body) != nil {
		s.fail(w, 400, "bad request")
		return
	}
	raw, _ := json.Marshal(body)
	s.lastBodies[r.URL.Path] = string(raw)
	for suffix, status := range s.failStatus {
		if strings.HasSuffix(r.URL.Path, suffix) {
			s.fail(w, status, "injected failure for "+body["jwt"])
			return
		}
	}
	switch r.URL.Path {
	case "/v1/auth/kubernetes/login":
		s.logins++
		if body["role"] != s.role || body["jwt"] != s.jwt {
			s.fail(w, 403, "permission denied")
			return
		}
		token := fmt.Sprintf("s.token%d", s.logins)
		s.tokens[token] = true
		s.reply(w, 200, map[string]any{"auth": map[string]any{"client_token": token, "lease_duration": s.lease, "renewable": s.renewable}})
	case "/v1/auth/token/renew-self":
		s.renews++
		token := r.Header.Get("X-Vault-Token")
		if s.failRenew || !s.tokens[token] {
			s.fail(w, 403, "permission denied")
			return
		}
		s.reply(w, 200, map[string]any{"auth": map[string]any{"client_token": token, "lease_duration": s.lease, "renewable": s.renewable}})
	case "/v1/transit/encrypt/kionga-credential", "/v1/transit/decrypt/kionga-credential":
		if s.revokeNext {
			s.revokeNext = false
			s.tokens = map[string]bool{}
		}
		if !s.tokens[r.Header.Get("X-Vault-Token")] {
			s.fail(w, 403, "permission denied")
			return
		}
		context, err := base64.StdEncoding.DecodeString(body["context"])
		if err != nil || (s.derived && len(context) == 0) {
			s.fail(w, 400, "missing 'context' for key derivation")
			return
		}
		block, _ := aes.NewCipher(s.key(context))
		aead, _ := cipher.NewGCM(block)
		if strings.HasSuffix(r.URL.Path, "encrypt/kionga-credential") {
			s.encrypts++
			plain, err := base64.StdEncoding.DecodeString(body["plaintext"])
			if err != nil {
				s.fail(w, 400, "plaintext is not base64")
				return
			}
			nonce := make([]byte, aead.NonceSize())
			_, _ = rand.Read(nonce)
			s.reply(w, 200, map[string]any{"data": map[string]any{"ciphertext": "vault:v1:" + base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, nil)), "key_version": 1}})
			return
		}
		s.decrypts++
		encoded, ok := strings.CutPrefix(body["ciphertext"], "vault:v1:")
		data, err := base64.StdEncoding.DecodeString(encoded)
		if !ok || err != nil || len(data) < aead.NonceSize() {
			s.fail(w, 400, "invalid ciphertext")
			return
		}
		plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], nil)
		if err != nil {
			s.fail(w, 400, "cipher: message authentication failed")
			return
		}
		s.reply(w, 200, map[string]any{"data": map[string]any{"plaintext": base64.StdEncoding.EncodeToString(plain)}})
	default:
		s.fail(w, 404, "no handler for route")
	}
}

func (s *stubBao) counts() (logins, renews, encrypts, decrypts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logins, s.renews, s.encrypts, s.decrypts
}

func (s *stubBao) set(fn func(*stubBao)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

func writeJWT(t *testing.T, jwt string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(jwt+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (s *stubBao) config(t *testing.T) OpenBaoConfig {
	return OpenBaoConfig{Addr: s.server.URL, TransitKey: "kionga-credential", AuthRole: "kionga-gateway", JWTFile: writeJWT(t, s.jwt), AllowHTTP: true, Timeout: 2 * time.Second}
}

func (s *stubBao) client(t *testing.T) *OpenBao {
	t.Helper()
	client, err := NewOpenBao(s.config(t))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func pemCert(server *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
}
