#!/usr/bin/env bash
# Installs one pinned, checksum-verified gVisor release for the sandbox attack suite in
# CI. The checksum lives in this file, so a change at the download location fails the
# job rather than changing what it tests. It runs on a throwaway Linux runner, as root.
set -euo pipefail

RELEASE=20261026.0
SHA512=26e4a327565f5da0f73261a6387e2c20f4695a232e330095a3111b70efd604fdf09d734e8cc8f714dbb5c5607ce8c87ea80b26df549d83a146f2efd5a1af448a
DIR=/opt/gvisor

[[ "$(uname -m)" == x86_64 ]] || { echo "this pin is for x86_64" >&2; exit 1; }
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
curl --fail --silent --show-error --location --retry 3 --output "$work/gvisor.tar.bz2" \
  "https://storage.googleapis.com/gvisor/releases/release/$RELEASE/x86_64/gvisor.tar.bz2"
echo "$SHA512  $work/gvisor.tar.bz2" | sha512sum --check --quiet
mkdir -p "$DIR"
tar -xjf "$work/gvisor.tar.bz2" -C "$DIR"
"$DIR/runsc" --version
