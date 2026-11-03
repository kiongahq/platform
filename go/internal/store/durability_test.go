package store

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kiongahq/platform/pkg/api"
)

// Regression for #36: an unreadable data file must fail Open and stay
// byte-for-byte unchanged instead of being replaced by the demo seed.
func TestOpenRefusesCorruptDataFileAndLeavesItUntouched(t *testing.T) {
	for name, contents := range map[string][]byte{
		"truncated": []byte(`{"projects": [`),
		"empty":     {},
		"not json":  []byte("\x00\x00garbage"),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "platform.json")
			if err := os.WriteFile(path, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if s, err := Open(path); err == nil || s != nil {
				t.Fatalf("Open(%s) = %v, %v; want an error", name, s, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, contents) {
				t.Fatalf("data file changed: %q (err %v), want %q", after, err, contents)
			}
		})
	}
}

func TestOpenRefusesUnreadableDataFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file permissions do not restrict this user")
	}
	path := filepath.Join(t.TempDir(), "platform.json")
	if err := os.WriteFile(path, []byte(`{"projects":[]}`), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open must fail for an unreadable data file")
	}
	_ = os.Chmod(path, 0o600)
	if raw, _ := os.ReadFile(path); string(raw) != `{"projects":[]}` {
		t.Fatalf("unreadable data file was overwritten: %q", raw)
	}
}

func TestNewPanicsRatherThanSeedingOverCorruptData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.json")
	if err := os.WriteFile(path, []byte(`{"projects": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("New must panic on a corrupt data file")
		}
	}()
	New(path)
}

func TestOpenSeedsOnlyAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "platform.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if projects := s.Projects(); len(projects) != 1 || projects[0].ID != "prj-demo" {
		t.Fatalf("missing file must seed the demo project, got %#v", projects)
	}
	// A valid file whose projects were all deleted is not re-seeded.
	if err := os.WriteFile(path, []byte(`{"projects":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if projects := reopened.Projects(); len(projects) != 0 {
		t.Fatalf("an existing empty store must stay empty, got %#v", projects)
	}
}

func TestPersistSyncsBeforeRenameAndKeepsBackup(t *testing.T) {
	var synced []string
	original := syncFile
	syncFile = func(file *os.File) error {
		synced = append(synced, filepath.Base(file.Name()))
		return original(file)
	}
	t.Cleanup(func() { syncFile = original })
	dir := t.TempDir()
	path := filepath.Join(dir, "platform.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	synced = nil
	if _, err := s.CreateProject(api.CreateProjectRequest{Name: "Churn risk"}, "tester"); err != nil {
		t.Fatal(err)
	}
	if len(synced) < 2 || synced[0] != "platform.json.tmp" || synced[len(synced)-1] != filepath.Base(dir) {
		t.Fatalf("persist must fsync the temp file and then the directory, synced %v", synced)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil || !bytes.Equal(backup, first) {
		t.Fatalf(".bak must hold the previous version (err %v)", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}
