# Sandbox manager

Runs model-chosen commands in isolated containers. `cmd/sandbox-manager` is the only
component with container-engine access for sandboxes. It is stateless: labelled
containers and volumes are the record. The wire contract is
`contracts/agent-team/sandbox-api.v1.schema.json`, and the gateway uses `client`.

Treat every command, file, repository and byte of output as hostile.

## Layout

| File | Holds |
| --- | --- |
| `service.go` | Every isolation decision: runtime gate, attestation, exec limits, reaper |
| `git.go`, `scripts.go` | Credentialed git in helpers; the fixed shell programs |
| `validate.go` | Path, branch and repository rules |
| `http.go` | Routes, bearer check, strict decoding |
| `docker.go`, `kubernetes.go` | Engines. They hard-code the hardened settings |
| `gate_test.go` and friends | Fast tests with a fake driver or fake engine |
| `attack_test.go` | Hostile commands against a real engine (`-tags sandboxattack`) |

## Configuration

| Variable | Default | Ceiling or values |
| --- | --- | --- |
| `KIONGA_SANDBOX_TOKEN` (or `_FILE`) | required | at least 16 characters, 32 in production |
| `PORT` | `8088` | |
| `KIONGA_SANDBOX_DRIVER` | `docker` | `docker`, `kubernetes` |
| `KIONGA_SANDBOX_RUNTIME` | `runsc` | RuntimeClass name under `kubernetes` |
| `KIONGA_SANDBOX_EXPECT_KERNEL` | `gvisor` | text the runtime's `uname -r` must contain, such as `4.19.0-gvisor` |
| `KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME` | unset | `1`, development only; refused with `KIONGA_ENVIRONMENT=production` |
| `KIONGA_SANDBOX_IMAGES` | empty: creates return 503 | comma-separated, `repo@sha256:digest`. The Docker driver also takes a local `sha256:` image ID |
| `KIONGA_SANDBOX_HELPER_IMAGE` | first of the images | pinned by digest |
| `KIONGA_SANDBOX_GIT_HOSTS` | empty: all git refused | comma-separated `host` or `host:port` |
| `KIONGA_SANDBOX_GIT_NETWORK` | `<prefix>git` | helpers' own engine network, which the manager creates. Not `bridge`, `host` or any network it did not create |
| `KIONGA_SANDBOX_GIT_ALLOW_PRIVATE_FOR_TESTS` | unset | `1` lets helpers reach private addresses, for local test servers; refused with `KIONGA_ENVIRONMENT=production` |
| `KIONGA_SANDBOX_GIT_USERNAME` | `x-access-token` | |
| `KIONGA_SANDBOX_GIT_CA_FILE` | system roots | PEM bundle for a private git host |
| `KIONGA_SANDBOX_GIT_BUNDLE_BYTES` | 1 GiB | 8 GiB |
| `KIONGA_SANDBOX_GIT_TIMEOUT_SECONDS` | 900 | 3600 |
| `KIONGA_SANDBOX_PIDS` | 512 | 4096 |
| `KIONGA_SANDBOX_MEMORY_BYTES` | 2 GiB | 16 GiB |
| `KIONGA_SANDBOX_CPUS` | 2 | 16 |
| `KIONGA_SANDBOX_TMP_BYTES` | 256 MiB | 4 GiB |
| `KIONGA_SANDBOX_VOLUME_BYTES` | 10 GiB, `kubernetes` only | 500 GiB |
| `KIONGA_SANDBOX_TTL_SECONDS` | 86400 | 7 days |
| `KIONGA_SANDBOX_MAX_SANDBOXES` | 256 | 4096 |
| `KIONGA_SANDBOX_NAME_PREFIX` | `kionga-sandbox-` | scopes everything this manager sees |
| `KIONGA_SANDBOX_DOCKER_SOCKET` | `/var/run/docker.sock` | `docker` only |
| `KIONGA_SANDBOX_NAMESPACE`, `KIONGA_SANDBOX_STORAGE_CLASS` | | `kubernetes` only |

A value above its ceiling stops the manager from starting.

Under `kubernetes`, the chart must ship a NetworkPolicy named `<prefix>default-deny` and
allow egress only to the `kionga.dev/sandbox-role=git-helper` label. The manager needs:
`pods` get, list, create, delete; `pods/exec` create and get; `persistentvolumeclaims`
get, list, create, delete; `networkpolicies` get; and `runtimeclasses` get. It needs no
Secrets. Set the kubelet's `podPidsLimit`, because a Pod has no pid limit of its own.

Register gVisor with `runsc install -- --oci-seccomp`. Without that flag gVisor ignores
the engine's seccomp profile.

## Why it is correct

**Runtime.** The engine's list of runtimes is necessary and not sufficient. Podman lists
`runsc` with no binary behind it, and Docker will register any path under that name. So a
throwaway container proves the runtime before creates are allowed, and every sandbox and
helper proves it again, by reporting a gVisor kernel release before any other code runs
in it. Failure removes the container and its volume and returns 503.

**Exec.** The command is one argv element to `bash -c`, under `env -i`. The deadline is on
the manager's clock. When it passes, the manager closes the connection rather than
waiting for end of stream, because a process that left the exec's tree can hold stdout
open forever. It then runs `kill -KILL -1` in the sandbox, which also reaches processes
that left the group with `setsid`, and restarts the sandbox if that cannot run or anything
survives. This also ends other background processes of that sandbox, by design.

**Paths.** A lexical check refuses `..`, control characters and anything outside
`/workspace`. Then a fixed script resolves the path inside the sandbox with `realpath -m`,
as the sandbox user, and checks the result. A race with the agent can only show it what it
can already read, because the read also happens as that user, in there.

**Git.** Helper and sandbox never share a filesystem, so a helper cannot run git inside a
`.git` the agent touched. Only bundle bytes cross, streamed through the manager with no
disk. Clone: fetch bare into an empty directory, delete the secret, send a bundle, unpack
it in the sandbox, where no credential exists. Nothing is checked out in the helper. Push:
bundle in the sandbox, where hostile repository config can run but has nothing to steal;
in the helper, clone fresh, read the bundle as data, push a private ref with no `+`. The
secret arrives over exec stdin onto a memory-backed mount and is read by an askpass
program, so it is in no argv, environment or engine inspect output.

## Failure modes

- The Docker volume has no size limit. A hostile agent can fill the engine's disk until
  the TTL. Keep the engine's data root on its own filesystem. Kubernetes claims are bounded.
- The manager resolves an allowed host, refuses it if any answer is private, loopback,
  link-local, metadata or reserved, and pins the answers in the helper's hosts file, so
  DNS rebinding after the check cannot reach git, and redirects are refused. This is a
  request check. It is not a firewall: the engine network still routes to private
  addresses, so code execution inside a helper, through a git or image bug, could reach
  them. Closing that needs a host firewall on the helper network (platform#115).
- The manager needs a DNS path. On a network with no route out, git fails closed with
  "could not be resolved".
- A private git host cannot be used: only the test allowance permits one.
- An address list that changes after the check only causes a connection failure.
- Redirects are refused, so a renamed repository fails until its URL is updated.
- Push bundles whole history, so a repository over the bundle limit cannot be pushed.
- A manager crash can leave a helper holding a credential until the reaper removes it,
  at the git timeout plus five minutes, after restart.
- A sandbox restart keeps `/workspace` and loses `/tmp` and running processes.
- `KIONGA_SANDBOX_MAX_SANDBOXES` is checked then created, so concurrent creates can pass
  it by a few.
