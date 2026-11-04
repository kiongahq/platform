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

The core backend browser checks run in CI and can also run locally:

```bash
npm run build:frontend
npm run test:core
```

These checks start an isolated Go gateway with temporary state and a Langflow
test upstream. They verify console navigation, instance selection, session-based
launch, proxied workspace assets, access restrictions and core API responses.
They do not connect to the development database, cluster or identity provider.
Use the live-stack acceptance tests to validate a running Langflow installation.

Use `make format` to apply gofmt and `make run` to start the gateway alone on
`:8080` with a file-backed store.

## Build binaries and images

```bash
make build                                             # bin/mlaiops-<service>
docker build -f Dockerfile.gateway -t gateway:dev .
docker build --build-arg SERVICE=operator -t operator:dev go
```

The gateway image builds the frontend with `Dockerfile.gateway`. Other Go service
images use `go/Dockerfile` and do not require Node.js or frontend dependencies.

`SERVICE` is one of `gateway`, `operator`, `integration-worker`, `feature-gateway`,
`storage-proxy`, `metrics-collector`, `log-exporter` or `cli`. The **Build &
Publish Images** workflow (`build.yml`) publishes each as
`ghcr.io/kiongahq/<service>` on pushes to `main` that touch `go/` or `frontend/`,
using `edge` and SHA references. Published releases use exact version tags and
attach image digest records to the GitHub release.

## Vendored browser libraries

The console vendors highlight.js under
`frontend/public/vendor/` with an integrity manifest. After you update one, run
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

## Release automation

Configure `RELEASE_PLEASE_TOKEN` with Contents, Issues and Pull requests write
permissions scoped to the participating repositories. Release PRs require
maintainer review and all required checks; no bypass is granted to automation.
The sole maintainer can merge after reviewing the diff and passing checks,
without a separate approval. Restore independent code-owner approval when
another trusted maintainer joins.
The workflow reports an error until this credential is configured.

Use reviewed `vX.Y.Z` releases, keep published tags and packages immutable, and
pin container digests in deployments. Development builds use `edge` and SHA
references; existing `latest` aliases stop updating. The
[release handbook](https://kiongahq.github.io/docs/engineering/contributing/releases/)
covers publication, dependency updates and rollback.

### Agent Teams console acceptance

After installing the root and frontend npm dependencies, run
`npx playwright test --config playwright.teams.config.cjs`. The config starts and
stops its own mock gateway and Next development server. Set
`PLAYWRIGHT_CHANNEL=chromium` after `npx playwright install chromium` to use the
bundled browser. CI runs both desktop and narrow layouts, including approvals,
outages and untrusted markup. This lane verifies console behavior against canned
responses; the Agent Teams smoke workflow verifies the real services separately.
