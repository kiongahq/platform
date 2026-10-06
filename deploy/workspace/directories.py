"""Prepare project directories for an IDE-only workspace.

The helper listens on loopback and is reached through code-server's authenticated
port proxy. It exposes no file reads, shell execution, or arbitrary path writes.
"""

import json
import os
import re
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def prepare_project(root: Path, namespace: str) -> Path:
    if not isinstance(namespace, str) or not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,61}[a-z0-9]|[a-z0-9]", namespace):
        raise ValueError("Invalid project namespace")
    root = root.resolve()
    projects = root / "projects"
    folder = projects / namespace
    if projects.is_symlink() or folder.is_symlink():
        raise ValueError("Project directories cannot be symbolic links")
    folder.resolve().relative_to(root)
    folder.mkdir(parents=True, exist_ok=True)
    return folder


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/prepare-project":
            self.send_error(404)
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= 1024:
                raise ValueError("Invalid request size")
            value = json.loads(self.rfile.read(length))
            folder = prepare_project(Path(os.environ.get("KIONGA_WORKSPACE", "/workspace")), value["namespace"])
            payload = json.dumps({"folder": str(folder)}).encode()
        except (ValueError, KeyError, TypeError, OSError):
            self.send_error(400, "Unable to prepare project folder")
            return
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", 8890), Handler).serve_forever()
