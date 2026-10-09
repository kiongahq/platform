package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// LocalAccountKind stores password logins for local (non-OIDC) deployments.
// They let an administrator create real non-admin identities on a laptop or
// single VM without an identity provider. Authorization still comes only from
// the subject's UserAccess profile.
const LocalAccountKind = "local_account"

// MinLocalPasswordLength follows NIST SP 800-63B's floor for memorized
// secrets chosen by a user.
const MinLocalPasswordLength = 12

type LocalAccount struct {
	Subject      string    `json:"subject"`
	PasswordHash string    `json:"password_hash"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// dummyHash keeps VerifyLocalPassword's timing similar for unknown subjects.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("kionga-timing-equalizer"), bcrypt.DefaultCost)

var ErrWeakPassword = fmt.Errorf("password must be at least %d characters", MinLocalPasswordLength)

func validLocalSubject(subject string) bool {
	if subject == "" || len(subject) > 128 {
		return false
	}
	for _, r := range subject {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._@-", r)) {
			return false
		}
	}
	return true
}

// SetLocalPassword creates or replaces the password for subject.
func SetLocalPassword(docs Documents, subject, password, actor string) error {
	if !validLocalSubject(subject) {
		return errors.New("subject may contain only letters, digits, '.', '_', '@' and '-'")
	}
	if len(password) < MinLocalPasswordLength {
		return ErrWeakPassword
	}
	if len(password) > 72 {
		return errors.New("password must be at most 72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = UpdateDoc(docs, LocalAccountKind, subject, func(current LocalAccount, exists bool) (LocalAccount, error) {
		if !exists {
			current = LocalAccount{Subject: subject, CreatedAt: now}
		}
		current.PasswordHash, current.UpdatedAt = string(hash), now
		return current, nil
	}, "local_account.password_set", actor)
	return err
}

// VerifyLocalPassword reports whether password matches subject's account.
func VerifyLocalPassword(docs Documents, subject, password string) bool {
	account, err := GetDoc[LocalAccount](docs, LocalAccountKind, subject)
	if err != nil || account.PasswordHash == "" {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(password)) == nil
}

// LocalAccountExists reports whether a password login exists for subject.
func LocalAccountExists(docs Documents, subject string) bool {
	_, err := docs.GetDocument(LocalAccountKind, subject)
	return err == nil
}
