#!/usr/bin/env bash
set -euo pipefail

# scripts/loadtest-upload.sh:
# Creates a 1.5 GiB sparse file, runs the upload load test, and checks peak RSS < 200 MiB.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

TMP_DIR="$(mktemp -d -t tgdl-loadtest-XXXXXX)"
SPARSE_FILE="$TMP_DIR/sparse_1500M.bin"

cleanup() {
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT

echo "Creating 1.5 GiB sparse file at $SPARSE_FILE..."
truncate -s 1500M "$SPARSE_FILE"

echo "Building loadtest helper..."
go build -o "$TMP_DIR/loadtest_runner" ./cmd/loadtest

echo "Running upload load test..."
"$TMP_DIR/loadtest_runner" "$SPARSE_FILE"
