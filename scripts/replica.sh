#!/usr/bin/env bash
# Round-robin llama.cpp proxy; optional managed servers with -spawn.
set -euo pipefail
REPLICA_REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPLICA_REPO"
go run ./cmd/replica "$@"
