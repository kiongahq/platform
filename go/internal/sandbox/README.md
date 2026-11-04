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
| `KIONGA_SANDBOX_DISK_MB` | 10240 | 512000. Per workspace. Also the per-file limit. Under `kubernetes`, the claim size |
| `KIONGA_SANDBOX_DISK_FILES` | 2000000 | 50000000. Per workspace, `docker` only |
| `KIONGA_SANDBOX_DISK_CHECK_SECONDS` | 5 | 300. Sweep interval, `docker` only |
| `KIONGA_SANDBOX_MIN_FREE_DISK_MB` | 5120 | Refuse new sandboxes under this, `docker` only |
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

## Disk

Under the Docker driver a workspace limit is a usage check, because a fixed-size volume is
not available on common hosts. Measured under real gVisor in CI, on ext4:

| Option | Docker | Podman |
| --- | --- | --- |
| `size` volume option | refused: "no quota support" | start refused |
| `o=size` volume option | refused: "device" required | refused: no project quota |
| tmpfs volume | hard limit, but it uses memory and is lost on restart | same |
| Loopback file | needs a privileged helper on the host, so not used | same |

`size` needs XFS with project quota, which this code does not use because no CI host can
prove it.

**What runs.** Before every exec, write, clone and push, and every
`KIONGA_SANDBOX_DISK_CHECK_SECONDS` for each running sandbox, a throwaway read-only container
measures the workspace with `du`. It runs outside the sandbox, so no agent process can kill
it, stop it or forge its output. Over `KIONGA_SANDBOX_DISK_MB` or `KIONGA_SANDBOX_DISK_FILES`,
or unmeasurable because a directory is unreadable or the walk takes over 60 seconds, the
sandbox is killed and calls return 507 `workspace_quota_exceeded`. An engine failure returns
503 and refuses the call without stopping anything.

**One file is a hard limit.** `RLIMIT_FSIZE` is set to the disk limit, and a write past it fails.

**The window.** Between two checks a writer is limited only by the disk. The most it can pass
the limit by is roughly: write rate x (check interval + meter time + sweep time), plus
buffered data up to the memory limit. Sweep time is about (running sandboxes / 4) x meter time.

| Measured in CI, GitHub-hosted runner | Docker | Podman 4.9.3 |
| --- | --- | --- |
| Sustained `fsync` writes | 209 MiB/s | 210 MiB/s |
| Buffered burst, until the memory limit stopped the sandbox | 2119 MiB/s for 512 MiB | 1959 MiB/s for 512 MiB |
| One meter | 2.2 s | 0.4 s |
| Past a 64 MiB limit with a 2 s sweep, one writer | 264 MiB | 376 MiB |

At the defaults of a 5 second check and 2 GiB of memory, one sandbox on such a disk can pass
its limit by about 1.5 GiB of sustained writes, plus up to about 2 GiB of buffered data. A faster disk
allows more. Several sandboxes share the disk's rate, and a longer sweep widens the window.

**Recovery.** The sandbox stays stopped and its volume is kept. Reads and `git/diff` keep
working through a read-only container, and never restart it. Exec, writes, clone and push
return 507 every time. Nothing inside can free space, so `DELETE` is the way out, and it
loses the workspace. A new create then starts empty.

**The floor.** A create returns 503 when the free space on the filesystem holding engine volumes,
read by a meter and cached 15 seconds, is under `KIONGA_SANDBOX_MIN_FREE_DISK_MB`. Running
sandboxes are not stopped by it, so the floor protects against new work and does not reserve
space. Choose `KIONGA_SANDBOX_MAX_SANDBOXES` x `KIONGA_SANDBOX_DISK_MB` with the disk size in
mind. If the images are on another filesystem than the volumes, the floor does not see it.

Kubernetes claims are bounded by the storage class, so none of this runs there.

## Failure modes

- The Docker disk limit is a usage check with a write window, not a hard limit. See "Disk".
- Each exec, write, clone and push waits for a disk meter, which took 0.4 to 2.2 seconds in CI.
- Podman's Docker-compatible API ignores `HostConfig.Runtime` and uses its default runtime.
  Seen on 4.9.3 and 6.1.4. With `KIONGA_SANDBOX_RUNTIME=runsc`, creates return 503 unless
  runsc is Podman's default runtime, which applies to every container on that engine.
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
- Under gVisor the pid limit counts the sentry's host threads, not guest processes, and
  reaching it stops the sandbox rather than failing one fork. CI measured about 130 guest
  processes at the default of 512. The next call restarts it, so a build with very high
  process counts should raise `KIONGA_SANDBOX_PIDS`. The memory limit behaves the same.
- A sandbox restart keeps `/workspace` and loses `/tmp` and running processes.
- `KIONGA_SANDBOX_MAX_SANDBOXES` is checked then created, so concurrent creates can pass
  it by a few.
