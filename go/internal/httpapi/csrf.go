package httpapi

import (
	"errors"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// User workspaces run on hosts that share a registrable site with the console
// (runtimeconfig requires it for host-only workspace cookies). Browsers treat
// those hosts as same-site, so SameSite cookies alone do not stop a notebook
// from sending a credentialed POST to the console. crossOriginGuard closes
// that gap for every unsafe method that carries cookies:
//
//   - Sec-Fetch-Site must be same-origin (or none, a user-initiated request),
//     or the Origin must equal MLAIOPS_ALLOWED_ORIGIN;
//   - browsers that send neither header fall back to comparing Origin with
//     Host (net/http.CrossOriginProtection);
//   - requests with no cookies are bearer, API-token or workspace-identity
//     clients and are not exposed to ambient-credential forgery, so they pass.
func crossOriginGuard(next http.Handler) http.Handler {
	protection := newCrossOriginProtection()
	guarded := protection.Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Cookies()) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		guarded.ServeHTTP(w, r)
	})
}

func newCrossOriginProtection() *http.CrossOriginProtection {
	protection := http.NewCrossOriginProtection()
	if origin := trustedOrigin(); origin != "" {
		if err := protection.AddTrustedOrigin(origin); err != nil {
			log.Printf("MLAIOPS_ALLOWED_ORIGIN %q is not a valid origin; only same-origin writes are accepted: %v", origin, err)
		}
	}
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusForbidden, "cross_origin", "State-changing requests must come from the Kionga console origin.")
	}))
	return protection
}

// trustedOrigin returns MLAIOPS_ALLOWED_ORIGIN normalised to scheme://host,
// or "" when it is unset or the wildcard.
func trustedOrigin() string {
	raw := strings.TrimSpace(os.Getenv("MLAIOPS_ALLOWED_ORIGIN"))
	if raw == "" || raw == "*" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	return parsed.Scheme + "://" + parsed.Host
}

// errUnsupportedMediaType is returned by decode when a request body is not
// declared as JSON. text/plain and form bodies are CORS "simple" requests
// that browsers send without a preflight, so the API never parses them.
var errUnsupportedMediaType = errors.New("request body must be sent with Content-Type: application/json")

func requireJSON(r *http.Request) error {
	value := r.Header.Get("Content-Type")
	if value == "" && r.ContentLength == 0 {
		// No body at all: let the decoder report io.EOF, which handlers with
		// optional bodies already accept.
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "application/json" {
		return errUnsupportedMediaType
	}
	return nil
}

// writeDecodeError maps a decode failure to its HTTP status: 415 for a body
// that is not declared as JSON, 400 for malformed JSON.
func writeDecodeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errUnsupportedMediaType) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
}
