#!/usr/bin/env bash
# Run the ragotogar web server. Renders photo pages on-demand from
# tools/.sql_index/library.db (populated by cmd/describe).
#
# Usage:
#   ./scripts/web.sh                          # default: :8080
#   ./scripts/web.sh -addr :9000              # different port
#   ./scripts/web.sh -db /path/to/other.db    # different library
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"
go run ./cmd/web -repo "$REPO" "$@"
