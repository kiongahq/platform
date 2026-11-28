package httpapi

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"testing"
)

// originalAppKey is the derivation before KIONGA_SIGNING_KEY existed.
func originalAppKey(secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("kionga application sign-in v1"))
	return mac.Sum(nil)
}

func envOf(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestAppSigningKeySources(t *testing.T) {
	credential, internal, signing := "Y3JlZGVudGlhbC1rZXktY3JlZGVudGlhbC1rZXk=", "internal-token-internal-token-1234", "signing-key-signing-key-signing-key-1"

	// Existing installs: unchanged derivation from the credential key.
	key, stable := deriveAppSigningKey(envOf(map[string]string{"KIONGA_CREDENTIAL_KEY": credential, "KIONGA_INTERNAL_TOKEN": internal}))
	if !stable || !bytes.Equal(key, originalAppKey(credential)) {
		t.Fatal("upgrading changed the signing key of a static install")
	}
	key, _ = deriveAppSigningKey(envOf(map[string]string{"KIONGA_INTERNAL_TOKEN": internal}))
	if !bytes.Equal(key, originalAppKey(internal)) {
		t.Fatal("internal-token fallback changed")
	}

	// The signing key wins whenever it is set.
	key, _ = deriveAppSigningKey(envOf(map[string]string{"KIONGA_SIGNING_KEY": signing, "KIONGA_CREDENTIAL_KEY": credential}))
	if !bytes.Equal(key, originalAppKey(signing)) {
		t.Fatal("KIONGA_SIGNING_KEY ignored")
	}

	// openbao: the credential key never feeds signing, so removing the
	// legacy key after rewrap leaves sessions valid.
	withLegacy, _ := deriveAppSigningKey(envOf(map[string]string{"KIONGA_KEY_PROVIDER": "openbao", "KIONGA_CREDENTIAL_KEY": credential, "KIONGA_INTERNAL_TOKEN": internal}))
	without, _ := deriveAppSigningKey(envOf(map[string]string{"KIONGA_KEY_PROVIDER": "openbao", "KIONGA_INTERNAL_TOKEN": internal}))
	if !bytes.Equal(withLegacy, without) || bytes.Equal(withLegacy, originalAppKey(credential)) {
		t.Fatal("openbao signing key depends on the credential key")
	}

	// Nothing configured: random and reported unstable.
	first, stable := deriveAppSigningKey(envOf(nil))
	second, _ := deriveAppSigningKey(envOf(nil))
	if stable || bytes.Equal(first, second) {
		t.Fatal("random fallback")
	}
}
