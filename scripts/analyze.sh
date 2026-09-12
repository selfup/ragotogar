#!/usr/bin/env bash
# Read-only library reports. Usage: ./scripts/analyze.sh <report> [flags]
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR/.."
go run ./cmd/analyze "$@"
