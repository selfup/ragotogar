#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DESCRIBE_DIR="$SCRIPT_DIR/../cmd/describe"

cd "$DESCRIBE_DIR"
go run . "$@"
