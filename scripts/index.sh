#!/usr/bin/env bash
# Index photo descriptions from the Postgres library into pgvector.
#
# Usage:
#   ./scripts/index.sh
#   ./scripts/index.sh -reindex                  # TRUNCATE chunks, re-embed all
#   ./scripts/index.sh -workers 16               # parallel embed workers (1 for local, 8–16 for cloud)
#   ./scripts/index.sh -dsn postgres:///other    # different library
#
# Env: LIBRARY_DSN, EMBED_ENDPOINT (or legacy LM_STUDIO_BASE), EMBED_MODEL, LLM_API_KEY
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR/.."
go run ./cmd/index "$@"
