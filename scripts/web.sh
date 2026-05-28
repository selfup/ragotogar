#!/usr/bin/env bash
# Run the ragotogar web server. Renders photo pages on-demand from
# tools/.sql_index/library.db (populated by cmd/describe).
#
# Usage:
#   ./scripts/web.sh                          # default: 127.0.0.1:8080 (loopback only)
#   ./scripts/web.sh -addr :9000              # expose on all interfaces, port 9000
#   ./scripts/web.sh -db /path/to/other.db    # different library
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"
go run ./cmd/web -repo "$REPO" "$@"
