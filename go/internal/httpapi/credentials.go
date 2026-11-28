package httpapi

import (
	"context"
	"errors"
	"log"
	"os"
	"sync"

	"github.com/kiongahq/platform/internal/keyring"
	"github.com/kiongahq/platform/internal/store"
)

// The gateway seals personal account credentials (Hugging Face and GitHub
// tokens, and the GitHub authorization state cookie) through one keyring. The
// associated data names the owner and the purpose, so a sealed value is
// useless under any other subject or purpose. These strings are part of the
// stored format: changing one makes every existing record unreadable.
func hubCredentialAAD(subject string) []byte    { return []byte(subject) }
func githubCredentialAAD(subject string) []byte { return []byte("github:" + subject) }
func githubOAuthStateAAD(subject string) []byte { return []byte("github-oauth:" + subject) }

var (
	keyringMu        sync.RWMutex
	installedKeyring *keyring.Ring
)

// UseKeyring installs the keyring the gateway built at startup. Until it is
// called (tests, tools), a static keyring is built from KIONGA_CREDENTIAL_KEY
// on each use, which is how the gateway behaved before providers existed.
func UseKeyring(ring *keyring.Ring) {
	keyringMu.Lock()
	installedKeyring = ring
	keyringMu.Unlock()
}

func credentialKeyring() *keyring.Ring {
	keyringMu.RLock()
	ring := installedKeyring
	keyringMu.RUnlock()
	if ring != nil {
		return ring
	}
	static, _ := keyring.NewStatic(os.Getenv(keyring.EnvCredentialKey))
	ring, _ = keyring.NewRing(keyring.ProviderStatic, static, nil)
	return ring
}

// credentialsConfigured reports whether new credentials can be sealed.
func credentialsConfigured() bool { return credentialKeyring().Configured() }

var errCredentialKeyMissing = errors.New("Account connections require KIONGA_CREDENTIAL_KEY (base64-encoded 32-byte key) on the gateway")
var errCredentialServiceUnavailable = errors.New("Credential encryption is temporarily unavailable. Please retry")

// sealCredential seals plaintext for aad with the configured provider.
func sealCredential(ctx context.Context, aad []byte, plaintext string) (string, error) {
	sealed, err := credentialKeyring().Seal(ctx, []byte(plaintext), aad)
	if errors.Is(err, keyring.ErrNotConfigured) {
		return "", errCredentialKeyMissing
	}
	if err != nil {
		log.Printf("credential keyring: seal failed: %v", err)
		return "", errCredentialServiceUnavailable
	}
	return string(sealed), nil
}

// errCredentialUnreadable means the record is malformed, bound to someone
// else, or sealed with a key this gateway no longer has: the user must
// reconnect. Any other error is transient (OpenBao unavailable).
var errCredentialUnreadable = errors.New("credential unreadable")

func openCredential(ctx context.Context, aad []byte, ciphertext string) (string, error) {
	plain, err := credentialKeyring().Open(ctx, []byte(ciphertext), aad)
	switch {
	case err == nil:
		return string(plain), nil
	case errors.Is(err, keyring.ErrNotConfigured):
		return "", errCredentialKeyMissing
	case errors.Is(err, keyring.ErrOpen), errors.Is(err, keyring.ErrStaticUnavailable), errors.Is(err, keyring.ErrTransitUnavailable):
		if !errors.Is(err, keyring.ErrOpen) {
			log.Printf("credential keyring: %v", err)
		}
		return "", errCredentialUnreadable
	default:
		log.Printf("credential keyring: open failed: %v", err)
		return "", errCredentialServiceUnavailable
	}
}

// SealedCredentialAAD is the associated data a stored credential record of
// kind was sealed with, for the rewrap command. ok is false for an unknown
// kind.
func SealedCredentialAAD(kind, subject string) (aad []byte, ok bool) {
	switch kind {
	case store.HubAccountKind:
		return hubCredentialAAD(subject), true
	case store.GitHubAccountKind:
		return githubCredentialAAD(subject), true
	}
	return nil, false
}
