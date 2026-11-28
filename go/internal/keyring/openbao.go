package keyring

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// OpenBao talks to the OpenBao transit secrets engine over its HTTP API with
// net/http only. The official Go client (github.com/openbao/openbao/api/v2)
// was not used: it adds about twenty modules and its DefaultConfig reads
// ambient BAO_* variables, including BAO_SKIP_VERIFY, which could switch off
// TLS verification behind the operator's back. Four endpoints are needed:
//
//	POST /v1/auth/<auth mount>/login        Kubernetes auth (role, jwt)
//	POST /v1/auth/token/renew-self          token renewal
//	POST /v1/<transit mount>/encrypt/<key>  plaintext, context
//	POST /v1/<transit mount>/decrypt/<key>  ciphertext, context
//
// Associated data binding: the transit key must be created with
// derived=true, and the record's associated data is sent as the base64
// key-derivation context. Transit derives a distinct key per context, so a
// value sealed for one subject fails authentication under another. Transit's
// associated_data parameter was not used because the transit rewrap endpoint
// does not accept it, which would make server-side rewrap after a key
// rotation impossible. A non-derived key silently ignores context, so Check
// refuses to start unless decrypting under a different context fails.
type OpenBao struct {
	cfg    OpenBaoConfig
	client *http.Client
	now    func() time.Time

	mu        sync.Mutex
	token     string
	renewable bool
	issued    time.Time
	expires   time.Time // zero: no expiry
	renewAt   time.Time // zero: never renew
}

// OpenBaoConfig configures the transit provider.
type OpenBaoConfig struct {
	Addr         string // https://openbao.example:8200
	TransitMount string // default "transit"
	TransitKey   string
	AuthMount    string // default "kubernetes"
	AuthRole     string
	CACertFile   string // PEM bundle; when set, only these roots are trusted
	JWTFile      string // projected service-account token
	AllowHTTP    bool   // non-production only
	Timeout      time.Duration
}

// DefaultJWTFile is where Kubernetes projects the service-account token.
const DefaultJWTFile = "/var/run/secrets/kubernetes.io/serviceaccount/token" // #nosec G101 -- a well-known file path, not a credential

var pathSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func validPath(p string) bool {
	for _, segment := range strings.Split(p, "/") {
		if !pathSegment.MatchString(segment) {
			return false
		}
	}
	return true
}

// NewOpenBao validates the configuration and builds the HTTP client. It does
// not contact OpenBao; call Check for that.
func NewOpenBao(cfg OpenBaoConfig) (*OpenBao, error) {
	if cfg.TransitMount == "" {
		cfg.TransitMount = "transit"
	}
	if cfg.AuthMount == "" {
		cfg.AuthMount = "kubernetes"
	}
	if cfg.JWTFile == "" {
		cfg.JWTFile = DefaultJWTFile
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	u, err := url.Parse(cfg.Addr)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("OPENBAO_ADDR must be a URL such as https://openbao.openbao.svc:8200")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !cfg.AllowHTTP) {
		return nil, errors.New("OPENBAO_ADDR must use https")
	}
	cfg.Addr = strings.TrimSuffix(cfg.Addr, "/")
	if cfg.TransitKey == "" || !pathSegment.MatchString(cfg.TransitKey) {
		return nil, errors.New("OPENBAO_TRANSIT_KEY must name a transit key")
	}
	if cfg.AuthRole == "" || !pathSegment.MatchString(cfg.AuthRole) {
		return nil, errors.New("OPENBAO_AUTH_ROLE must name a Kubernetes auth role")
	}
	if !validPath(cfg.TransitMount) || !validPath(cfg.AuthMount) {
		return nil, errors.New("OPENBAO_TRANSIT_MOUNT and OPENBAO_AUTH_MOUNT must be mount paths")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CACertFile != "" {
		pem, err := os.ReadFile(cfg.CACertFile) // #nosec G304 G703 -- operator-set path
		if err != nil {
			return nil, errors.New("cannot read OPENBAO_CACERT_FILE")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("OPENBAO_CACERT_FILE holds no PEM certificate")
		}
		tlsConfig.RootCAs = pool
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil, // never route key operations through an ambient proxy
		DialContext:           dialer.DialContext,
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: cfg.Timeout,
		MaxIdleConns:          8,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	client := &http.Client{
		Timeout:   cfg.Timeout,
		Transport: transport,
		// A redirect would forward X-Vault-Token to wherever it points.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &OpenBao{cfg: cfg, client: client, now: time.Now}, nil
}

// apiError is a failed OpenBao call. Message never carries the token, the
// service-account JWT or any plaintext.
type apiError struct {
	Op     string
	Status int
	Detail string
}

func (e *apiError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("openbao %s failed: %s", e.Op, e.Detail)
	}
	if e.Detail == "" {
		return fmt.Sprintf("openbao %s failed: HTTP %d", e.Op, e.Status)
	}
	return fmt.Sprintf("openbao %s failed: HTTP %d: %s", e.Op, e.Status, e.Detail)
}

type authResponse struct {
	Auth *struct {
		ClientToken   string `json:"client_token"`
		LeaseDuration int64  `json:"lease_duration"`
		Renewable     bool   `json:"renewable"`
	} `json:"auth"`
}

// call posts body to path and decodes the JSON reply into out. secrets are
// values to scrub from any error text.
func (o *OpenBao) call(ctx context.Context, op, path, token string, body, out any, secrets ...string) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return &apiError{Op: op, Detail: "cannot encode request"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.Addr+"/v1/"+path, bytes.NewReader(payload))
	if err != nil {
		return &apiError{Op: op, Detail: "invalid request"}
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	resp, err := o.client.Do(req) // #nosec G704 -- OPENBAO_ADDR is operator configuration; paths are validated, redirects disabled
	if err != nil {
		detail := "OpenBao could not be reached"
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			detail = "timed out"
		}
		return &apiError{Op: op, Detail: detail}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &apiError{Op: op, Status: resp.StatusCode, Detail: "response could not be read"}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var failure struct {
			Errors []string `json:"errors"`
		}
		_ = json.Unmarshal(raw, &failure)
		detail := strings.Join(failure.Errors, "; ")
		for _, secret := range append(secrets, token) {
			if secret != "" {
				detail = strings.ReplaceAll(detail, secret, "[redacted]")
			}
		}
		if len(detail) > 300 {
			detail = detail[:300]
		}
		return &apiError{Op: op, Status: resp.StatusCode, Detail: detail}
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return &apiError{Op: op, Status: resp.StatusCode, Detail: "invalid JSON response"}
	}
	return nil
}

func (o *OpenBao) setToken(a authResponse, op string) error {
	if a.Auth == nil || a.Auth.ClientToken == "" {
		return &apiError{Op: op, Detail: "no client token in response"}
	}
	now := o.now()
	o.token, o.renewable, o.issued = a.Auth.ClientToken, a.Auth.Renewable, now
	o.expires, o.renewAt = time.Time{}, time.Time{}
	if a.Auth.LeaseDuration > 0 {
		lease := time.Duration(a.Auth.LeaseDuration) * time.Second
		o.expires = now.Add(lease)
		// Renew once two thirds of the lease have passed, leaving a third
		// for retries before the token expires.
		o.renewAt = now.Add(lease * 2 / 3)
	}
	return nil
}

func (o *OpenBao) loginLocked(ctx context.Context) error {
	// Re-read every login: kubelet rotates projected tokens.
	jwt, err := os.ReadFile(o.cfg.JWTFile) // #nosec G304 -- operator-set path
	if err != nil {
		return &apiError{Op: "login", Detail: "cannot read the service-account token file"}
	}
	token := strings.TrimSpace(string(jwt))
	if token == "" {
		return &apiError{Op: "login", Detail: "the service-account token file is empty"}
	}
	var auth authResponse
	if err := o.call(ctx, "login", "auth/"+o.cfg.AuthMount+"/login", "", map[string]string{"role": o.cfg.AuthRole, "jwt": token}, &auth, token); err != nil {
		return err
	}
	return o.setToken(auth, "login")
}

func (o *OpenBao) renewLocked(ctx context.Context) error {
	var auth authResponse
	if err := o.call(ctx, "token renewal", "auth/token/renew-self", o.token, map[string]string{}, &auth); err != nil {
		return err
	}
	if auth.Auth == nil || auth.Auth.ClientToken == "" {
		return &apiError{Op: "token renewal", Detail: "no client token in response"}
	}
	// A renewed lease must still expire; a zero lease (max TTL reached)
	// means log in again rather than treat the token as permanent.
	if auth.Auth.LeaseDuration <= 0 {
		return &apiError{Op: "token renewal", Detail: "lease cannot be extended"}
	}
	return o.setToken(auth, "token renewal")
}

// ensureToken returns a usable token: the cached one before its renewal
// point, a renewed one after it, and a fresh login when renewal is not
// possible or fails.
func (o *OpenBao) ensureToken(ctx context.Context) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	if o.token != "" && (o.renewAt.IsZero() || now.Before(o.renewAt)) {
		return o.token, nil
	}
	if o.token != "" && o.renewable && now.Before(o.expires) {
		if err := o.renewLocked(ctx); err == nil {
			return o.token, nil
		}
	}
	o.token = ""
	if err := o.loginLocked(ctx); err != nil {
		return "", err
	}
	return o.token, nil
}

func (o *OpenBao) dropToken(token string) {
	o.mu.Lock()
	if o.token == token {
		o.token = ""
	}
	o.mu.Unlock()
}

// transit runs one transit operation, logging in again once if OpenBao says
// the token is no longer valid (revoked, expired early, OpenBao restarted).
func (o *OpenBao) transit(ctx context.Context, op string, body map[string]string, out any, secrets ...string) error {
	path := o.cfg.TransitMount + "/" + op + "/" + o.cfg.TransitKey
	for attempt := 0; ; attempt++ {
		token, err := o.ensureToken(ctx)
		if err != nil {
			return err
		}
		err = o.call(ctx, "transit "+op, path, token, body, out, secrets...)
		var apiErr *apiError
		if attempt == 0 && errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden {
			o.dropToken(token)
			continue
		}
		return err
	}
}

// Seal encrypts plaintext under the transit key derived for aad.
func (o *OpenBao) Seal(ctx context.Context, plaintext, aad []byte) ([]byte, error) {
	if len(aad) == 0 {
		return nil, errors.New("openbao transit sealing needs associated data")
	}
	var out struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	encoded := base64.StdEncoding.EncodeToString(plaintext)
	if err := o.transit(ctx, "encrypt", map[string]string{"plaintext": encoded, "context": base64.StdEncoding.EncodeToString(aad)}, &out, encoded); err != nil {
		return nil, err
	}
	if !IsTransit([]byte(out.Data.Ciphertext)) {
		return nil, &apiError{Op: "transit encrypt", Detail: "response is not transit ciphertext"}
	}
	return []byte(out.Data.Ciphertext), nil
}

// Open decrypts transit ciphertext under the key derived for aad.
func (o *OpenBao) Open(ctx context.Context, ciphertext, aad []byte) ([]byte, error) {
	if !IsTransit(ciphertext) {
		return nil, ErrOpen
	}
	if len(aad) == 0 {
		return nil, errors.New("openbao transit opening needs associated data")
	}
	var out struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	err := o.transit(ctx, "decrypt", map[string]string{"ciphertext": string(ciphertext), "context": base64.StdEncoding.EncodeToString(aad)}, &out)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest {
		// Wrong context, tampered value or unknown key version.
		return nil, fmt.Errorf("%w: %v", ErrOpen, err)
	}
	if err != nil {
		return nil, err
	}
	plain, err := base64.StdEncoding.DecodeString(out.Data.Plaintext)
	if err != nil {
		return nil, &apiError{Op: "transit decrypt", Detail: "response is not base64"}
	}
	return plain, nil
}

// Check logs in and proves the transit key works and binds associated data:
// a value sealed under one context must open under it and fail under
// another. The gateway calls it at startup and refuses to run if it fails.
func (o *OpenBao) Check(ctx context.Context) error {
	if _, err := o.ensureToken(ctx); err != nil {
		return err
	}
	probe := make([]byte, 16)
	if _, err := rand.Read(probe); err != nil {
		return err
	}
	sealed, err := o.Seal(ctx, probe, []byte("kionga-keyring-check:a"))
	if err != nil {
		return err
	}
	opened, err := o.Open(ctx, sealed, []byte("kionga-keyring-check:a"))
	if err != nil {
		return err
	}
	if !bytes.Equal(opened, probe) {
		return errors.New("openbao transit check: decrypted value does not match")
	}
	_, err = o.Open(ctx, sealed, []byte("kionga-keyring-check:b"))
	if err == nil {
		return fmt.Errorf("openbao transit key %q ignores the derivation context: create it with derived=true so each credential is bound to its owner", o.cfg.TransitKey)
	}
	if !errors.Is(err, ErrOpen) {
		return err
	}
	return nil
}

// RunRenewal renews the token in the background before it expires, so a
// quiet gateway does not let it lapse. Operations also renew on demand.
func (o *OpenBao) RunRenewal(ctx context.Context) {
	for {
		o.mu.Lock()
		wait := time.Minute
		if !o.renewAt.IsZero() {
			wait = o.renewAt.Sub(o.now())
		}
		o.mu.Unlock()
		if wait < time.Second {
			wait = time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		callCtx, cancel := context.WithTimeout(ctx, o.cfg.Timeout)
		if _, err := o.ensureToken(callCtx); err != nil {
			log.Printf("credential keyring: %v", err)
		}
		cancel()
	}
}
