// Package credentialrewrap re-encrypts stored account credentials (Hugging
// Face and GitHub tokens) with the gateway's configured key provider.
//
// It is a data change, so it follows the backfill protocol: a dry run by
// default; with Apply, every row it will change is snapshotted to the job
// directory before the first write, progress is logged from real counts, and
// the run ends with a before/after report and CSV. Reports carry ciphertext
// formats ("static", "openbao:v1") and counts only, never plaintext and never
// ciphertext. The snapshot and the applied log do hold ciphertext, because
// they are what Restore puts back; they are written with mode 0600.
package credentialrewrap

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/keyring"
	"github.com/kiongahq/platform/internal/store"
)

// Row statuses.
const (
	StatusCurrent     = "current"            // already in the configured format
	StatusWould       = "would-convert"      // dry run: opens, would be rewritten
	StatusConverted   = "converted"          // rewritten and verified
	StatusUnresolved  = "unresolved-subject" // no audited subject hashes to the id
	StatusUnreadable  = "unreadable"         // cannot be opened with the available keys
	StatusChanged     = "changed"            // the user changed it during the run
	StatusFailed      = "failed"             // sealing, verifying or writing failed
	snapshotFile      = "snapshot.jsonl"
	appliedFile       = "applied.jsonl"
	progressFile      = "progress.log"
	reportFile        = "report.md"
	csvFile           = "before_after.csv"
	defaultMaxRows    = 100_000
	defaultMaxBytes   = 100 << 20
	defaultProgressAt = 5 * time.Second
)

// Options control one run.
type Options struct {
	Apply   bool
	JobDir  string                                    // created; must not exist
	AAD     func(kind, subject string) ([]byte, bool) // associated data per record
	Console io.Writer                                 // progress echo; nil for none
	Now     func() time.Time                          // clock; nil for time.Now
	// Limits for the snapshot; zero means the protocol defaults (100,000
	// rows, 100 MB). Larger runs need the operator's explicit approval.
	MaxRows       int
	MaxBytes      int64
	ProgressEvery time.Duration
}

// Row is one record's outcome.
type Row struct {
	Kind, ID, Before, After, Status string
}

// Result summarises a run.
type Result struct {
	Provider string
	Apply    bool
	JobDir   string
	Total    int // sealed records found
	Targets  int // records not in the configured format
	Counts   map[string]int
	Rows     []Row
	Elapsed  time.Duration
}

// Incomplete reports whether some target was not converted (or, in a dry
// run, would not be).
func (r Result) Incomplete() bool {
	return r.Counts[StatusUnresolved]+r.Counts[StatusUnreadable]+r.Counts[StatusChanged]+r.Counts[StatusFailed] > 0
}

type snapshotEntry struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Ciphertext string `json:"ciphertext"`
}

type appliedEntry struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// ErrTooLarge means the snapshot would exceed the protocol limits.
var ErrTooLarge = errors.New("snapshot would exceed the backfill limits")

func (o *Options) defaults() {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxRows == 0 {
		o.MaxRows = defaultMaxRows
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = defaultMaxBytes
	}
	if o.ProgressEvery == 0 {
		o.ProgressEvery = defaultProgressAt
	}
	if o.Console == nil {
		o.Console = io.Discard
	}
}

// DefaultJobDir names a fresh job directory under /tmp.
func DefaultJobDir(now time.Time) string {
	return filepath.Join(os.TempDir(), "kionga-credential-rewrap-"+now.UTC().Format("20060102T150405Z"))
}

// Run converts (Apply) or plans (dry run) every sealed credential that is not
// in the ring's configured format.
func Run(ctx context.Context, repo store.SealedCredentialStore, ring *keyring.Ring, opts Options) (Result, error) {
	opts.defaults()
	if opts.AAD == nil || opts.JobDir == "" {
		return Result{}, errors.New("credential rewrap needs associated data rules and a job directory")
	}
	if !ring.Configured() {
		return Result{}, fmt.Errorf("the %s key provider cannot seal: %w", ring.Provider(), keyring.ErrNotConfigured)
	}
	start := opts.Now()
	records, err := repo.SealedCredentials()
	if err != nil {
		return Result{}, fmt.Errorf("list sealed credentials: %w", err)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Kind+"/"+records[i].ID < records[j].Kind+"/"+records[j].ID
	})
	result := Result{Provider: ring.Provider(), Apply: opts.Apply, JobDir: opts.JobDir, Total: len(records), Counts: map[string]int{}}
	var targets []store.SealedCredential
	var snapshotBytes int64
	for _, record := range records {
		if ring.Current([]byte(record.Ciphertext)) {
			result.Counts[StatusCurrent]++
			result.Rows = append(result.Rows, Row{Kind: record.Kind, ID: record.ID, Before: keyring.Format([]byte(record.Ciphertext)), After: keyring.Format([]byte(record.Ciphertext)), Status: StatusCurrent})
			continue
		}
		targets = append(targets, record)
		snapshotBytes += int64(len(record.Kind) + len(record.ID) + len(record.Ciphertext) + 48)
	}
	result.Targets = len(targets)
	if opts.Apply && (len(targets) > opts.MaxRows || snapshotBytes > opts.MaxBytes) {
		return result, fmt.Errorf("%w: %d rows, about %d bytes (limits %d rows, %d bytes); ask for approval and rerun with raised limits", ErrTooLarge, len(targets), snapshotBytes, opts.MaxRows, opts.MaxBytes)
	}
	if err := os.Mkdir(opts.JobDir, 0o700); err != nil { // #nosec G703 -- operator-chosen job directory
		return result, fmt.Errorf("create job directory: %w", err)
	}
	progress, err := os.OpenFile(filepath.Join(opts.JobDir, progressFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 G703 -- operator-chosen job directory
	if err != nil {
		return result, err
	}
	defer progress.Close()
	mode := "dry run"
	if opts.Apply {
		mode = "apply"
	}
	fmt.Fprintf(opts.Console, "credential rewrap (%s): follow progress with: tail -f %s\n", mode, filepath.Join(opts.JobDir, progressFile))
	report := func(done int) {
		line := progressLine(mode, done, len(targets), start, opts.Now(), result.Counts)
		fmt.Fprintln(progress, line)
		fmt.Fprintln(opts.Console, line)
	}
	if opts.Apply && len(targets) > 0 {
		if err := writeSnapshot(filepath.Join(opts.JobDir, snapshotFile), targets); err != nil {
			return result, fmt.Errorf("snapshot: %w", err)
		}
		fmt.Fprintf(opts.Console, "credential rewrap: snapshot of %d rows written to %s\n", len(targets), filepath.Join(opts.JobDir, snapshotFile))
	}
	var applied *os.File
	if opts.Apply && len(targets) > 0 {
		applied, err = os.OpenFile(filepath.Join(opts.JobDir, appliedFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 G703 -- operator-chosen job directory
		if err != nil {
			return result, err
		}
		defer applied.Close()
	}
	report(0)
	lastReport := opts.Now()
	for i, record := range targets {
		if err := ctx.Err(); err != nil {
			report(i)
			return result, err
		}
		row := convert(ctx, repo, ring, opts, record, applied)
		result.Counts[row.Status]++
		result.Rows = append(result.Rows, row)
		if now := opts.Now(); now.Sub(lastReport) >= opts.ProgressEvery || i == len(targets)-1 {
			report(i + 1)
			lastReport = now
		}
	}
	result.Elapsed = opts.Now().Sub(start)
	sort.SliceStable(result.Rows, func(i, j int) bool {
		return result.Rows[i].Kind+"/"+result.Rows[i].ID < result.Rows[j].Kind+"/"+result.Rows[j].ID
	})
	if err := writeCSV(filepath.Join(opts.JobDir, csvFile), result.Rows); err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(opts.JobDir, reportFile), []byte(renderReport(result)), 0o600); err != nil { // #nosec G703 -- operator-chosen job directory
		return result, err
	}
	fmt.Fprintf(opts.Console, "credential rewrap: report %s, CSV %s\n", filepath.Join(opts.JobDir, reportFile), filepath.Join(opts.JobDir, csvFile))
	return result, nil
}

func convert(ctx context.Context, repo store.SealedCredentialStore, ring *keyring.Ring, opts Options, record store.SealedCredential, applied *os.File) Row {
	row := Row{Kind: record.Kind, ID: record.ID, Before: keyring.Format([]byte(record.Ciphertext)), After: keyring.Format([]byte(record.Ciphertext))}
	if record.Subject == "" {
		row.Status = StatusUnresolved
		return row
	}
	aad, ok := opts.AAD(record.Kind, record.Subject)
	if !ok {
		row.Status = StatusFailed
		return row
	}
	plain, err := ring.Open(ctx, []byte(record.Ciphertext), aad)
	defer clear(plain)
	if err != nil {
		if errors.Is(err, keyring.ErrOpen) || errors.Is(err, keyring.ErrStaticUnavailable) || errors.Is(err, keyring.ErrTransitUnavailable) || errors.Is(err, keyring.ErrNotConfigured) {
			row.Status = StatusUnreadable
		} else {
			row.Status = StatusFailed
		}
		return row
	}
	if !opts.Apply {
		row.Status = StatusWould
		row.After = targetFormat(ring)
		return row
	}
	sealed, err := ring.Seal(ctx, plain, aad)
	if err != nil {
		row.Status = StatusFailed
		return row
	}
	check, err := ring.Open(ctx, sealed, aad)
	same := err == nil && string(check) == string(plain)
	clear(check)
	if !same || !ring.Current(sealed) {
		row.Status = StatusFailed
		return row
	}
	// Record the restore line durably before the write: if it cannot be
	// recorded, the row is not changed. A line whose write then fails or loses
	// the race is harmless, because Restore only swaps rows that still hold
	// the value this run wrote.
	line, _ := json.Marshal(appliedEntry{Kind: record.Kind, ID: record.ID, Before: record.Ciphertext, After: string(sealed)})
	if _, err := applied.Write(append(line, '\n')); err != nil {
		row.Status = StatusFailed
		return row
	}
	if err := applied.Sync(); err != nil {
		row.Status = StatusFailed
		return row
	}
	switch err := repo.ReplaceSealedCredential(record.Kind, record.ID, record.Ciphertext, string(sealed)); {
	case errors.Is(err, store.ErrCredentialChanged):
		row.Status = StatusChanged
		return row
	case err != nil:
		row.Status = StatusFailed
		return row
	}
	row.Status = StatusConverted
	row.After = keyring.Format(sealed)
	return row
}

func targetFormat(ring *keyring.Ring) string {
	if ring.Provider() == keyring.ProviderOpenBao {
		return "openbao"
	}
	return keyring.ProviderStatic
}

func writeSnapshot(path string, targets []store.SealedCredential) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 G703 -- path inside the operator-chosen job directory
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(file)
	for _, record := range targets {
		line, _ := json.Marshal(snapshotEntry{Kind: record.Kind, ID: record.ID, Ciphertext: record.Ciphertext})
		if _, err := writer.Write(append(line, '\n')); err != nil {
			file.Close()
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// progressLine is the protocol's update: title, percent, ETA, then counts.
func progressLine(mode string, done, total int, start, now time.Time, counts map[string]int) string {
	elapsed := now.Sub(start)
	percent := 100.0
	if total > 0 {
		percent = float64(done) * 100 / float64(total)
	}
	rate := 0.0
	if elapsed > 0 {
		rate = float64(done) / elapsed.Seconds()
	}
	eta := "unknown"
	switch {
	case done >= total:
		eta = "0s"
	case rate > 0:
		eta = time.Duration(float64(total-done) / rate * float64(time.Second)).Round(time.Second).String()
	}
	errorsSeen := counts[StatusUnresolved] + counts[StatusUnreadable] + counts[StatusChanged] + counts[StatusFailed]
	converted := counts[StatusConverted] + counts[StatusWould]
	return fmt.Sprintf("%s credential rewrap (%s): %.1f%% done, ETA %s | %d/%d rows, %.1f rows/s, converted %d, errors %d",
		now.UTC().Format(time.RFC3339), mode, percent, eta, done, total, rate, converted, errorsSeen)
}

func writeCSV(path string, rows []Row) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 G703 -- path inside the operator-chosen job directory
	if err != nil {
		return err
	}
	writer := csv.NewWriter(file)
	_ = writer.Write([]string{"kind", "record_id", "before_format", "after_format", "status"})
	for _, row := range rows {
		_ = writer.Write([]string{row.Kind, row.ID, row.Before, row.After, row.Status})
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func renderReport(r Result) string {
	var b strings.Builder
	mode := "dry run (nothing written)"
	if r.Apply {
		mode = "apply"
	}
	fmt.Fprintf(&b, "# Credential rewrap report\n\n- Mode: %s\n- Target provider: %s\n- Sealed records found: %d\n- Records not in the target format: %d\n- Elapsed: %s\n\n", mode, r.Provider, r.Total, r.Targets, r.Elapsed.Round(time.Millisecond))
	b.WriteString("## Verdict\n\n")
	switch {
	case r.Targets == 0:
		fmt.Fprintf(&b, "Nothing to do: all %d records are already sealed by %s.\n\n", r.Total, r.Provider)
	case !r.Incomplete() && r.Apply:
		fmt.Fprintf(&b, "Worked: all %d records outside the target format were converted to %s and verified by opening them again.\n\n", r.Targets, r.Provider)
	case !r.Incomplete():
		fmt.Fprintf(&b, "Ready: all %d records outside the target format open with the available keys and would be converted. Rerun with --apply.\n\n", r.Targets)
	default:
		fmt.Fprintf(&b, "Incomplete: %d of %d records outside the target format were not %s.\n\n", r.Counts[StatusUnresolved]+r.Counts[StatusUnreadable]+r.Counts[StatusChanged]+r.Counts[StatusFailed], r.Targets, map[bool]string{true: "converted", false: "convertible"}[r.Apply])
	}
	b.WriteString("## What needs to be better\n\n")
	gaps := 0
	if n := r.Counts[StatusUnresolved]; n > 0 {
		gaps++
		fmt.Fprintf(&b, "- %d records have no audited owner, so their associated data is unknown. They keep opening only while KIONGA_CREDENTIAL_KEY stays in the gateway; otherwise those users reconnect their accounts.\n", n)
	}
	if n := r.Counts[StatusUnreadable]; n > 0 {
		gaps++
		fmt.Fprintf(&b, "- %d records do not open with the keys this run has. Give the run KIONGA_CREDENTIAL_KEY (static records) or OpenBao access (transit records), or ask those users to reconnect.\n", n)
	}
	if n := r.Counts[StatusChanged]; n > 0 {
		gaps++
		fmt.Fprintf(&b, "- %d records changed during the run (a user reconnected). Rerun to pick them up; the new values are already in the configured format if the gateway wrote them.\n", n)
	}
	if n := r.Counts[StatusFailed]; n > 0 {
		gaps++
		fmt.Fprintf(&b, "- %d records failed to seal, verify or write. Check the gateway log and OpenBao availability, then rerun; converted rows are skipped.\n", n)
	}
	if gaps == 0 {
		b.WriteString("- Nothing.\n")
	}
	b.WriteString("\n## Counts\n\n| Kind | Before | After | Status | Records |\n| --- | --- | --- | --- | --- |\n")
	type key struct{ kind, before, after, status string }
	counts := map[key]int{}
	example := map[key]string{}
	for _, row := range r.Rows {
		k := key{row.Kind, row.Before, row.After, row.Status}
		counts[k]++
		if _, ok := example[k]; !ok {
			example[k] = row.ID
		}
	}
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	for _, k := range keys {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d |\n", k.kind, k.before, k.after, k.status, counts[k])
	}
	b.WriteString("\n## Examples (one record per category)\n\n| Kind | Record id | Before | After | Status |\n| --- | --- | --- | --- | --- |\n")
	for _, k := range keys {
		id := example[k]
		if len(id) > 12 {
			id = id[:12] + "..."
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", k.kind, id, k.before, k.after, k.status)
	}
	fmt.Fprintf(&b, "\nFull per-record CSV: %s\n", filepath.Join(r.JobDir, csvFile))
	if r.Apply && r.Targets > 0 {
		fmt.Fprintf(&b, "Snapshot of every changed row (ciphertext, mode 0600): %s\nRestore converted rows: gateway rewrap --restore %s\n", filepath.Join(r.JobDir, snapshotFile), filepath.Join(r.JobDir, appliedFile))
	}
	return b.String()
}

// Restore puts back the ciphertext an applied run replaced, for every row
// that still holds the value the run wrote. Rows changed since are left alone
// and counted.
func Restore(repo store.SealedCredentialStore, appliedPath string, console io.Writer) (restored, skipped int, err error) {
	if console == nil {
		console = io.Discard
	}
	file, err := os.Open(appliedPath) // #nosec G304 G703 -- operator-supplied path
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		var entry appliedEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return restored, skipped, fmt.Errorf("applied log: %w", err)
		}
		switch err := repo.ReplaceSealedCredential(entry.Kind, entry.ID, entry.After, entry.Before); {
		case errors.Is(err, store.ErrCredentialChanged):
			skipped++
		case err != nil:
			return restored, skipped, err
		default:
			restored++
		}
	}
	if err := scanner.Err(); err != nil {
		return restored, skipped, err
	}
	fmt.Fprintf(console, "credential rewrap restore: %d rows restored, %d skipped because they changed since\n", restored, skipped)
	return restored, skipped, nil
}
