# Contributing to platform

Thank you for helping improve Kionga. Read [ARCHITECTURE.md](ARCHITECTURE.md) for
how the code is organized before you make a larger change.

## Development setup

Prerequisites:

- Go 1.26. `go/go.mod` declares `go 1.26.0` with `toolchain go1.26.10`; a newer
  local Go downloads that toolchain automatically.
- Node.js 22 and npm, for the console tests (the version CI uses).
- Docker, for the PostgreSQL integration tests and the images.
- ripgrep (`rg`), used by `make verify`.
- Google Chrome, only for the Playwright browser tests.

```bash
git clone --recurse-submodules git@github.com:kiongahq/platform.git
cd platform
make verify
```

The `contracts/` submodule holds the shared pipeline schema; some Go tests read it,
so clone with `--recurse-submodules` (or run `git submodule update --init`).

## Run the checks

The **Platform CI** workflow (`.github/workflows/cicd.yml`) has three jobs. Run the
same checks locally:

```bash
make verify              # gofmt -l, go vet, go test -race, build every command,
                         # node --check on the console scripts, banned-technology scan
make test-ui             # npm ci --ignore-scripts && npm run test:ui (jsdom)
make test-integration    # go test -tags=integration ./integration against a throwaway
                         # pgvector/pgvector:pg16 container
make test-security       # govulncheck v1.8.0, gosec v2.29.0, gitleaks v8.30.1
```

`make test-security` runs exactly the CI commands:

```bash
cd go && go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
cd go && go run github.com/securego/gosec/v2/cmd/gosec@v2.29.0 -quiet -exclude-dir=build -severity medium -confidence medium ./...
go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --no-banner --redact .
```

Browser acceptance tests run against a live stack and are not part of CI:

```bash
make -C ../deploy local-up
make test-browser        # Playwright with the installed Chrome; KIONGA_URL overrides the target
```

Use `make format` to apply gofmt and `make run` to start the gateway alone on
`:8080` with a file-backed store.

## Build binaries and images

```bash
make build                                             # bin/mlaiops-<service>
docker build -f go/Dockerfile --build-arg SERVICE=gateway -t gateway:dev go
```

`SERVICE` is one of `gateway`, `operator`, `integration-worker`, `feature-gateway`,
`storage-proxy`, `metrics-collector`, `log-exporter` or `cli`. The **Build &
Publish Images** workflow (`build.yml`) publishes each as
`ghcr.io/kiongahq/<service>` on pushes to `main` that touch `go/`, and on `v*`
tags, tagged `sha-<short>`, `latest` and the semantic version.

## Vendored browser libraries

The console vendors Dagre, Editor.js and highlight.js under
`go/cmd/gateway/web/vendor/` with an integrity manifest. After you update one, run
`python3 scripts/vendor-manifest.py` to verify it against upstream and rewrite the
manifest; `tests/ui/vendor-integrity.test.cjs` checks it on every run.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/), as the history
does: `type(scope): summary` in the imperative mood, for example
`feat(operator): image pull secrets for private registries` or
`fix(auth): list no sign-in providers as [] rather than null`. Common types are
`feat`, `fix`, `perf`, `docs`, `test`, `ci` and `chore`.

## Pull requests

- Keep one logical change per pull request, with tests for new behavior.
- CI must pass. Do not skip hooks or checks.
- A change to a contract other components read (the REST API in `go/pkg/api`, the
  CRDs in `go/pkg/kube/v1alpha1`, the pipeline schema in `contracts/`) needs the
  matching changes in those components; link their pull requests.

## Reviewed scanner findings

When gosec flags code that you have reviewed and found safe, annotate the line with
the rule and the reason after `--`:

```go
response, err := client.Do(request) // #nosec G704 -- the CLI calls the gateway its user configured
```

Fake credentials in tests that gitleaks reports are pinned in `.gitleaksignore` by
fingerprint (`commit:file:rule:line`, printed by gitleaks), with a comment that says
why. A fingerprint pins one exact finding, so any new secret still fails the scan.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENCE).
