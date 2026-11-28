// Package keyringtest provides an in-process OpenBao stand-in for tests.
package keyringtest

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

// Bao is an in-process stand-in for the parts of OpenBao the keyring
// uses: Kubernetes auth login, renew-self, and transit encrypt/decrypt with a
// derived (or deliberately non-derived) key. Ciphertext uses the real
// "vault:v<N>:" framing and real AES-GCM, so tampering and wrong contexts fail
// the way they do on a real server. Tests change its fields through Set.
type Bao struct {
	t          *testing.T
	Server     *httptest.Server
	mu         sync.Mutex
	Master     []byte
	Derived    bool
	Role, JWT  string
	Lease      int64
	Renewable  bool
	Tokens     map[string]bool
	Logins     int
	Renews     int
	Encrypts   int
	Decrypts   int
	FailRenew  bool
	FailStatus map[string]int           // path suffix -> status to return
	Delay      map[string]time.Duration // path suffix -> delay before answering
	RevokeNext bool                     // answer the next transit call 403 and forget tokens
	LastBodies map[string]string
}

// New starts a stub; it stops when the test ends.
func New(t *testing.T) *Bao {
	t.Helper()
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	s := &Bao{t: t, Master: master, Derived: true, Role: "kionga-gateway", JWT: "header.payload.signature", Lease: 3600, Renewable: true,
		Tokens: map[string]bool{}, FailStatus: map[string]int{}, Delay: map[string]time.Duration{}, LastBodies: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Server.Close)
	return s
}

func (s *Bao) reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Bao) fail(w http.ResponseWriter, status int, msg string) {
	s.reply(w, status, map[string]any{"errors": []string{msg}})
}

func (s *Bao) key(context []byte) []byte {
	if !s.Derived {
		return s.Master
	}
	mac := hmac.New(sha256.New, s.Master)
	mac.Write(context)
	return mac.Sum(nil)
}

func (s *Bao) handle(w http.ResponseWriter, r *http.Request) {
	// Read the body first: the server notices a client hang-up (and cancels
	// r.Context) only once the request body has been consumed.
	requestBody, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	var delay time.Duration
	for suffix, d := range s.Delay {
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
	s.LastBodies[r.URL.Path] = string(raw)
	for suffix, status := range s.FailStatus {
		if strings.HasSuffix(r.URL.Path, suffix) {
			s.fail(w, status, "injected failure for "+body["jwt"])
			return
		}
	}
	switch r.URL.Path {
	case "/v1/auth/kubernetes/login":
		s.Logins++
		if body["role"] != s.Role || body["jwt"] != s.JWT {
			s.fail(w, 403, "permission denied")
			return
		}
		token := fmt.Sprintf("s.token%d", s.Logins)
		s.Tokens[token] = true
		s.reply(w, 200, map[string]any{"auth": map[string]any{"client_token": token, "lease_duration": s.Lease, "renewable": s.Renewable}})
	case "/v1/auth/token/renew-self":
		s.Renews++
		token := r.Header.Get("X-Vault-Token")
		if s.FailRenew || !s.Tokens[token] {
			s.fail(w, 403, "permission denied")
			return
		}
		s.reply(w, 200, map[string]any{"auth": map[string]any{"client_token": token, "lease_duration": s.Lease, "renewable": s.Renewable}})
	case "/v1/transit/encrypt/kionga-credential", "/v1/transit/decrypt/kionga-credential":
		if s.RevokeNext {
			s.RevokeNext = false
			s.Tokens = map[string]bool{}
		}
		if !s.Tokens[r.Header.Get("X-Vault-Token")] {
			s.fail(w, 403, "permission denied")
			return
		}
		context, err := base64.StdEncoding.DecodeString(body["context"])
		if err != nil || (s.Derived && len(context) == 0) {
			s.fail(w, 400, "missing 'context' for key derivation")
			return
		}
		block, _ := aes.NewCipher(s.key(context))
		aead, _ := cipher.NewGCM(block)
		if strings.HasSuffix(r.URL.Path, "encrypt/kionga-credential") {
			s.Encrypts++
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
		s.Decrypts++
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

// Counts returns the number of each call received.
func (s *Bao) Counts() (logins, renews, encrypts, decrypts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Logins, s.Renews, s.Encrypts, s.Decrypts
}

// Set changes the stub's behaviour under its lock.
func (s *Bao) Set(fn func(*Bao)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

// WriteJWT writes a service-account token file and returns its path.
func WriteJWT(t *testing.T, jwt string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(jwt+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// LastBody returns the JSON body of the last request to path.
func (s *Bao) LastBody(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastBodies[path]
}

// URL is the stub's address.
func (s *Bao) URL() string { return s.Server.URL }

// PEM returns the certificate of a TLS test server, for a CA file.
func PEM(server *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
}
