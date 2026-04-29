#!/usr/bin/env bash
# Index photo descriptions from the Postgres library into pgvector.
#
# Usage:
#   ./scripts/index.sh
#   ./scripts/index.sh -reindex                  # TRUNCATE chunks, re-embed all
#   ./scripts/index.sh -dsn postgres:///other    # different library
#
# Env: LIBRARY_DSN, LM_STUDIO_BASE, EMBED_MODEL
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR/.."
go run ./cmd/index "$@"
