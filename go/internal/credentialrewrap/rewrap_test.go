package credentialrewrap

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/keyring"
	"github.com/kiongahq/platform/internal/keyring/keyringtest"
	"github.com/kiongahq/platform/internal/store"
)

var legacyKey = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

func aad(kind, subject string) ([]byte, bool) {
	switch kind {
	case store.HubAccountKind:
		return []byte(subject), true
	case store.GitHubAccountKind:
		return []byte("github:" + subject), true
	}
	return nil, false
}

func id(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:])
}

type fixture struct {
	path   string
	repo   *store.Store
	static *keyring.Static
}

type ghDoc struct {
	Login      string `json:"login"`
	Ciphertext string `json:"ciphertext"`
}

// newFixture writes static records the way the gateway always has: a Hugging
// Face token for alice and bob and a GitHub token for alice.
func newFixture(t *testing.T) fixture {
	t.Helper()
	static, err := keyring.NewStatic(legacyKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	repo := store.New(path)
	ctx := context.Background()
	for subject, token := range map[string]string{"alice": "hf_alice", "bob": "hf_bob"} {
		sealed, _ := static.Seal(ctx, []byte(token), []byte(subject))
		if err := repo.SaveHubAccount(subject, store.HubAccount{Username: subject, Ciphertext: string(sealed)}); err != nil {
			t.Fatal(err)
		}
	}
	sealed, _ := static.Seal(ctx, []byte("ghp_alice"), []byte("github:alice"))
	if _, err := store.UpdateDoc(repo, store.GitHubAccountKind, id("alice"), func(ghDoc, bool) (ghDoc, error) {
		return ghDoc{Login: "alice-gh", Ciphertext: string(sealed)}, nil
	}, "github.account.updated", "alice"); err != nil {
		t.Fatal(err)
	}
	return fixture{path: path, repo: repo, static: static}
}

func openBaoRing(t *testing.T, stub *keyringtest.Bao, withLegacy bool) *keyring.Ring {
	t.Helper()
	cfg := keyring.OpenBaoConfig{Addr: stub.URL(), TransitKey: "kionga-credential", AuthRole: "kionga-gateway", JWTFile: keyringtest.WriteJWT(t, stub.JWT), AllowHTTP: true, Timeout: 2 * time.Second}
	client, err := keyring.NewOpenBao(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var static *keyring.Static
	if withLegacy {
		static, _ = keyring.NewStatic(legacyKey)
	}
	ring, err := keyring.NewRing(keyring.ProviderOpenBao, static, client)
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func options(t *testing.T, apply bool) Options {
	return Options{Apply: apply, JobDir: filepath.Join(t.TempDir(), "job"), AAD: aad, ProgressEvery: time.Nanosecond}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDryRunChangesNothing(t *testing.T) {
	f := newFixture(t)
	stub := keyringtest.New(t)
	before := readFile(t, f.path)
	opts := options(t, false)
	result, err := Run(context.Background(), f.repo, openBaoRing(t, stub, true), opts)
	if err != nil {
		t.Fatal(err)
	}
	if after := readFile(t, f.path); after != before {
		t.Fatal("dry run modified the store")
	}
	if _, _, encrypts, _ := stub.Counts(); encrypts != 0 {
		t.Fatalf("dry run sealed %d values", encrypts)
	}
	if result.Targets != 3 || result.Counts[StatusWould] != 3 || result.Incomplete() {
		t.Fatalf("plan: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(opts.JobDir, snapshotFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry run wrote a snapshot")
	}
	report := readFile(t, filepath.Join(opts.JobDir, reportFile))
	if !strings.Contains(report, "Ready: all 3 records") || !strings.Contains(report, "dry run") {
		t.Fatalf("report:\n%s", report)
	}
	assertNoSecrets(t, f, opts.JobDir, reportFile, csvFile, progressFile)
}

// assertNoSecrets checks report files carry neither tokens nor ciphertext.
func assertNoSecrets(t *testing.T, f fixture, dir string, files ...string) {
	t.Helper()
	records, _ := f.repo.SealedCredentials()
	for _, name := range files {
		content := readFile(t, filepath.Join(dir, name))
		for _, secret := range []string{"hf_alice", "hf_bob", "ghp_alice"} {
			if strings.Contains(content, secret) {
				t.Fatalf("%s contains a token", name)
			}
		}
		for _, record := range records {
			if strings.Contains(content, record.Ciphertext) {
				t.Fatalf("%s contains ciphertext", name)
			}
		}
	}
}

// snapshotCheck fails the test if a write happens before the snapshot holds
// every row the run is about to change.
type snapshotCheck struct {
	*store.Store
	t        *testing.T
	snapshot string
	want     map[string]string
	writes   int
}

func (s *snapshotCheck) ReplaceSealedCredential(kind, recordID, old, replacement string) error {
	if s.writes == 0 {
		file, err := os.Open(s.snapshot)
		if err != nil {
			s.t.Fatalf("write before snapshot: %v", err)
		}
		got := map[string]string{}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			var entry snapshotEntry
			_ = json.Unmarshal(scanner.Bytes(), &entry)
			got[entry.Kind+"/"+entry.ID] = entry.Ciphertext
		}
		file.Close()
		if len(got) != len(s.want) {
			s.t.Fatalf("snapshot has %d rows, want %d", len(got), len(s.want))
		}
		for k, v := range s.want {
			if got[k] != v {
				s.t.Fatalf("snapshot row %s differs", k)
			}
		}
		info, _ := os.Stat(s.snapshot)
		if info.Mode().Perm() != 0o600 {
			s.t.Fatalf("snapshot mode %v", info.Mode().Perm())
		}
	}
	s.writes++
	return s.Store.ReplaceSealedCredential(kind, recordID, old, replacement)
}

func TestApplyConvertsAfterSnapshotAndCanBeRestored(t *testing.T) {
	f := newFixture(t)
	stub := keyringtest.New(t)
	ring := openBaoRing(t, stub, true)
	ctx := context.Background()
	before, _ := f.repo.SealedCredentials()
	want := map[string]string{}
	for _, record := range before {
		want[record.Kind+"/"+record.ID] = record.Ciphertext
	}
	opts := options(t, true)
	checked := &snapshotCheck{Store: f.repo, t: t, snapshot: filepath.Join(opts.JobDir, snapshotFile), want: want}
	var console bytes.Buffer
	opts.Console = &console
	result, err := Run(ctx, checked, ring, opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts[StatusConverted] != 3 || result.Incomplete() || checked.writes != 3 {
		t.Fatalf("apply: %+v", result.Counts)
	}
	// Every record is now transit ciphertext and opens without the legacy key.
	endState := openBaoRing(t, stub, false)
	after, _ := f.repo.SealedCredentials()
	for _, record := range after {
		if keyring.Format([]byte(record.Ciphertext)) != "openbao:v1" {
			t.Fatalf("%s/%s not converted: %s", record.Kind, record.ID, keyring.Format([]byte(record.Ciphertext)))
		}
		data, _ := aad(record.Kind, record.Subject)
		if _, err := endState.Open(ctx, []byte(record.Ciphertext), data); err != nil {
			t.Fatalf("converted record does not open: %v", err)
		}
	}
	// Other fields survive.
	gh, _ := store.GetDoc[ghDoc](f.repo, store.GitHubAccountKind, id("alice"))
	if gh.Login != "alice-gh" {
		t.Fatalf("github login lost: %+v", gh)
	}
	progress := readFile(t, filepath.Join(opts.JobDir, progressFile))
	if !strings.Contains(progress, "credential rewrap (apply): 0.0% done") || !strings.Contains(progress, "100.0% done, ETA 0s | 3/3 rows") {
		t.Fatalf("progress log:\n%s", progress)
	}
	if !strings.Contains(console.String(), "tail -f "+filepath.Join(opts.JobDir, progressFile)) {
		t.Fatalf("console did not print the follow command:\n%s", console.String())
	}
	report := readFile(t, filepath.Join(opts.JobDir, reportFile))
	if !strings.Contains(report, "Worked: all 3 records") || !strings.Contains(report, "| hub_account | static | openbao:v1 | converted | 2 |") {
		t.Fatalf("report:\n%s", report)
	}
	csv := readFile(t, filepath.Join(opts.JobDir, csvFile))
	if !strings.HasPrefix(csv, "kind,record_id,before_format,after_format,status\n") || strings.Count(csv, ",static,openbao:v1,converted") != 3 {
		t.Fatalf("csv:\n%s", csv)
	}
	assertNoSecrets(t, f, opts.JobDir, reportFile, csvFile, progressFile)

	// A second run has nothing to do.
	again, err := Run(ctx, f.repo, ring, options(t, true))
	if err != nil || again.Targets != 0 || again.Counts[StatusCurrent] != 3 {
		t.Fatalf("second run: %+v %v", again, err)
	}

	// Restore puts the static ciphertext back.
	restored, skipped, err := Restore(f.repo, filepath.Join(opts.JobDir, appliedFile), nil)
	if err != nil || restored != 3 || skipped != 0 {
		t.Fatalf("restore: %d %d %v", restored, skipped, err)
	}
	back, _ := f.repo.SealedCredentials()
	for _, record := range back {
		if want[record.Kind+"/"+record.ID] != record.Ciphertext {
			t.Fatal("restore did not put the original ciphertext back")
		}
	}
}

func TestApplyLeavesUnconvertibleRecordsAlone(t *testing.T) {
	f := newFixture(t)
	stub := keyringtest.New(t)
	// No audited owner.
	sealed, _ := f.static.Seal(context.Background(), []byte("ghp_x"), []byte("github:orphan"))
	_, _ = store.UpdateDoc(f.repo, store.GitHubAccountKind, id("orphan"), func(ghDoc, bool) (ghDoc, error) {
		return ghDoc{Login: "o", Ciphertext: string(sealed)}, nil
	}, "github.account.updated", "someone-else")
	// Sealed under a different key.
	other, _ := keyring.NewStatic(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	foreign, _ := other.Seal(context.Background(), []byte("hf_carol"), []byte("carol"))
	_ = f.repo.SaveHubAccount("carol", store.HubAccount{Ciphertext: string(foreign)})

	opts := options(t, true)
	result, err := Run(context.Background(), f.repo, openBaoRing(t, stub, true), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts[StatusConverted] != 3 || result.Counts[StatusUnresolved] != 1 || result.Counts[StatusUnreadable] != 1 || !result.Incomplete() {
		t.Fatalf("counts: %+v", result.Counts)
	}
	if account, _ := f.repo.HubAccount("carol"); account.Ciphertext != string(foreign) {
		t.Fatal("unreadable record was rewritten")
	}
	if doc, _ := store.GetDoc[ghDoc](f.repo, store.GitHubAccountKind, id("orphan")); doc.Ciphertext != string(sealed) {
		t.Fatal("unresolved record was rewritten")
	}
	report := readFile(t, filepath.Join(opts.JobDir, reportFile))
	if !strings.Contains(report, "Incomplete: 2 of 5") || !strings.Contains(report, "no audited owner") {
		t.Fatalf("report:\n%s", report)
	}
}

func TestApplyRefusesOversizedSnapshotBeforeWriting(t *testing.T) {
	f := newFixture(t)
	stub := keyringtest.New(t)
	before := readFile(t, f.path)
	opts := options(t, true)
	opts.MaxRows = 2
	if _, err := Run(context.Background(), f.repo, openBaoRing(t, stub, true), opts); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
	opts.MaxRows, opts.MaxBytes = 0, 100
	if _, err := Run(context.Background(), f.repo, openBaoRing(t, stub, true), opts); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge for bytes, got %v", err)
	}
	if readFile(t, f.path) != before {
		t.Fatal("store changed")
	}
	if _, err := os.Stat(opts.JobDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("job directory created for a refused run")
	}
}

// racingStore simulates a user reconnecting between read and write.
type racingStore struct{ *store.Store }

func (r racingStore) ReplaceSealedCredential(kind, recordID, old, replacement string) error {
	if kind == store.HubAccountKind && recordID == id("bob") {
		_ = r.Store.SaveHubAccount("bob", store.HubAccount{Ciphertext: "vault:v1:reconnected"})
	}
	return r.Store.ReplaceSealedCredential(kind, recordID, old, replacement)
}

func TestApplyNeverOverwritesAConcurrentChange(t *testing.T) {
	f := newFixture(t)
	stub := keyringtest.New(t)
	result, err := Run(context.Background(), racingStore{f.repo}, openBaoRing(t, stub, true), options(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts[StatusChanged] != 1 || result.Counts[StatusConverted] != 2 {
		t.Fatalf("counts: %+v", result.Counts)
	}
	if account, _ := f.repo.HubAccount("bob"); account.Ciphertext != "vault:v1:reconnected" {
		t.Fatal("concurrent change overwritten")
	}
}

func TestApplyWithOpenBaoDownWritesNothing(t *testing.T) {
	f := newFixture(t)
	stub := keyringtest.New(t)
	stub.Set(func(s *keyringtest.Bao) { s.FailStatus["encrypt/kionga-credential"] = 503 })
	before := readFile(t, f.path)
	result, err := Run(context.Background(), f.repo, openBaoRing(t, stub, true), options(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts[StatusFailed] != 3 {
		t.Fatalf("counts: %+v", result.Counts)
	}
	if readFile(t, f.path) != before {
		t.Fatal("store changed although sealing failed")
	}
}

func TestRewrapBackToStatic(t *testing.T) {
	f := newFixture(t)
	stub := keyringtest.New(t)
	ctx := context.Background()
	if _, err := Run(ctx, f.repo, openBaoRing(t, stub, true), options(t, true)); err != nil {
		t.Fatal(err)
	}
	// Rollback: static provider that can still read transit.
	client, _ := keyring.NewOpenBao(keyring.OpenBaoConfig{Addr: stub.URL(), TransitKey: "kionga-credential", AuthRole: "kionga-gateway", JWTFile: keyringtest.WriteJWT(t, stub.JWT), AllowHTTP: true})
	static, _ := keyring.NewStatic(legacyKey)
	back, _ := keyring.NewRing(keyring.ProviderStatic, static, client)
	result, err := Run(ctx, f.repo, back, options(t, true))
	if err != nil || result.Counts[StatusConverted] != 3 {
		t.Fatalf("rollback: %+v %v", result.Counts, err)
	}
	if account, _ := f.repo.HubAccount("alice"); keyring.Format([]byte(account.Ciphertext)) != "static" {
		t.Fatal("not static after rollback")
	}
}

func TestProgressLine(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	line := progressLine("apply", 25, 100, start, start.Add(10*time.Second), map[string]int{StatusConverted: 24, StatusFailed: 1})
	want := "2026-01-01T00:00:10Z credential rewrap (apply): 25.0% done, ETA 30s | 25/100 rows, 2.5 rows/s, converted 24, errors 1"
	if line != want {
		t.Fatalf("got  %s\nwant %s", line, want)
	}
	if line := progressLine("dry run", 0, 0, start, start, nil); !strings.Contains(line, "100.0% done, ETA 0s | 0/0 rows") {
		t.Fatal(line)
	}
}

func TestApplyDoesNotWriteWhenTheRestoreLogCannotBeRecorded(t *testing.T) {
	f := newFixture(t)
	stub := keyringtest.New(t)
	ring := openBaoRing(t, stub, true)
	records, _ := f.repo.SealedCredentials()
	closed, err := os.Create(filepath.Join(t.TempDir(), "applied.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	before := readFile(t, f.path)
	row := convert(context.Background(), f.repo, ring, Options{Apply: true, AAD: aad}, records[0], closed)
	if row.Status != StatusFailed {
		t.Fatalf("status %s, want failed", row.Status)
	}
	if readFile(t, f.path) != before {
		t.Fatal("record rewritten without a restore line")
	}
}

func TestRestoreSkipsRowsTheRunDidNotWrite(t *testing.T) {
	f := newFixture(t)
	stub := keyringtest.New(t)
	opts := options(t, true)
	if _, err := Run(context.Background(), racingStore{f.repo}, openBaoRing(t, stub, true), opts); err != nil {
		t.Fatal(err)
	}
	restored, skipped, err := Restore(f.repo, filepath.Join(opts.JobDir, appliedFile), nil)
	if err != nil || restored != 2 || skipped != 1 {
		t.Fatalf("restore: %d restored, %d skipped, %v", restored, skipped, err)
	}
	if account, _ := f.repo.HubAccount("bob"); account.Ciphertext != "vault:v1:reconnected" {
		t.Fatal("restore overwrote the user's newer value")
	}
}
