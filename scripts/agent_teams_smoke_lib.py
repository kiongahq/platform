"""Plumbing for scripts/agent_teams_smoke.py: real processes, an HTTPS git server, a
counting HTTP backend, a pass-through recorder and the API client. Standard library
only, apart from the schema check in the scenarios.

Nothing here stands in for a Kionga component. The git server stands in for a remote,
and the backend stands in for a customer's API, as the attack suite and the designer
smoke do.
"""

from __future__ import annotations

import base64
import gzip
import http.cookiejar
import json
import os
import re
import signal
import ssl
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Callable

GIT_ENV = {"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
           "GIT_AUTHOR_NAME": "smoke", "GIT_AUTHOR_EMAIL": "smoke@example.invalid",
           "GIT_COMMITTER_NAME": "smoke", "GIT_COMMITTER_EMAIL": "smoke@example.invalid"}


def note(text: str) -> None:
    print(f"    {text}", flush=True)


def wait_for(what: str, probe: Callable[[], Any], timeout: float = 60, every: float = 0.25) -> Any:
    """Polls until probe returns something truthy. A timeout is a scenario failure."""
    deadline = time.monotonic() + timeout
    last: Exception | None = None
    while time.monotonic() < deadline:
        try:
            value = probe()
            if value:
                return value
        except (urllib.error.URLError, ConnectionError, TimeoutError, OSError) as error:
            last = error
        time.sleep(every)
    raise AssertionError(f"timed out after {timeout:.0f}s waiting for {what}" + (f" (last error: {last})" if last else ""))


class Process:
    """One real process with its log. kill9 is SIGKILL with no chance to clean up."""

    def __init__(self, name: str, argv: list[str], env: dict[str, str], log: Path, cwd: str | None = None):
        self.name, self.argv, self.env, self.log, self.cwd = name, argv, env, log, cwd
        self.popen: subprocess.Popen[bytes] | None = None

    @property
    def alive(self) -> bool:
        return self.popen is not None and self.popen.poll() is None

    def start(self) -> None:
        if self.alive:
            raise AssertionError(f"{self.name} is already running")
        handle = open(self.log, "ab")
        handle.write(f"\n=== start {time.strftime('%H:%M:%S')} ===\n".encode())
        handle.flush()
        self.popen = subprocess.Popen(self.argv, env=self.env, cwd=self.cwd, stdout=handle, stderr=subprocess.STDOUT)
        handle.close()

    def kill9(self) -> None:
        if not self.alive:
            raise AssertionError(f"{self.name} is not running, so it cannot be killed")
        assert self.popen is not None
        self.popen.send_signal(signal.SIGKILL)
        self.popen.wait(10)
        note(f"kill -9 {self.name} (pid {self.popen.pid})")

    def stop(self) -> None:
        if self.alive:
            assert self.popen is not None
            self.popen.terminate()
            try:
                self.popen.wait(15)
            except subprocess.TimeoutExpired:
                self.popen.kill()
                self.popen.wait(10)

    def tail(self, lines: int = 40) -> str:
        try:
            return "\n".join(self.log.read_text(errors="replace").splitlines()[-lines:])
        except OSError:
            return ""


def run(argv: list[str], check: bool = True, env: dict[str, str] | None = None, cwd: str | None = None, timeout: float = 300) -> str:
    done = subprocess.run(argv, env=env, cwd=cwd, capture_output=True, text=True, timeout=timeout, check=False)
    if check and done.returncode != 0:
        raise AssertionError(f"{' '.join(argv)} exited {done.returncode}: {done.stdout}{done.stderr}")
    return done.stdout.strip()


def make_certificates(directory: Path, address: str) -> tuple[Path, Path, Path]:
    """A throwaway CA and a server certificate for the address helpers reach us on."""
    key, cert, ca_key, ca = (directory / name for name in ("server.key", "server.crt", "ca.key", "ca.pem"))
    run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-days", "2",
         "-subj", "/CN=kionga smoke ca", "-keyout", str(ca_key), "-out", str(ca)])
    csr = directory / "server.csr"
    run(["openssl", "req", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes",
         "-subj", f"/CN={address}", "-keyout", str(key), "-out", str(csr)])
    extensions = directory / "san.cnf"
    extensions.write_text(f"subjectAltName=IP:{address}\nextendedKeyUsage=serverAuth\n")
    run(["openssl", "x509", "-req", "-in", str(csr), "-CA", str(ca), "-CAkey", str(ca_key), "-CAcreateserial",
         "-days", "2", "-extfile", str(extensions), "-out", str(cert)])
    return cert, key, ca


class GitServer:
    """HTTPS smart-HTTP around `git http-backend`. Every request needs the secret, and
    pushes per repository are counted, so "not repeated" is a number and not a belief."""

    def __init__(self, root: Path, address: str, secret: str):
        self.root, self.address, self.secret = root, address, secret
        self.root.mkdir(parents=True, exist_ok=True)
        self.lock = threading.Lock()
        self.pushes: dict[str, int] = {}
        self.requests: list[tuple[str, str, int]] = []
        self.holds: dict[str, threading.Event] = {}
        self.bad_auth = 0
        cert, key, self.ca_file = make_certificates(self.root, address)
        outer = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"
            timeout = 900

            def log_message(self, *_: Any) -> None:
                return

            def _body(self) -> bytes:
                if self.headers.get("Transfer-Encoding", "").lower() == "chunked":
                    chunks = []
                    while True:
                        size = int(self.rfile.readline().strip().split(b";")[0], 16)
                        if size == 0:
                            self.rfile.readline()
                            return b"".join(chunks)
                        chunks.append(self.rfile.read(size))
                        self.rfile.readline()
                return self.rfile.read(int(self.headers.get("Content-Length") or 0))

            def _reply(self, status: int, headers: dict[str, str], body: bytes) -> None:
                self.send_response(status)
                for name, value in headers.items():
                    self.send_header(name, value)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def _handle(self) -> None:
                parsed = urllib.parse.urlsplit(self.path)
                body = self._body()
                supplied = self.headers.get("Authorization", "")
                password = ""
                if supplied.startswith("Basic "):
                    password = base64.b64decode(supplied[6:]).decode(errors="replace").partition(":")[2]
                if password != outer.secret:
                    with outer.lock:
                        outer.bad_auth += 1 if supplied else 0
                        outer.requests.append((self.command, self.path, 401))
                    self._reply(401, {"WWW-Authenticate": 'Basic realm="smoke"'}, b"")
                    return
                repository = parsed.path.strip("/").split("/")[0].removesuffix(".git")
                is_push = self.command == "POST" and parsed.path.endswith("/git-receive-pack")
                encoded = self.headers.get("Content-Encoding", "")
                plain = gzip.decompress(body) if encoded == "gzip" else body
                # git may probe with a bare flush before a big push. That moves nothing.
                if is_push and len(plain) > 4:
                    with outer.lock:
                        outer.pushes[repository] = outer.pushes.get(repository, 0) + 1
                        hold = outer.holds.get(repository)
                    if hold is not None:
                        hold.wait(600)
                env = {"PATH": os.environ["PATH"], "GIT_PROJECT_ROOT": str(outer.root), "GIT_HTTP_EXPORT_ALL": "1",
                       "REMOTE_USER": "smoke", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
                       "PATH_INFO": parsed.path, "QUERY_STRING": parsed.query, "REQUEST_METHOD": self.command,
                       "CONTENT_TYPE": self.headers.get("Content-Type", ""), "CONTENT_LENGTH": str(len(plain)),
                       "HTTP_GIT_PROTOCOL": self.headers.get("Git-Protocol", "")}
                done = subprocess.run(["git", "http-backend"], input=plain, env=env, capture_output=True, timeout=300, check=False)
                head, _, payload = done.stdout.partition(b"\r\n\r\n")
                headers, status = {}, 200
                for line in head.decode(errors="replace").split("\r\n"):
                    name, _, value = line.partition(":")
                    if name.lower() == "status":
                        status = int(value.strip().split()[0])
                    elif name:
                        headers[name] = value.strip()
                with outer.lock:
                    outer.requests.append((self.command, self.path, status))
                self._reply(status, headers, payload)

            def do_GET(self) -> None:
                self._handle()

            def do_POST(self) -> None:
                self._handle()

        self.server = ThreadingHTTPServer(("0.0.0.0", 0), Handler)  # helpers reach it over the engine's bridge
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(cert, key)
        self.server.socket = context.wrap_socket(self.server.socket, server_side=True)
        self.port = self.server.server_address[1]
        self.host = f"{address}:{self.port}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def url(self, name: str) -> str:
        return f"https://{self.host}/{name}.git"

    def seed(self, name: str) -> str:
        """A bare repository with one commit on main. Returns the commit."""
        bare = self.root / f"{name}.git"
        run(["git", "init", "--quiet", "--bare", "--initial-branch=main", str(bare)], env={**os.environ, **GIT_ENV})
        run(["git", "-C", str(bare), "config", "http.receivepack", "true"], env={**os.environ, **GIT_ENV})
        work = self.root / f"{name}.work"
        run(["git", "init", "--quiet", "--initial-branch=main", str(work)], env={**os.environ, **GIT_ENV})
        (work / "README.md").write_text("# seed\nvalue = old\n")
        run(["git", "-C", str(work), "add", "."], env={**os.environ, **GIT_ENV})
        run(["git", "-C", str(work), "commit", "--quiet", "-m", "seed"], env={**os.environ, **GIT_ENV})
        run(["git", "-C", str(work), "push", "--quiet", str(bare), "main"], env={**os.environ, **GIT_ENV})
        return run(["git", "-C", str(bare), "rev-parse", "refs/heads/main"], env={**os.environ, **GIT_ENV})

    def refs(self, name: str) -> dict[str, str]:
        out = run(["git", "-C", str(self.root / f"{name}.git"), "for-each-ref", "--format=%(refname) %(objectname)"], env={**os.environ, **GIT_ENV})
        return dict(line.split() for line in out.splitlines())

    def git(self, name: str, *args: str) -> str:
        return run(["git", "-C", str(self.root / f"{name}.git"), *args], env={**os.environ, **GIT_ENV})

    def hold(self, name: str) -> threading.Event:
        event = threading.Event()
        with self.lock:
            self.holds[name] = event
        return event

    def push_count(self, name: str) -> int:
        with self.lock:
            return self.pushes.get(name, 0)

    def close(self) -> None:
        with self.lock:
            for event in self.holds.values():
                event.set()
        self.server.shutdown()


class CountingBackend:
    """A customer's API: counts POSTs per path. A hold blocks a path's responses until
    released, which is how a call stays in flight while the gateway dies."""

    def __init__(self, address: str = "127.0.0.1"):
        self.lock = threading.Lock()
        self.counts: dict[str, int] = {}
        self.bodies: dict[str, list[Any]] = {}
        self.holds: dict[str, threading.Event] = {}
        outer = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *_: Any) -> None:
                return

            def do_PUT(self) -> None:
                # OpenFaaS deploy: PUT /system/functions. Counted like any other call.
                self.do_POST()

            def do_POST(self) -> None:
                body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
                with outer.lock:
                    outer.counts[self.path] = outer.counts.get(self.path, 0) + 1
                    outer.bodies.setdefault(self.path, []).append(body)
                    hold = outer.holds.get(self.path)
                if hold is not None:
                    hold.wait(60)
                reply = json.dumps({"ok": True, "path": self.path}).encode()
                try:
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(reply)))
                    self.end_headers()
                    self.wfile.write(reply)
                except OSError:
                    pass  # the caller died while held, which is the point of a hold

        self.server = ThreadingHTTPServer((address, 0), Handler)
        self.url = f"http://{address}:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def hold(self, path: str) -> threading.Event:
        event = threading.Event()
        with self.lock:
            self.holds[path] = event
        return event

    def count(self, path: str) -> int:
        with self.lock:
            return self.counts.get(path, 0)

    def close(self) -> None:
        with self.lock:
            for event in self.holds.values():
                event.set()
        self.server.shutdown()


class RunnerRecorder:
    """Forwards to the real runner and keeps each request and response, so the real wire
    format can be checked against the contract. It changes nothing on the way through."""

    def __init__(self, target: str):
        self.lock = threading.Lock()
        self.exchanges: list[dict[str, Any]] = []
        outer = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *_: Any) -> None:
                return

            def _forward(self, body: bytes | None) -> None:
                headers = {name: value for name, value in self.headers.items() if name.lower() not in ("host", "content-length", "connection")}
                request = urllib.request.Request(target + self.path, data=body, headers=headers, method=self.command)
                try:
                    with urllib.request.urlopen(request, timeout=660) as response:  # noqa: S310 -- local runner URL
                        status, reply, kind = response.status, response.read(), response.headers.get("Content-Type", "")
                except urllib.error.HTTPError as error:
                    status, reply, kind = error.code, error.read(), error.headers.get("Content-Type", "")
                with outer.lock:
                    outer.exchanges.append({"path": self.path, "status": status, "request": body, "response": reply})
                self.send_response(status)
                self.send_header("Content-Type", kind)
                self.send_header("Content-Length", str(len(reply)))
                self.end_headers()
                self.wfile.write(reply)

            def do_POST(self) -> None:
                self._forward(self.rfile.read(int(self.headers.get("Content-Length") or 0)))

            def do_GET(self) -> None:
                self._forward(None)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self) -> None:
        self.server.shutdown()


class ApiError(AssertionError):
    def __init__(self, method: str, path: str, status: int, body: Any):
        super().__init__(f"{method} {path} returned {status}: {str(body)[:800]}")
        self.status, self.body = status, body


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *_: Any, **__: Any) -> None:
        return None


class Api:
    """The real HTTP API, signed in the way a person's browser is."""

    def __init__(self, origin: str, username: str, password: str):
        self.origin, self.username, self.password = origin, username, password
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar), _NoRedirect)

    def login(self) -> None:
        form = urllib.parse.urlencode({"username": self.username, "password": self.password, "return_to": "/console"}).encode()
        request = urllib.request.Request(self.origin + "/auth/local/login", data=form, method="POST",
                                         headers={"Origin": self.origin, "Content-Type": "application/x-www-form-urlencoded"})
        try:
            self.opener.open(request, timeout=30).read()
        except urllib.error.HTTPError as error:
            if error.code not in (302, 303):
                raise AssertionError(f"local sign-in returned {error.code}") from None
        if not list(self.jar):
            raise AssertionError("local sign-in returned no session")

    def _raw(self, method: str, path: str, body: Any) -> tuple[int, bytes]:
        data = None if body is None else json.dumps(body).encode()
        headers = {"Origin": self.origin, "Accept": "application/json"}
        if data is not None or method != "GET":
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(self.origin + "/api/v1" + path, data=data if data is not None else (b"{}" if method != "GET" else None), method=method, headers=headers)
        try:
            with self.opener.open(request, timeout=120) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as error:
            return error.code, error.read()

    def call(self, method: str, path: str, body: Any = None, expect: int | tuple[int, ...] | None = None) -> Any:
        status, raw = self._raw(method, path, body)
        # A restarted gateway may have forgotten the session. A person signs in again.
        if status in (301, 302, 303, 401):
            self.login()
            status, raw = self._raw(method, path, body)
        try:
            parsed = json.loads(raw) if raw else None
        except ValueError:
            parsed = raw.decode(errors="replace")
        if expect is None:
            if not 200 <= status < 300:
                raise ApiError(method, path, status, parsed)
        elif status not in (expect if isinstance(expect, tuple) else (expect,)):
            raise ApiError(method, path, status, parsed)
        return parsed

    def get(self, path: str) -> Any:
        return self.call("GET", path)

    def post(self, path: str, body: Any = None, expect: int | tuple[int, ...] | None = None) -> Any:
        return self.call("POST", path, body, expect)

    def sse(self, path: str, timeout: float = 30) -> list[tuple[str, str]]:
        """Reads a Server-Sent Events stream until `end`. Returns (event, data) pairs."""
        request = urllib.request.Request(self.origin + "/api/v1" + path, headers={"Accept": "text/event-stream", "Origin": self.origin})
        frames: list[tuple[str, str]] = []
        with self.opener.open(request, timeout=timeout) as response:
            name, data = "", ""
            for raw in response:
                line = raw.decode().rstrip("\n")
                if line.startswith("event: "):
                    name = line[7:]
                elif line.startswith("data: "):
                    data = line[6:]
                elif line == "" and name:
                    frames.append((name, data))
                    if name == "end":
                        return frames
                    name, data = "", ""
        return frames


FRAME_BODY = re.compile(r"^<kionga-data [^>]*>(.*)</kionga-data>$", re.DOTALL)


def unframe(content: str) -> str:
    match = FRAME_BODY.match(content)
    return match.group(1) if match else content


def script(steps: list[dict[str, Any]]) -> str:
    """A `[[mock-script]]` block for the mock backend, the only thing that reads it."""
    return "[[mock-script]]" + json.dumps(steps) + "[[mock-script]]"


def call(name: str, **arguments: Any) -> dict[str, Any]:
    return {"tool_calls": [{"name": name, "arguments": arguments}]}


def finish(report: str) -> dict[str, Any]:
    return call("finish", report=report)
