package runtimeconfig

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

// productionGateway sets a production configuration that passes with the
// static provider.
func productionGateway(t *testing.T) {
	t.Helper()
	t.Setenv("KIONGA_ENVIRONMENT", "production")
	for _, key := range []string{"OIDC_ISSUER", "OIDC_JWKS_URL", "OIDC_AUTH_URL", "OIDC_TOKEN_URL"} {
		t.Setenv(key, "https://id.company.test/path")
	}
	t.Setenv("OIDC_AUDIENCE", "kionga")
	t.Setenv("OIDC_CLIENT_ID", "kionga")
	t.Setenv("OIDC_CLIENT_SECRET", strings.Repeat("s", 32))
	t.Setenv("KIONGA_INTERNAL_TOKEN", strings.Repeat("t", 32))
	t.Setenv("KIONGA_CREDENTIAL_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("DATABASE_URL", "postgres://user:pass@db.company.test/db?sslmode=verify-full")
	t.Setenv("KIONGA_ALLOWED_ORIGIN", "https://app.company.test")
	t.Setenv("OIDC_REDIRECT_URL", "https://app.company.test/auth/callback")
	for _, key := range []string{"KIONGA_KEY_PROVIDER", "KIONGA_SIGNING_KEY", "OPENBAO_ADDR", "OPENBAO_TRANSIT_KEY", "OPENBAO_AUTH_ROLE"} {
		t.Setenv(key, "")
	}
}

func productionOpenBao(t *testing.T) {
	t.Helper()
	productionGateway(t)
	t.Setenv("KIONGA_KEY_PROVIDER", "openbao")
	t.Setenv("KIONGA_CREDENTIAL_KEY", "")
	t.Setenv("OPENBAO_ADDR", "https://openbao.openbao.svc:8200")
	t.Setenv("OPENBAO_TRANSIT_KEY", "kionga-credential")
	t.Setenv("OPENBAO_AUTH_ROLE", "kionga-gateway")
	t.Setenv("KIONGA_SIGNING_KEY", strings.Repeat("k", 48))
}

func TestProductionStaticProviderNeedsTheCredentialKey(t *testing.T) {
	productionGateway(t)
	if err := ValidateGateway(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIONGA_KEY_PROVIDER", "static")
	if err := ValidateGateway(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIONGA_CREDENTIAL_KEY", "")
	if err := ValidateGateway(); err == nil || !strings.Contains(err.Error(), "KIONGA_CREDENTIAL_KEY") {
		t.Fatalf("static production without a credential key: %v", err)
	}
}

func TestProductionOpenBaoProviderNeedsNoCredentialKey(t *testing.T) {
	productionOpenBao(t)
	if err := ValidateGateway(); err != nil {
		t.Fatalf("complete OpenBao configuration refused: %v", err)
	}
	// A valid legacy key alongside is accepted; a malformed one is not.
	t.Setenv("KIONGA_CREDENTIAL_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err := ValidateGateway(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIONGA_CREDENTIAL_KEY", "short")
	if err := ValidateGateway(); err == nil {
		t.Fatal("malformed legacy credential key accepted")
	}
}

func TestProductionOpenBaoProviderRefusesIncompleteConfiguration(t *testing.T) {
	cases := map[string][2]string{
		"missing address":    {"OPENBAO_ADDR", ""},
		"plain http":         {"OPENBAO_ADDR", "http://openbao.openbao.svc:8200"},
		"credentials in url": {"OPENBAO_ADDR", "https://root:x@openbao.openbao.svc:8200"},
		"missing key":        {"OPENBAO_TRANSIT_KEY", ""},
		"missing role":       {"OPENBAO_AUTH_ROLE", ""},
		"unknown provider":   {"KIONGA_KEY_PROVIDER", "vault"},
		"weak signing key":   {"KIONGA_SIGNING_KEY", "change-me-change-me-change-me-change-me"},
		"short signing key":  {"KIONGA_SIGNING_KEY", "short"},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			productionOpenBao(t)
			t.Setenv(change[0], change[1])
			if err := ValidateGateway(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestProductionOpenBaoWithoutSigningKeyExplainsWhy(t *testing.T) {
	productionOpenBao(t)
	t.Setenv("KIONGA_SIGNING_KEY", "")
	err := ValidateGateway()
	if err == nil || !strings.Contains(err.Error(), "KIONGA_SIGNING_KEY") || !strings.Contains(err.Error(), "no longer holds") {
		t.Fatalf("got %v", err)
	}
}

func TestSigningKeyIsASecretLoadedFromFile(t *testing.T) {
	setenvClean(t, "KIONGA_SIGNING_KEY")
	t.Setenv("KIONGA_CONFIG_FILE", "")
	t.Setenv("KIONGA_SIGNING_KEY_FILE", secretFile(t, strings.Repeat("f", 40)+"\n"))
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("KIONGA_SIGNING_KEY"); got != strings.Repeat("f", 40) {
		t.Fatalf("got %q", got)
	}
	t.Setenv("KIONGA_SIGNING_KEY_FILE", "")
	config := t.TempDir() + "/config.json"
	if err := os.WriteFile(config, []byte(`{"KIONGA_SIGNING_KEY":"inline"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIONGA_CONFIG_FILE", config)
	if err := Load(); err == nil {
		t.Fatal("signing key accepted from the non-secret config file")
	}
}

func TestKeyServiceSettingsAreNeverReferences(t *testing.T) {
	names := []string{"OPENBAO_ADDR", "OPENBAO_TRANSIT_KEY", "OPENBAO_AUTH_ROLE", "OPENBAO_AUTH_MOUNT", "OPENBAO_CACERT", "OPENBAO_AUTH_JWT", "BAO_TOKEN", "VAULT_TOKEN", "BAO_ADDR", "KIONGA_KEY_PROVIDER", "KIONGA_SIGNING_KEY", "KIONGA_CREDENTIAL_KEY"}
	t.Setenv("KIONGA_KB_SECRET_REFS", strings.Join(names, ","))
	t.Setenv("KIONGA_CONNECTION_SECRET_REFS", strings.Join(names, ","))
	t.Setenv("KIONGA_PLATFORM_SECRET_REFS", strings.Join(names, ","))
	for _, name := range names {
		for _, scope := range allScopes() {
			if scope.Permits("env:" + name) {
				t.Errorf("%s is referenceable through an allowlist", name)
			}
		}
		if !GatewayOwnSecret(name) {
			t.Errorf("%s is not a gateway-owned name", name)
		}
	}
	// The CA file path is never turned into a variable by the reference loader.
	setenvClean(t, "OPENBAO_CACERT")
	t.Setenv("OPENBAO_CACERT_FILE", secretFile(t, "-----BEGIN CERTIFICATE-----"))
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("OPENBAO_CACERT") != "" {
		t.Fatal("OPENBAO_CACERT_FILE was loaded as a reference")
	}
}
