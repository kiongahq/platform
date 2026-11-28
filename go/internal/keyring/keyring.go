// Package keyring seals the credentials the gateway stores for users (Hugging
// Face and GitHub tokens) and opens them again.
//
// Two providers exist:
//
//   - static: AES-256-GCM with KIONGA_CREDENTIAL_KEY. The stored value is
//     base64(nonce || ciphertext || tag) with the record's associated data
//     authenticated, byte for byte what the gateway has always written.
//   - openbao: OpenBao transit. The key never leaves OpenBao; the gateway sends
//     plaintext over TLS and stores transit's "vault:vN:..." ciphertext. The
//     associated data is bound as the transit key-derivation context, so a
//     record opens only for the subject it was sealed for.
//
// A Ring routes by format: a value starting with "vault:v" is transit
// ciphertext, anything else is static. Writes always use the configured
// provider; reads accept either format when that provider is available, so
// records written before a provider switch keep opening until they are
// rewrapped.
package keyring

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// Sealer seals and opens one value. The associated data is authenticated but
// not stored: Open must be given the same associated data that Seal was.
type Sealer interface {
	Seal(ctx context.Context, plaintext, aad []byte) ([]byte, error)
	Open(ctx context.Context, ciphertext, aad []byte) ([]byte, error)
}

const (
	ProviderStatic  = "static"
	ProviderOpenBao = "openbao"
)

var (
	// ErrNotConfigured means no provider can seal: static without a valid key.
	ErrNotConfigured = errors.New("credential encryption is not configured")
	// ErrStaticUnavailable means a static record cannot be opened because the
	// static key is absent (openbao provider without a legacy credential key).
	ErrStaticUnavailable = errors.New("record was sealed with KIONGA_CREDENTIAL_KEY, which this gateway does not have; run the rewrap command or reconnect the account")
	// ErrTransitUnavailable means a transit record cannot be opened because
	// OpenBao is not configured.
	ErrTransitUnavailable = errors.New("record was sealed by OpenBao transit, which this gateway is not configured to use")
	// ErrOpen means the value is malformed or failed authentication.
	ErrOpen = errors.New("credential cannot be decrypted")
)

var transitPrefix = regexp.MustCompile(`^vault:v([1-9][0-9]{0,9}):`)

// Format names the provider and key version that produced a stored value:
// "static", "openbao:v<N>", or "empty". It never inspects more than the prefix.
func Format(ciphertext []byte) string {
	if len(ciphertext) == 0 {
		return "empty"
	}
	if m := transitPrefix.FindSubmatch(ciphertext); m != nil {
		return "openbao:v" + string(m[1])
	}
	return ProviderStatic
}

// IsTransit reports whether a stored value is OpenBao transit ciphertext.
// Static values are standard base64, which never contains ':'.
func IsTransit(ciphertext []byte) bool { return transitPrefix.Match(ciphertext) }

// TransitVersion returns the transit key version of a transit value, or 0.
func TransitVersion(ciphertext []byte) int {
	m := transitPrefix.FindSubmatch(ciphertext)
	if m == nil {
		return 0
	}
	v, _ := strconv.Atoi(string(m[1]))
	return v
}

// Ring is the gateway's credential keyring.
type Ring struct {
	provider string
	static   *Static
	transit  *OpenBao
}

// NewRing builds a ring for provider. static and transit may be nil; the
// provider's own sealer must be present for Seal to work.
func NewRing(provider string, static *Static, transit *OpenBao) (*Ring, error) {
	switch provider {
	case ProviderStatic, ProviderOpenBao:
	default:
		return nil, fmt.Errorf("KIONGA_KEY_PROVIDER must be static or openbao")
	}
	if provider == ProviderOpenBao && transit == nil {
		return nil, errors.New("the openbao key provider needs an OpenBao client")
	}
	return &Ring{provider: provider, static: static, transit: transit}, nil
}

// Provider returns the provider that seals new values.
func (r *Ring) Provider() string { return r.provider }

// Configured reports whether Seal can succeed (subject to OpenBao being up).
func (r *Ring) Configured() bool { return r.writer() != nil }

// HasStatic reports whether the ring can open static records.
func (r *Ring) HasStatic() bool { return r.static != nil }

// Transit returns the OpenBao client, or nil.
func (r *Ring) Transit() *OpenBao { return r.transit }

func (r *Ring) writer() Sealer {
	if r.provider == ProviderOpenBao {
		if r.transit == nil {
			return nil
		}
		return r.transit
	}
	if r.static == nil {
		return nil
	}
	return r.static
}

// Seal seals with the configured provider.
func (r *Ring) Seal(ctx context.Context, plaintext, aad []byte) ([]byte, error) {
	w := r.writer()
	if w == nil {
		return nil, ErrNotConfigured
	}
	return w.Seal(ctx, plaintext, aad)
}

// Open opens a value of either format, choosing the provider by its prefix.
func (r *Ring) Open(ctx context.Context, ciphertext, aad []byte) ([]byte, error) {
	if IsTransit(ciphertext) {
		if r.transit == nil {
			return nil, ErrTransitUnavailable
		}
		return r.transit.Open(ctx, ciphertext, aad)
	}
	if r.static == nil {
		if r.provider == ProviderStatic {
			return nil, ErrNotConfigured
		}
		return nil, ErrStaticUnavailable
	}
	return r.static.Open(ctx, ciphertext, aad)
}

// Current reports whether a stored value is already in the configured
// provider's format, so rewrap leaves it alone.
func (r *Ring) Current(ciphertext []byte) bool {
	return IsTransit(ciphertext) == (r.provider == ProviderOpenBao)
}
