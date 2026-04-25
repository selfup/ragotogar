#!/usr/bin/env bash
# Run the ragotogar web landing page.
#
# Usage:
#   ./scripts/web.sh                              # default: :8080, dir=describe_output
#   ./scripts/web.sh -dir descriptions            # serve a different photo dir
#   ./scripts/web.sh -addr :9000                  # different port
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"
go run ./cmd/web -repo "$REPO" "$@"
