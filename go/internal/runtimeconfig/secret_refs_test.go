package runtimeconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func secretFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// setenvClean registers a variable Load will set, so it does not leak between tests.
func setenvClean(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		t.Setenv(name, "")
	}
}

func TestReferenceFilesResolve(t *testing.T) {
	cases := []struct{ name, listIn string }{
		{"KIONGA_KB_SECRET_DOCS", ""},
		{"KIONGA_CONNECTION_SECRET_CRM", ""},
		{"KIONGA_FEATURE_SECRET_FEAST", ""},
		{"KIONGA_STORAGE_SECRET_LAKE", ""},
		{"KIONGA_PLATFORM_SECRET_PREFECT", ""},
		{"KIONGA_QDRANT_KEY", ""},
		{"KIONGA_KNOWLEDGE_DSN", ""},
		{"KIONGA_AGENT_API_TOKEN", ""},
		{"TEAM_QDRANT_KEY", "KIONGA_KB_SECRET_REFS"},
		{"TEAM_CRM_TOKEN", "KIONGA_CONNECTION_SECRET_REFS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setenvClean(t, tc.name)
			if tc.listIn != "" {
				t.Setenv(tc.listIn, "OTHER, "+tc.name)
			}
			t.Setenv(tc.name+"_FILE", secretFile(t, "file-value\r\n"))
			if err := Load(); err != nil {
				t.Fatal(err)
			}
			if got := os.Getenv(tc.name); got != "file-value" {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestSecretNameInAllowlistIsNeverLoadedThroughIt(t *testing.T) {
	// MLFLOW_TOKEN is loaded by the SecretNames loop, not by a reference list.
	t.Setenv("KIONGA_KB_SECRET_REFS", "DATABASE_URL,KIONGA_DESIGNER_RUNNER_TOKEN")
	setenvClean(t, "KIONGA_DESIGNER_RUNNER_TOKEN")
	t.Setenv("KIONGA_DESIGNER_RUNNER_TOKEN_FILE", secretFile(t, "must-not-load"))
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("KIONGA_DESIGNER_RUNNER_TOKEN"); got != "" {
		t.Fatalf("a gateway secret loaded through a list: %q", got)
	}
	if KnowledgeSecrets.Permits("env:DATABASE_URL") {
		t.Fatal("a gateway secret is referenceable through a list")
	}
}

func TestUnlistedFileIsIgnored(t *testing.T) {
	setenvClean(t, "SOME_OTHER_THING")
	t.Setenv("SOME_OTHER_THING_FILE", secretFile(t, "x"))
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SOME_OTHER_THING") != "" {
		t.Fatal("an unlisted variable was loaded")
	}
}

func TestReferenceFileErrors(t *testing.T) {
	t.Run("both forms", func(t *testing.T) {
		t.Setenv("KIONGA_KB_SECRET_DOCS", "value")
		t.Setenv("KIONGA_KB_SECRET_DOCS_FILE", secretFile(t, "file"))
		if err := Load(); err == nil || !strings.Contains(err.Error(), "set only one") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("empty file", func(t *testing.T) {
		setenvClean(t, "KIONGA_KB_SECRET_DOCS")
		t.Setenv("KIONGA_KB_SECRET_DOCS_FILE", secretFile(t, "\n"))
		if err := Load(); err == nil || !strings.Contains(err.Error(), "is empty") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		setenvClean(t, "KIONGA_KB_SECRET_DOCS")
		t.Setenv("KIONGA_KB_SECRET_DOCS_FILE", filepath.Join(t.TempDir(), "absent"))
		if err := Load(); err == nil || !strings.Contains(err.Error(), "cannot read") {
			t.Fatalf("%v", err)
		}
	})
}

func TestConfigurationVariablesAreNotReferences(t *testing.T) {
	t.Setenv("KIONGA_KB_SECRET_REFS", "A,B")
	t.Setenv("KIONGA_CONNECTION_SECRET_REFS", "A,B")
	for scope, ref := range map[*SecretScope]string{
		&KnowledgeSecrets:  "env:KIONGA_KB_SECRET_REFS",
		&ConnectionSecrets: "env:KIONGA_CONNECTION_SECRET_REFS",
	} {
		if scope.Permits(ref) {
			t.Errorf("%s is permitted", ref)
		}
		if scope.Value(ref) != "" {
			t.Errorf("%s resolved", ref)
		}
	}
	// A *_FILE variable holds a path, which is no one's business either.
	t.Setenv("KIONGA_KB_SECRET_DOCS_FILE", "/run/secrets/docs")
	if KnowledgeSecrets.Permits("env:KIONGA_KB_SECRET_DOCS_FILE") {
		t.Error("a _FILE path is referenceable")
	}
}

func TestAllowlistCannotBeLoadedFromFile(t *testing.T) {
	t.Setenv("KIONGA_KB_SECRET_REFS", "")
	t.Setenv("KIONGA_KB_SECRET_REFS_FILE", secretFile(t, "DATABASE_URL"))
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("KIONGA_KB_SECRET_REFS") != "" {
		t.Fatal("a reference list was loaded from a file")
	}
}

func TestConfigFileRejectsReferenceVariables(t *testing.T) {
	for _, key := range []string{"KIONGA_KB_SECRET_DOCS", "KIONGA_KB_SECRET_REFS", "KIONGA_AGENT_API_TOKEN"} {
		t.Setenv("KIONGA_CONFIG_FILE", secretFile(t, `{"`+key+`":"x"}`))
		if err := Load(); err == nil {
			t.Errorf("%s accepted from the config file", key)
		}
	}
}
