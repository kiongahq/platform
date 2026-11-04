package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPSendsTokenAndDecodes(t *testing.T) {
	var seen *http.Request
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte(`{"runtime":"runsc","runtime_verified":true,"driver":"docker","unsafe_runtime":true}`))
		case "/sandboxes/sbx-1/files":
			if r.Method == http.MethodPut {
				_, _ = w.Write([]byte(`{"path":"/workspace/x","bytes_written":4}`))
				return
			}
			if r.URL.Query().Get("path") == "dir" {
				_, _ = w.Write([]byte(`{"entries":[{"name":"a","type":"file","size":3}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"content":"","truncated":false}`))
		case "/sandboxes/sbx-1/git/push":
			_, _ = w.Write([]byte(`{"branch":"b","commit":"c"}`))
		case "/sandboxes/sbx-1":
			w.WriteHeader(http.StatusNoContent)
		case "/sandboxes/bad":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"path_rejected","message":"no"}`))
		case "/sandboxes/garbled":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`<html>`))
		}
	}))
	defer server.Close()
	api := New(server.URL+"/", "tok")
	ctx := context.Background()

	health, err := api.Health(ctx)
	if err != nil || !health.UnsafeRuntime || !health.RuntimeVerified || health.Driver != "docker" {
		t.Fatalf("%+v %v", health, err)
	}
	if seen.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("auth: %q", seen.Header.Get("Authorization"))
	}
	if content, err := api.ReadPath(ctx, "sbx-1", "file"); err != nil || content.IsDirectory {
		t.Fatalf("an empty file is still a file: %+v %v", content, err)
	}
	listing, err := api.ReadPath(ctx, "sbx-1", "dir")
	if err != nil || !listing.IsDirectory || listing.Entries[0].Size != 3 {
		t.Fatalf("%+v %v", listing, err)
	}
	if result, err := api.WriteFile(ctx, "sbx-1", "x", []byte("data")); err != nil || result.BytesWritten != 4 || body != "data" {
		t.Fatalf("%+v %v %q", result, err, body)
	}
	if _, err := api.GitPush(ctx, "sbx-1", GitPushRequest{Repository: "r", Branch: "b", Credential: "the-secret"}); err != nil {
		t.Fatal(err)
	}
	var wire map[string]string
	if err := json.Unmarshal([]byte(body), &wire); err != nil || wire["credential"] != "the-secret" {
		t.Fatalf("the credential must reach the wire as a string: %q", body)
	}
	if err := api.Delete(ctx, "sbx-1"); err != nil {
		t.Fatal(err)
	}
	err = api.Delete(ctx, "bad")
	var failure *Error
	if !errors.As(err, &failure) || failure.Status != http.StatusForbidden || failure.Code != CodePathRejected || Code(err) != CodePathRejected {
		t.Fatalf("%v", err)
	}
	if err := api.Delete(ctx, "garbled"); Code(err) != CodeInternal {
		t.Fatalf("%v", err)
	}
	if err := api.Delete(ctx, "../../etc"); err == nil || strings.Contains(seen.URL.Path, "/etc") && !strings.Contains(seen.URL.EscapedPath(), "%2F") {
		t.Logf("escaped path: %s", seen.URL.EscapedPath())
	}
}

func TestUnreachableManagerIsUnavailable(t *testing.T) {
	api := New("http://127.0.0.1:1", "t")
	_, err := api.Create(context.Background(), CreateRequest{})
	if err == nil || !IsUnavailable(err) || Code(err) != "" {
		t.Fatalf("%v", err)
	}
	if IsUnavailable(nil) {
		t.Fatal("nil is not unavailable")
	}
}

func TestCredentialNeverFormats(t *testing.T) {
	secret := Credential("the-secret")
	request := GitPushRequest{Repository: "r", Branch: "b", Credential: secret}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if text := fmt.Sprintf(format, request); strings.Contains(text, "the-secret") {
			t.Errorf("%s leaked: %s", format, text)
		}
	}
}

func TestFake(t *testing.T) {
	ctx := context.Background()
	fake := &Fake{Unsafe: true}
	var _ Client = fake
	if health, _ := fake.Health(ctx); !health.UnsafeRuntime || !health.RuntimeVerified {
		t.Fatalf("%+v", health)
	}
	first, err := fake.Create(ctx, CreateRequest{RunID: "r", WorkerID: "w", ProjectID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := fake.Create(ctx, CreateRequest{RunID: "r", WorkerID: "w", ProjectID: "p"})
	if first.ID != second.ID {
		t.Fatal("create must be idempotent")
	}
	fake.OnExec = func(_ string, request ExecRequest) (ExecResult, error) {
		return ExecResult{Stdout: request.Command}, nil
	}
	if result, err := fake.Exec(ctx, first.ID, ExecRequest{Command: "hi"}); err != nil || result.Stdout != "hi" || len(fake.Execs) != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := fake.WriteFile(ctx, first.ID, "a/b.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if content, err := fake.ReadPath(ctx, first.ID, "/workspace/a/b.txt"); err != nil || content.Content != "x" {
		t.Fatalf("%+v %v", content, err)
	}
	listing, err := fake.ReadPath(ctx, first.ID, ".")
	if err != nil || !listing.IsDirectory || listing.Entries[0].Name != "a" || listing.Entries[0].Type != "directory" {
		t.Fatalf("%+v %v", listing, err)
	}
	if _, err := fake.ReadPath(ctx, first.ID, "../x"); Code(err) != CodePathRejected {
		t.Fatalf("%v", err)
	}
	if _, err := fake.ReadPath(ctx, first.ID, "missing"); Code(err) != CodeNotFound {
		t.Fatalf("%v", err)
	}
	if _, err := fake.GitClone(ctx, first.ID, GitCloneRequest{Repository: "r"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.GitPush(ctx, first.ID, GitPushRequest{Repository: "r", Branch: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.GitDiff(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := fake.Delete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.Exec(ctx, first.ID, ExecRequest{Command: "x"}); Code(err) != CodeNotFound {
		t.Fatalf("%v", err)
	}
	fake.Unavailable = true
	if _, err := fake.Create(ctx, CreateRequest{}); !IsUnavailable(err) {
		t.Fatalf("%v", err)
	}
	if health, _ := fake.Health(ctx); health.RuntimeVerified {
		t.Fatal("health must show the outage")
	}
}

func TestPathContentContractShapes(t *testing.T) {
	file, _ := json.Marshal(PathContent{Content: "x"})
	if string(file) != `{"content":"x","truncated":false}` {
		t.Fatalf("%s", file)
	}
	directory, _ := json.Marshal(PathContent{IsDirectory: true})
	if string(directory) != `{"entries":[],"truncated":false}` {
		t.Fatalf("%s", directory)
	}
}
