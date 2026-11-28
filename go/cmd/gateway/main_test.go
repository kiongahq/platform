package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/keyring/keyringtest"
)

// TestGatewayProcess runs main() when the parent test asks for it.
func TestGatewayProcess(t *testing.T) {
	if os.Getenv("KIONGA_GATEWAY_TEST_PROCESS") != "1" {
		t.Skip("helper process")
	}
	os.Args = []string{"gateway"}
	main()
}

func runGateway(t *testing.T, env ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGatewayProcess$")
	cmd.Env = append([]string{"KIONGA_GATEWAY_TEST_PROCESS=1", "PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
		"KIONGA_DATA_PATH=" + filepath.Join(t.TempDir(), "platform.json")}, env...)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	return output.String(), err
}

func TestGatewayFailsClosedWhenOpenBaoIsUnreachable(t *testing.T) {
	out, err := runGateway(t, "KIONGA_KEY_PROVIDER=openbao", "OPENBAO_ADDR=http://127.0.0.1:1", "OPENBAO_TRANSIT_KEY=kionga-credential",
		"OPENBAO_AUTH_ROLE=kionga-gateway", "OPENBAO_AUTH_JWT_FILE="+keyringtest.WriteJWT(t, "jwt"))
	if err == nil || !strings.Contains(out, "credential keyring: KIONGA_KEY_PROVIDER=openbao: openbao login failed: OpenBao could not be reached") {
		t.Fatalf("gateway did not fail closed (err %v):\n%s", err, out)
	}
}

func TestGatewayFailsClosedWhenTransitKeyIgnoresContext(t *testing.T) {
	stub := keyringtest.New(t)
	stub.Set(func(s *keyringtest.Bao) { s.Derived = false })
	out, err := runGateway(t, "KIONGA_KEY_PROVIDER=openbao", "OPENBAO_ADDR="+stub.URL(), "OPENBAO_TRANSIT_KEY=kionga-credential",
		"OPENBAO_AUTH_ROLE=kionga-gateway", "OPENBAO_AUTH_JWT_FILE="+keyringtest.WriteJWT(t, stub.JWT))
	if err == nil || !strings.Contains(out, "derived=true") {
		t.Fatalf("gateway accepted a non-derived key (err %v):\n%s", err, out)
	}
}

// With OpenBao healthy and no credential key the gateway gets past the
// keyring (this test binary has no built frontend, so it stops at that check).
func TestGatewayStartsKeyringWithoutCredentialKey(t *testing.T) {
	stub := keyringtest.New(t)
	out, _ := runGateway(t, "KIONGA_KEY_PROVIDER=openbao", "OPENBAO_ADDR="+stub.URL(), "OPENBAO_TRANSIT_KEY=kionga-credential",
		"OPENBAO_AUTH_ROLE=kionga-gateway", "OPENBAO_AUTH_JWT_FILE="+keyringtest.WriteJWT(t, stub.JWT))
	if !strings.Contains(out, "credential keyring: OpenBao transit; no credential key in the gateway") {
		t.Fatalf("keyring did not start:\n%s", out)
	}
}
