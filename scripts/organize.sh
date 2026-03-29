#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ORGANIZE_DIR="$SCRIPT_DIR/../cmd/organize"

cd "$ORGANIZE_DIR"
go run . "$@"
