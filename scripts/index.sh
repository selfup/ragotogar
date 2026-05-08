#!/usr/bin/env bash
# Index photos from the Postgres library into the v12 three-store vector
# schema (photo_descriptions / photo_metadata / photo_queries).
#
# Usage:
#   ./scripts/index.sh                                       # incremental — populates missing rows in all three stores
#   ./scripts/index.sh -reindex=descriptions                 # invalidate descriptions store, re-populate
#   ./scripts/index.sh -reindex=descriptions,queries         # multiple stores
#   ./scripts/index.sh -workers 16                           # parallel embed workers (1 for local, 8–16 for cloud)
#   ./scripts/index.sh -dsn postgres:///other                # different library
#
# Env: LIBRARY_DSN, EMBED_ENDPOINT (or legacy LM_STUDIO_BASE), EMBED_MODEL, LLM_API_KEY
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR/.."
go run ./cmd/index "$@"
