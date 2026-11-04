package sandbox

// Fixed shell programs. Everything variable reaches them as positional arguments, so
// a hostile path or ref is never parsed as shell source. They run from argv with a
// cleared environment and a PATH that holds only the read-only image directories, so
// sandbox contents cannot substitute any tool they call.
const (
	safePath = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

	// initScript is PID 1. A shell's wait reaps adopted orphans as well as its own
	// children, so killed process trees do not pile up as zombies against the pid
	// limit. Plain `sleep infinity` as PID 1 would never reap them.
	initScript = `while :; do sleep 3600 & wait; done`

	// sweepScript kills every process in the sandbox except PID 1. kill is a shell
	// builtin, so it needs no fork and works near the pid limit. kill -1 reaches
	// processes that left the process group with setsid, which killing a group would
	// miss. The count afterwards tells the manager whether anything outran it.
	sweepScript = `kill -KILL -1 2>/dev/null; kill -KILL -1 2>/dev/null; n=0; for d in /proc/[0-9]*; do n=$((n+1)); done; echo "$n"`

	resolveFn = `resolve() { p=$(realpath -m -- "$1") || exit 64; case "$p" in /workspace|/workspace/*) ;; *) exit 65 ;; esac; }
`
	// Exit codes the scripts below share.
	exitResolveFailed = 64
	exitPathEscape    = 65
	exitNotFound      = 66
	exitSpecialFile   = 67
	exitIsDirectory   = 68
	exitWriteFailed   = 69
	exitShortWrite    = 70
	exitTargetExists  = 71
	exitBundleFailed  = 72
	exitEmptyRepo     = 73
	exitRefNotFound   = 74
	exitDefaultBranch = 75
	exitNotADirectory = 76

	checkDirScript = resolveFn + `resolve "$1"
[ -e "$p" ] || exit 66
[ -d "$p" ] || exit 76
printf '%s' "$p"`

	// readScript prints a one-byte tag, then the file's bytes or the listing.
	readScript = resolveFn + `resolve "$1"
if [ -d "$p" ]; then
  printf D
  find "$p" -mindepth 1 -maxdepth 1 -printf '%y\t%s\t%f\0'
elif [ -f "$p" ]; then
  printf F
  head -c "$2" -- "$p"
elif [ -e "$p" ]; then
  exit 67
else
  exit 66
fi`

	// writeScript reads exactly $2 bytes from stdin, so it needs no EOF, and swaps
	// the file in atomically. The temporary file sits beside the target so the rename
	// stays on one filesystem.
	writeScript = resolveFn + `resolve "$1"
[ ! -d "$p" ] || exit 68
d=$(dirname -- "$p")
mkdir -p -- "$d" || exit 69
t=$(mktemp -p "$d" .kionga-write.XXXXXX) || exit 69
head -c "$2" > "$t" || { rm -f -- "$t"; exit 69; }
n=$(stat -c %s -- "$t")
[ "$n" = "$2" ] || { rm -f -- "$t"; exit 70; }
chmod 644 -- "$t"
mv -fT -- "$t" "$p" || { rm -f -- "$t"; exit 69; }
printf '%s' "$p"`

	// diffScript runs repository config the agent controls, as the agent's own user
	// with no credential and no network. Its output is untrusted like any other.
	diffScript = `for g in /workspace/*/.git; do
  [ -e "$g" ] || continue
  d=${g%/.git}
  echo "# repository: ${d#/workspace/}"
  git -C "$d" -c core.fsmonitor=false -c core.hooksPath=/dev/null status --short
  if git -C "$d" rev-parse --verify -q refs/kionga/base >/dev/null; then
    git -C "$d" -c core.fsmonitor=false --no-pager diff --no-ext-diff refs/kionga/base --
  else
    git -C "$d" -c core.fsmonitor=false --no-pager diff --no-ext-diff HEAD --
  fi
done`

	// ingestScript runs in the sandbox with no credential and no network. It unpacks
	// the helper's bundle, which holds only objects and refs: no config and no hooks
	// cross this boundary. The remote URL it records has no credential because the
	// manager rejects URLs with user information and the secret only ever lived on
	// the helper's tmpfs. $1 target, $2 url, $3 ref, $4 size.
	ingestScript = resolveFn + `set -eu
resolve "$1"
if [ -e "$p" ] || [ -L "$p" ]; then exit 71; fi
f=$(mktemp -p /workspace .kionga-xfer.XXXXXX)
trap 'rm -f -- "$f"' EXIT
head -c "$4" > "$f"
[ "$(stat -c %s -- "$f")" = "$4" ] || exit 70
git clone --quiet --no-hardlinks -- "$f" "$p" >&2
git -C "$p" remote set-url origin "$2"
if [ -n "$3" ]; then git -C "$p" -c advice.detachedHead=false checkout --quiet "$3" -- >&2; fi
git -C "$p" config user.name kionga-agent
git -C "$p" config user.email agent@kionga.invalid
git -C "$p" update-ref refs/kionga/base HEAD
git -C "$p" rev-parse HEAD`

	// bundleScript runs in the sandbox, against a .git the agent controls. Whatever
	// that repository's config runs, it runs here, without a credential or a network.
	// The result is opaque bytes to everything downstream. $1 repo dir, $2 branch.
	bundleScript = resolveFn + `set -eu
resolve "$1"
[ -e "$p/.git" ] || exit 66
f=$(mktemp -p /workspace .kionga-xfer.XXXXXX)
if ! git -C "$p" -c core.fsmonitor=false -c core.hooksPath=/dev/null bundle create --quiet "$f" "refs/heads/$2" >&2; then
  rm -f -- "$f"
  exit 72
fi
printf '%s\n' "$f"
stat -c %s -- "$f"`

	// helperSetupScript reads the secret and CA bundle from stdin onto the helper's
	// tmpfs. Only their lengths appear in argv. $1 secret length, $2 CA length, $3 user.
	helperSetupScript = `set -eu
umask 077
cd /run/kionga
mkdir -p home
head -c "$1" > cred
head -c "$2" > ca.pem
printf '%s' "$3" > user
cat > askpass <<'ASKPASS'
#!/bin/sh
case "$1" in
  Username*) cat /run/kionga/user ;;
  *) cat /run/kionga/cred ;;
esac
ASKPASS
chmod 700 askpass`

	// hardenedGit defines G. Hooks, fsmonitor and credential helpers are forced off
	// on every call, and redirects are refused so a redirect cannot carry the secret
	// to a host outside the allowlist. The environment already disables system and
	// global config, and these repositories are fresh ones this helper just made.
	hardenedGit = `G() { git -c core.hooksPath=/dev/null -c core.fsmonitor=false -c credential.helper= -c http.followRedirects=false -c gc.auto=0 "$@"; }
`

	// helperCloneScript fetches into an empty directory and packs a bundle. Nothing is
	// checked out in the credentialed helper, so no repository content is ever written
	// as a working tree here. The secret is deleted before any bytes leave. $1 url, $2 ref.
	helperCloneScript = hardenedGit + `set -eu
R=/scratch/r.git
G clone --quiet --bare -- "$1" "$R" >&2
rm -f /run/kionga/cred
if [ -z "$(G --git-dir="$R" for-each-ref --count=1)" ]; then exit 73; fi
if [ -n "$2" ]; then
  G --git-dir="$R" rev-parse --verify --quiet --end-of-options "$2^{commit}" >/dev/null || exit 74
fi
if G --git-dir="$R" rev-parse --verify --quiet HEAD >/dev/null; then
  G --git-dir="$R" bundle create --quiet /scratch/out.bundle --all HEAD >&2
else
  G --git-dir="$R" bundle create --quiet /scratch/out.bundle --all >&2
fi
stat -c %s /scratch/out.bundle`

	// helperReceiveScript stores exactly $1 untrusted bytes as a file, as data.
	helperReceiveScript = `set -eu
head -c "$1" > /scratch/in.bundle
[ "$(stat -c %s /scratch/in.bundle)" = "$1" ] || exit 70`

	// helperPushScript never touches a .git the agent wrote. It clones the remote
	// fresh, reads the bundle as data into a private ref, and pushes that ref without
	// a + so the remote refuses anything but a fast-forward or a new branch. Reading a
	// bundle needs the file transport, so it is allowed for those two calls only, with
	// a path this script chose. $1 url, $2 branch.
	helperPushScript = hardenedGit + `set -eu
R=/scratch/r.git
G clone --quiet --bare -- "$1" "$R" >&2
head=$(G --git-dir="$R" symbolic-ref -q HEAD || true)
if [ "$head" = "refs/heads/$2" ]; then exit 75; fi
GIT_ALLOW_PROTOCOL=file G --git-dir="$R" bundle verify --quiet /scratch/in.bundle >&2
GIT_ALLOW_PROTOCOL=file G --git-dir="$R" fetch --quiet --no-tags /scratch/in.bundle "refs/heads/$2:refs/kionga/incoming" >&2
G --git-dir="$R" cat-file -e 'refs/kionga/incoming^{commit}'
G --git-dir="$R" push --quiet --no-verify --no-follow-tags "$1" "refs/kionga/incoming:refs/heads/$2" >&2
rm -f /run/kionga/cred
G --git-dir="$R" rev-parse refs/kionga/incoming`
)
