package keyring

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Environment variables read by FromEnv.
const (
	EnvProvider      = "KIONGA_KEY_PROVIDER"
	EnvCredentialKey = "KIONGA_CREDENTIAL_KEY" // #nosec G101 -- variable name
	EnvAddr          = "OPENBAO_ADDR"
	EnvTransitKey    = "OPENBAO_TRANSIT_KEY"
	EnvTransitMount  = "OPENBAO_TRANSIT_MOUNT"
	EnvAuthRole      = "OPENBAO_AUTH_ROLE"
	EnvAuthMount     = "OPENBAO_AUTH_MOUNT"
	EnvCACertFile    = "OPENBAO_CACERT_FILE"
	EnvJWTFile       = "OPENBAO_AUTH_JWT_FILE" // #nosec G101 -- variable name
)

// ProviderFromEnv returns the configured provider name, defaulting to static.
func ProviderFromEnv(getenv func(string) string) string {
	provider := strings.TrimSpace(getenv(EnvProvider))
	if provider == "" {
		return ProviderStatic
	}
	return provider
}

// OpenBaoConfigFromEnv reads the OpenBao settings.
func OpenBaoConfigFromEnv(getenv func(string) string) OpenBaoConfig {
	return OpenBaoConfig{
		Addr:         strings.TrimSpace(getenv(EnvAddr)),
		TransitKey:   strings.TrimSpace(getenv(EnvTransitKey)),
		TransitMount: strings.Trim(strings.TrimSpace(getenv(EnvTransitMount)), "/"),
		AuthRole:     strings.TrimSpace(getenv(EnvAuthRole)),
		AuthMount:    strings.Trim(strings.TrimSpace(getenv(EnvAuthMount)), "/"),
		CACertFile:   strings.TrimSpace(getenv(EnvCACertFile)),
		JWTFile:      strings.TrimSpace(getenv(EnvJWTFile)),
		AllowHTTP:    getenv("KIONGA_ENVIRONMENT") != "production",
	}
}

// openBaoConfigured reports whether enough OpenBao settings exist to build a
// client (used to offer transit reads to a static gateway during rollback).
func openBaoConfigured(cfg OpenBaoConfig) bool {
	return cfg.Addr != "" && cfg.TransitKey != "" && cfg.AuthRole != ""
}

// FromEnv builds the gateway keyring.
//
// With provider static it behaves as the gateway always has: KIONGA_CREDENTIAL_KEY
// seals and opens, and a missing key leaves credential connections unconfigured
// rather than stopping the gateway. If OpenBao is also configured, transit
// records stay readable, which is what a rollback from openbao needs before
// rewrap converts them back.
//
// With provider openbao, sealing never touches KIONGA_CREDENTIAL_KEY. The key
// is optional and, when present, only opens records written before the switch
// until rewrap has converted them; remove it afterwards. FromEnv contacts
// OpenBao (Check) and fails closed: an unreachable OpenBao, a failed login or a
// transit key that does not bind context is an error, never a silent fallback.
func FromEnv(ctx context.Context, getenv func(string) string) (*Ring, error) {
	provider := ProviderFromEnv(getenv)
	var static *Static
	if encoded := getenv(EnvCredentialKey); encoded != "" {
		s, err := NewStatic(encoded)
		if err != nil && provider == ProviderOpenBao {
			return nil, err
		}
		static = s
	}
	cfg := OpenBaoConfigFromEnv(getenv)
	switch provider {
	case ProviderStatic:
		var transit *OpenBao
		if openBaoConfigured(cfg) {
			client, err := NewOpenBao(cfg)
			if err != nil {
				return nil, err
			}
			transit = client
		}
		return NewRing(provider, static, transit)
	case ProviderOpenBao:
		client, err := NewOpenBao(cfg)
		if err != nil {
			return nil, err
		}
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := client.Check(checkCtx); err != nil {
			return nil, fmt.Errorf("KIONGA_KEY_PROVIDER=openbao: %w", err)
		}
		return NewRing(provider, static, client)
	default:
		return nil, fmt.Errorf("%s must be static or openbao", EnvProvider)
	}
}
