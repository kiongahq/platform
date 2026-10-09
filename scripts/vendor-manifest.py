#!/usr/bin/env python3
"""Verify vendored browser libraries against upstream and write their manifest.

Each vendored file is compared with the SHA-256 that the jsDelivr package API
publishes for the same npm package version (an independent channel from the
file download). The manifest records the pinned version, the upstream path and
the SHA-384 used for Subresource Integrity. tests/ui/vendor-integrity.test.cjs
re-checks the manifest on every gate run, so a tampered or accidentally edited
vendor file fails CI.

    python3 scripts/vendor-manifest.py            # verify upstream + rewrite manifests
    python3 scripts/vendor-manifest.py --offline  # recompute local hashes only
"""

import base64
import hashlib
import json
import pathlib
import sys
import urllib.request

WEB = pathlib.Path(__file__).resolve().parent.parent / "go" / "cmd" / "gateway" / "web"

# directory -> list of (local file, npm package, version, upstream path)
VENDORED = {
    "vendor/editorjs": [
        ("editorjs.umd.js", "@editorjs/editorjs", "2.31.7", "/dist/editorjs.umd.js"),
        ("header.umd.js", "@editorjs/header", "2.8.9", "/dist/header.umd.js"),
        ("list.umd.js", "@editorjs/list", "2.0.9", "/dist/editorjs-list.umd.js"),
        ("quote.umd.js", "@editorjs/quote", "2.7.6", "/dist/quote.umd.js"),
        ("code.umd.js", "@editorjs/code", "2.9.4", "/dist/code.umd.js"),
        ("delimiter.umd.js", "@editorjs/delimiter", "1.4.2", "/dist/delimiter.umd.js"),
        ("embed.umd.js", "@editorjs/embed", "2.8.0", "/dist/embed.umd.js"),
        ("table.umd.js", "@editorjs/table", "2.4.6", "/dist/table.umd.js"),
        ("inline-code.umd.js", "@editorjs/inline-code", "1.5.2", "/dist/inline-code.umd.js"),
        ("marker.umd.js", "@editorjs/marker", "1.4.0", "/dist/marker.umd.js"),
        ("warning.umd.js", "@editorjs/warning", "1.4.1", "/dist/warning.umd.js"),
    ],
    "vendor/highlight": [
        ("core.min.js", "@highlightjs/cdn-assets", "11.11.1", "/es/core.min.js"),
        ("github-dark.min.css", "@highlightjs/cdn-assets", "11.11.1", "/styles/github-dark.min.css"),
    ] + [
        (f"languages/{name}.min.js", "@highlightjs/cdn-assets", "11.11.1", f"/es/languages/{name}.min.js")
        for name in ("bash", "dockerfile", "go", "ini", "javascript", "json", "plaintext", "python",
                     "shell", "sql", "typescript", "xml", "yaml")
    ],
}


def upstream_hashes(package, version, cache={}):
    key = (package, version)
    if key not in cache:
        url = f"https://data.jsdelivr.com/v1/packages/npm/{package}@{version}?structure=flat"
        with urllib.request.urlopen(url, timeout=120) as response:
            listing = json.load(response)
        cache[key] = {item["name"]: item["hash"] for item in listing["files"]}
    return cache[key]


def main(offline):
    failures = []
    for directory, files in VENDORED.items():
        entries = []
        for local, package, version, upstream in files:
            data = (WEB / directory / local).read_bytes()
            sha256 = base64.b64encode(hashlib.sha256(data).digest()).decode()
            if not offline:
                expected = upstream_hashes(package, version).get(upstream)
                if expected != sha256:
                    failures.append(f"{directory}/{local}: sha256 {sha256} != upstream {expected}")
            sri = "sha384-" + base64.b64encode(hashlib.sha384(data).digest()).decode()
            entries.append({"file": local, "package": package, "version": version, "upstream": upstream, "integrity": sri})
        manifest = {"verified_against": "jsDelivr package API sha256", "files": entries}
        (WEB / directory / "MANIFEST.json").write_text(json.dumps(manifest, indent=2) + "\n")
        print(f"wrote {directory}/MANIFEST.json ({len(entries)} files)")
    if failures:
        print("\n".join(failures), file=sys.stderr)
        return 1
    print("all vendored files match upstream" if not offline else "local hashes recorded (offline)")
    return 0


if __name__ == "__main__":
    sys.exit(main("--offline" in sys.argv))
