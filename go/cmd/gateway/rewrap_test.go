package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/keyring"
	"github.com/kiongahq/platform/internal/keyring/keyringtest"
	"github.com/kiongahq/platform/internal/store"
)

func TestRewrapCommandDryRunThenApply(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	static, _ := keyring.NewStatic(key)
	dataPath := filepath.Join(t.TempDir(), "platform.json")
	repo := store.New(dataPath)
	sealed, _ := static.Seal(context.Background(), []byte("hf_alice"), []byte("alice"))
	if err := repo.SaveHubAccount("alice", store.HubAccount{Username: "alice", Ciphertext: string(sealed)}); err != nil {
		t.Fatal(err)
	}
	stub := keyringtest.New(t)
	t.Setenv("DATABASE_URL", "")
	t.Setenv("KIONGA_DATA_PATH", dataPath)
	t.Setenv("KIONGA_CREDENTIAL_KEY", key)
	t.Setenv("KIONGA_KEY_PROVIDER", "openbao")
	t.Setenv("OPENBAO_ADDR", stub.URL())
	t.Setenv("OPENBAO_TRANSIT_KEY", "kionga-credential")
	t.Setenv("OPENBAO_AUTH_ROLE", "kionga-gateway")
	t.Setenv("OPENBAO_AUTH_JWT_FILE", keyringtest.WriteJWT(t, stub.JWT))

	before, _ := os.ReadFile(dataPath)
	var out, errOut bytes.Buffer
	dry := filepath.Join(t.TempDir(), "dry")
	if code := runRewrap([]string{"--job-dir", dry}, &out, &errOut); code != 0 {
		t.Fatalf("dry run exit %d: %s", code, errOut.String())
	}
	if after, _ := os.ReadFile(dataPath); !bytes.Equal(before, after) {
		t.Fatal("dry run changed the data file")
	}
	if !strings.Contains(out.String(), "1 to convert") {
		t.Fatalf("output: %s", out.String())
	}

	applied := filepath.Join(t.TempDir(), "apply")
	out.Reset()
	if code := runRewrap([]string{"--apply", "--job-dir", applied}, &out, &errOut); code != 0 {
		t.Fatalf("apply exit %d: %s", code, errOut.String())
	}
	account, _ := store.New(dataPath).HubAccount("alice")
	if !keyring.IsTransit([]byte(account.Ciphertext)) {
		t.Fatalf("not converted: %s", keyring.Format([]byte(account.Ciphertext)))
	}
	for _, name := range []string{"snapshot.jsonl", "applied.jsonl", "progress.log", "report.md", "before_after.csv"} {
		if _, err := os.Stat(filepath.Join(applied, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if code := runRewrap([]string{"--restore", filepath.Join(applied, "applied.jsonl")}, &out, &errOut); code != 0 {
		t.Fatalf("restore exit %d: %s", code, errOut.String())
	}
	if account, _ := store.New(dataPath).HubAccount("alice"); account.Ciphertext != string(sealed) {
		t.Fatal("restore did not bring back the static record")
	}
}

func TestRewrapCommandFailsClosedWithoutOpenBao(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("KIONGA_DATA_PATH", filepath.Join(t.TempDir(), "platform.json"))
	t.Setenv("KIONGA_KEY_PROVIDER", "openbao")
	t.Setenv("OPENBAO_ADDR", "http://127.0.0.1:1")
	t.Setenv("OPENBAO_TRANSIT_KEY", "kionga-credential")
	t.Setenv("OPENBAO_AUTH_ROLE", "kionga-gateway")
	t.Setenv("OPENBAO_AUTH_JWT_FILE", keyringtest.WriteJWT(t, "jwt"))
	var out, errOut bytes.Buffer
	if code := runRewrap(nil, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "could not be reached") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if code := runRewrap([]string{"--bogus"}, &out, &errOut); code != 2 {
		t.Fatalf("bad flag exit %d", code)
	}
}
