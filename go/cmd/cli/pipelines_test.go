package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/httpapi"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// The CLI round-trips a definition through the real API handler.
func TestPipelineValidateApplyExport(t *testing.T) {
	data := store.New()
	project, _ := data.CreateProject(api.CreateProjectRequest{Name: "CLI project"}, "admin")
	handler := httpapi.New(data, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Subject: "admin", Roles: []string{auth.RoleAdmin}})))
	}))
	defer server.Close()
	dir := t.TempDir()
	good := filepath.Join(dir, "flow.kionga.yaml")
	text := "apiVersion: kionga.dev/v1\nkind: Pipeline\nmetadata: {name: cli-flow, project: " + project.ID + ", version: \"1\"}\nspec:\n  nodes:\n    - {id: a, type: container, image: busybox}\n    - {id: b, type: container, image: busybox, dependsOn: [a]}\n"
	_ = os.WriteFile(good, []byte(text), 0o600)
	bad := filepath.Join(dir, "bad.kionga.yaml")
	_ = os.WriteFile(bad, []byte(strings.Replace(text, "dependsOn: [a]", "dependsOn: [missing]", 1)), 0o600)

	var out, errs bytes.Buffer
	if code := pipelineFile(server.Client(), server.URL, []string{"validate", bad}, &out, &errs); code != 1 || !strings.Contains(errs.String(), "line 7 (node b)") {
		t.Fatalf("validate bad: %d %q", code, errs.String())
	}
	out.Reset()
	if code := pipelineFile(server.Client(), server.URL, []string{"validate", good}, &out, &errs); code != 0 || !strings.Contains(out.String(), "valid: 2 stage(s)") {
		t.Fatalf("validate good: %d %q", code, out.String())
	}
	out.Reset()
	if code := pipelineFile(server.Client(), server.URL, []string{"apply", good}, &out, &errs); code != 0 || !strings.Contains(out.String(), "revision 1") {
		t.Fatalf("apply: %d %q %q", code, out.String(), errs.String())
	}
	id := strings.Fields(out.String())[0]
	out.Reset()
	if code := pipelineFile(server.Client(), server.URL, []string{"export", id}, &out, &errs); code != 0 || !strings.Contains(out.String(), "name: cli-flow") {
		t.Fatalf("export: %d %q", code, out.String())
	}
	exported := filepath.Join(dir, "exported.kionga.yaml")
	_ = os.WriteFile(exported, out.Bytes(), 0o600)
	out.Reset()
	if code := pipelineFile(server.Client(), server.URL, []string{"apply", exported, id, "no-op"}, &out, &errs); code != 0 || !strings.Contains(out.String(), "revision 1") {
		t.Fatalf("re-applying exported YAML must not create a revision: %d %q", code, out.String())
	}
}
