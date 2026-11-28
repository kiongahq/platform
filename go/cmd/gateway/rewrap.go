package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kiongahq/platform/internal/credentialrewrap"
	"github.com/kiongahq/platform/internal/httpapi"
	"github.com/kiongahq/platform/internal/keyring"
	"github.com/kiongahq/platform/internal/store"
)

const rewrapUsage = `usage: gateway rewrap [--apply] [--job-dir DIR]
       gateway rewrap --restore DIR/applied.jsonl

Re-encrypts stored Hugging Face and GitHub tokens with the configured key
provider (KIONGA_KEY_PROVIDER). Without --apply it only reports what would
change. With --apply it first snapshots every row it will change to the job
directory, logs progress to DIR/progress.log and ends with DIR/report.md and
DIR/before_after.csv. Reports hold ciphertext formats and counts, never
tokens.

Moving from static to openbao: run it with the openbao settings and
KIONGA_CREDENTIAL_KEY, so it can open the old records; remove the key from
the gateway once the report shows nothing left to convert.

With the file repository (no DATABASE_URL), stop the gateway first: the file
has one writer.
`

func runRewrap(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("rewrap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, rewrapUsage) }
	apply := flags.Bool("apply", false, "write the converted ciphertext (default: dry run)")
	jobDir := flags.String("job-dir", "", "directory for the snapshot, progress log and report (default /tmp/kionga-credential-rewrap-<time>)")
	restore := flags.String("restore", "", "put back the ciphertext an applied run replaced, from its applied.jsonl")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		if err == nil {
			flags.Usage()
		}
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	repo, closeRepo, err := openSealedCredentialStore(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "rewrap: %v\n", err)
		return 1
	}
	defer closeRepo()
	if *restore != "" {
		if _, _, err := credentialrewrap.Restore(repo, *restore, stdout); err != nil {
			fmt.Fprintf(stderr, "rewrap restore: %v\n", err)
			return 1
		}
		return 0
	}
	ring, err := keyring.FromEnv(ctx, os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "rewrap: credential keyring: %v\n", err)
		return 1
	}
	dir := *jobDir
	if dir == "" {
		dir = credentialrewrap.DefaultJobDir(time.Now())
	}
	result, err := credentialrewrap.Run(ctx, repo, ring, credentialrewrap.Options{Apply: *apply, JobDir: dir, AAD: httpapi.SealedCredentialAAD, Console: stdout})
	if err != nil {
		fmt.Fprintf(stderr, "rewrap: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "credential rewrap: %d records, %d to convert, counts %v\n", result.Total, result.Targets, result.Counts)
	if result.Incomplete() {
		return 3
	}
	return 0
}

func openSealedCredentialStore(ctx context.Context) (store.SealedCredentialStore, func(), error) {
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		postgres, err := store.OpenPostgres(ctx, databaseURL, os.Getenv("KIONGA_TENANT"))
		if err != nil {
			return nil, nil, fmt.Errorf("open PostgreSQL repository: %w", err)
		}
		return postgres, postgres.Close, nil
	}
	dataPath := os.Getenv("KIONGA_DATA_PATH")
	if dataPath == "" {
		dataPath = "data/platform.json"
	}
	fileStore, err := store.Open(dataPath)
	if err != nil {
		return nil, nil, fmt.Errorf("file repository: %w", err)
	}
	return fileStore, func() {}, nil
}

// logKeyring records which provider seals credentials, never key material.
func logKeyring(ring *keyring.Ring) {
	switch {
	case ring.Provider() == keyring.ProviderOpenBao && ring.HasStatic():
		log.Printf("credential keyring: OpenBao transit seals new credentials; KIONGA_CREDENTIAL_KEY is present for records not yet rewrapped. Run 'gateway rewrap --apply', then remove it")
	case ring.Provider() == keyring.ProviderOpenBao:
		log.Printf("credential keyring: OpenBao transit; no credential key in the gateway")
	case ring.Configured():
		log.Printf("credential keyring: static KIONGA_CREDENTIAL_KEY")
	default:
		log.Printf("credential keyring: not configured; account connections are disabled until KIONGA_CREDENTIAL_KEY or KIONGA_KEY_PROVIDER=openbao is set")
	}
}
