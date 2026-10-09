package store

import (
	"strings"
	"testing"
)

func TestLocalPasswords(t *testing.T) {
	s := New()
	if err := SetLocalPassword(s, "alice", "short", "admin"); err == nil {
		t.Fatal("weak password accepted")
	}
	if err := SetLocalPassword(s, "bad subject!", "long-enough-password", "admin"); err == nil {
		t.Fatal("invalid subject accepted")
	}
	if err := SetLocalPassword(s, "alice", "long-enough-password", "admin"); err != nil {
		t.Fatal(err)
	}
	if !VerifyLocalPassword(s, "alice", "long-enough-password") || VerifyLocalPassword(s, "alice", "wrong-password-xx") || VerifyLocalPassword(s, "bob", "long-enough-password") {
		t.Fatal("password verification incorrect")
	}
	raw, _ := s.GetDocument(LocalAccountKind, "alice")
	if strings.Contains(string(raw), "long-enough-password") {
		t.Fatal("plaintext password stored")
	}
	if !LocalAccountExists(s, "alice") || LocalAccountExists(s, "bob") {
		t.Fatal("existence check incorrect")
	}
}
