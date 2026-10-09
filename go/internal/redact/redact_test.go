package redact

import (
	"strings"
	"sync"
	"testing"
)

func TestRedactsCredentialShapes(t *testing.T) {
	cases := map[string]string{
		"aws key AKIAABCDEFGHIJKLMNOP in log":                                    "AKIAABCDEFGHIJKLMNOP",
		"Authorization: Bearer abcdefghijklmnop1234567890":                       "abcdefghijklmnop1234567890",
		"token hf_abcdefghijklmnopqrstuvwxyz12":                                  "hf_abcdefghijklmnopqrstuvwxyz12",
		"OPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuvwx":                        "sk-proj-abcdefghijklmnopqrstuvwx",
		"github ghp_abcdefghijklmnopqrstuvwxyz0123456789":                        "ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"db postgres://mlaiops:hunter2-secret@postgres:5432/x":                   "hunter2-secret",
		`{"password": "correct horse battery"}`:                                  "correct horse battery",
		"MY_SECRET=plainvalue123 next":                                           "plainvalue123",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY-----": "MIIabc",
		"jwt eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.c2lnbmF0dXJlLXZhbHVl":      "eyJzdWIiOiJ1c2VyIn0",
	}
	for input, secret := range cases {
		out := With(input, nil)
		if strings.Contains(out, secret) || !strings.Contains(out, Mask) {
			t.Errorf("%q → %q still contains %q", input, out, secret)
		}
	}
}

func TestRedactsKnownValuesAndKeepsOrdinaryText(t *testing.T) {
	out := With("loaded model with key s3cr3t-value-123 from cache", []string{"s3cr3t-value-123", "short"})
	if strings.Contains(out, "s3cr3t-value-123") {
		t.Fatalf("known secret leaked: %s", out)
	}
	plain := "epoch 3/10 loss=0.231 accuracy=0.94 tokens processed: 1200"
	if got := With(plain, []string{"short"}); got != plain {
		t.Fatalf("ordinary text altered: %q", got)
	}
}

func TestEnvironmentSecretsAreLearned(t *testing.T) {
	t.Setenv("KIONGA_TEST_SECRET_TOKEN", "envsecretvalue42")
	knownOnce = sync.Once{}
	knownValue = nil
	if out := String("value envsecretvalue42 printed"); strings.Contains(out, "envsecretvalue42") {
		t.Fatalf("environment secret leaked: %s", out)
	}
}
