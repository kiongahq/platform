"""Kionga workspace sidecar: project folders and structured scaffold jobs.

The helper listens on loopback (127.0.0.1:8890) inside the Jupyter and IDE
containers and is reached only through the workspace's own authenticated port
proxy (Jupyter: /proxy/8890 behind the notebook token; code-server: /proxy/8890
behind the IDE session). It exposes exactly two operations:

* ``POST /prepare-project`` creates ``/workspace/projects/<namespace>``.
* ``POST /scaffold-jobs`` runs ``kionga scaffold`` with an argv the gateway
  built from the stored project. The argv is re-validated here against a fixed
  allowlist of flags and a strict ASCII character class, run without a shell,
  in ``/workspace/projects``, with a scrubbed environment and a timeout. It
  additionally requires the shared job token (``X-Kionga-Workspace-Token``).

It never reads files back, never accepts paths, and never runs a shell.
"""

import hmac
import json
import os
import re
import shutil
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

NAMESPACE = re.compile(r"[a-z0-9][a-z0-9-]{0,61}[a-z0-9]|[a-z0-9]", re.ASCII)
VALUE = re.compile(r"[a-z0-9][a-z0-9.-]{0,62}", re.ASCII)
JOB_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9-]{0,63}", re.ASCII)
# The only flags a scaffold job may pass. --prompt (free text) and --agent
# (runs a coding agent) are deliberately absent.
SCAFFOLD_FLAGS = ("--template", "--template-version", "--framework", "--accelerator", "--profile")
OUTPUT_LIMIT = 8000
FILE_LIMIT = 500
TIMEOUT_SECONDS = int(os.environ.get("KIONGA_SCAFFOLD_TIMEOUT", "60"))
SECRET_WORDS = ("TOKEN", "SECRET", "PASSWORD", "KEY", "CREDENTIAL")
_job_lock = threading.Lock()


def prepare_project(root: Path, namespace: str) -> Path:
    if not isinstance(namespace, str) or not NAMESPACE.fullmatch(namespace):
        raise ValueError("Invalid project namespace")
    root = root.resolve()
    projects = root / "projects"
    folder = projects / namespace
    if projects.is_symlink() or folder.is_symlink():
        raise ValueError("Project directories cannot be symbolic links")
    folder.resolve().relative_to(root)
    folder.mkdir(parents=True, exist_ok=True)
    return folder


def validate_job(payload: object) -> tuple[str, list[str]]:
    """Return (namespace, argv) or raise ValueError naming the problem."""
    if not isinstance(payload, dict) or set(payload) != {"job_id", "namespace", "argv"}:
        raise ValueError("job must contain exactly job_id, namespace and argv")
    job_id, namespace, argv = payload["job_id"], payload["namespace"], payload["argv"]
    if not isinstance(job_id, str) or not JOB_ID.fullmatch(job_id):
        raise ValueError("invalid job_id")
    if not isinstance(namespace, str) or not NAMESPACE.fullmatch(namespace):
        raise ValueError("invalid project namespace")
    if not isinstance(argv, list) or not all(isinstance(item, str) for item in argv):
        raise ValueError("argv must be a list of strings")
    if len(argv) < 3 or argv[:2] != ["kionga", "scaffold"] or argv[2] != namespace:
        raise ValueError("argv must start with: kionga scaffold <namespace>")
    rest = argv[3:]
    if len(rest) % 2:
        raise ValueError("every flag needs exactly one value")
    seen: set[str] = set()
    for flag, value in zip(rest[::2], rest[1::2]):
        if flag not in SCAFFOLD_FLAGS:
            raise ValueError(f"argument {flag[:40]!r} is not allowed")
        if flag in seen:
            raise ValueError(f"argument {flag} given twice")
        seen.add(flag)
        if not VALUE.fullmatch(value):
            raise ValueError(f"value for {flag} contains characters that are not allowed")
    return namespace, list(argv)


def scrubbed_environment(projects: Path) -> dict[str, str]:
    """Only what the generator needs; no credentials reach the child."""
    env = {"PATH": os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin"), "LANG": "C.UTF-8", "KIONGA_WORKSPACE": str(projects)}
    if home := os.environ.get("HOME"):
        env["HOME"] = home
    return env


def redact(text: str) -> str:
    for name, value in os.environ.items():
        if len(value) >= 6 and any(word in name.upper() for word in SECRET_WORDS):
            text = text.replace(value, "[redacted]")
    text = re.sub(r"(?i)bearer\s+\S+", "Bearer [redacted]", text)
    return re.sub(r"(?i)\b(token|secret|password|api[_-]?key)=\S+", r"\1=[redacted]", text)


def produced_files(folder: Path) -> list[str]:
    files: list[str] = []
    for path in sorted(folder.rglob("*")):
        relative = path.relative_to(folder)
        if relative.parts and relative.parts[0] == ".git":
            continue
        if path.is_file() and not path.is_symlink():
            files.append(relative.as_posix())
            if len(files) == FILE_LIMIT:
                break
    return files


def git_status(folder: Path) -> list[str]:
    if not (folder / ".git").exists() or not shutil.which("git"):
        return []
    result = subprocess.run(
        ["git", "-C", str(folder), "status", "--porcelain", "--untracked-files=all"],
        capture_output=True, text=True, timeout=15, check=False,
    )
    return result.stdout.splitlines()[:FILE_LIMIT]


def run_scaffold(root: Path, payload: object, cli: str | None = None, timeout: int = TIMEOUT_SECONDS) -> dict:
    """Validate and run one job; the result never includes secrets."""
    namespace, argv = validate_job(payload)
    folder = prepare_project(root, namespace)
    projects = folder.parent
    cli = cli or os.environ.get("KIONGA_CLI", "/usr/local/bin/kionga")
    command = [sys.executable, cli, *argv[1:]]
    try:
        completed = subprocess.run(
            command, cwd=projects, env=scrubbed_environment(projects), capture_output=True,
            text=True, timeout=timeout, check=False, shell=False,
        )
        exit_code, output = completed.returncode, completed.stdout + completed.stderr
    except subprocess.TimeoutExpired as error:
        exit_code = 124
        partial = (error.stdout or b"") + (error.stderr or b"")
        if isinstance(partial, bytes):
            partial = partial.decode("utf-8", "replace")
        output = f"{partial}\nkionga scaffold timed out after {timeout}s"
    output = redact(output)
    return {
        "exit_code": exit_code,
        "output_tail": output[-OUTPUT_LIMIT:],
        "files": produced_files(folder),
        "git_status": git_status(folder),
        "folder": str(folder),
    }


def expected_token() -> str:
    for name in ("KIONGA_WORKSPACE_JOB_TOKEN", "JUPYTER_TOKEN", "PASSWORD"):
        if value := os.environ.get(name, ""):
            return value
    return ""


class Handler(BaseHTTPRequestHandler):
    server_version = "kionga-workspace"

    def log_message(self, format, *args):  # noqa: A002 - stdlib signature
        sys.stderr.write("kionga-workspace: " + (format % args) + "\n")

    def _json(self, status: int, payload: dict) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _read(self, limit: int) -> object:
        length = int(self.headers.get("Content-Length", "0"))
        if not 0 < length <= limit:
            raise ValueError("Invalid request size")
        return json.loads(self.rfile.read(length))

    def do_POST(self):
        if self.path == "/prepare-project":
            self._prepare()
        elif self.path == "/scaffold-jobs":
            self._scaffold()
        else:
            self.send_error(404)

    def _prepare(self):
        try:
            value = self._read(1024)
            folder = prepare_project(Path(os.environ.get("KIONGA_WORKSPACE", "/workspace")), value["namespace"])
            payload = {"folder": str(folder)}
        except (ValueError, KeyError, TypeError, OSError):
            self.send_error(400, "Unable to prepare project folder")
            return
        self._json(200, payload)

    def _scaffold(self):
        expected = expected_token()
        if not expected:
            self._json(503, {"error": "Scaffold jobs are disabled: set KIONGA_WORKSPACE_JOB_TOKEN in this workspace."})
            return
        provided = self.headers.get("X-Kionga-Workspace-Token", "")
        if not hmac.compare_digest(provided.encode(), expected.encode()):
            self._json(401, {"error": "Missing or invalid workspace job token."})
            return
        try:
            payload = self._read(4096)
        except ValueError:
            self._json(400, {"error": "Request must be a JSON job of at most 4 KiB."})
            return
        if not _job_lock.acquire(blocking=False):
            self._json(409, {"error": "Another scaffold job is running in this workspace."})
            return
        try:
            result = run_scaffold(Path(os.environ.get("KIONGA_WORKSPACE", "/workspace")), payload)
        except ValueError as error:
            self._json(422, {"error": str(error)})
            return
        except OSError as error:
            self._json(500, {"error": f"Could not run the scaffolder: {error.strerror or error}"})
            return
        finally:
            _job_lock.release()
        self._json(200, result)


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", int(os.environ.get("KIONGA_SIDECAR_PORT", "8890"))), Handler).serve_forever()
