#!/usr/bin/env bash
# Index photos from the Postgres library into the v12 three-store vector
# schema (photo_descriptions / photo_metadata / photo_queries).
#
# Usage:
#   ./scripts/index.sh                                       # incremental — populates missing rows in all three stores
#   EMBED_MODEL=text-embedding-qwen3-embedding-0.6b ./scripts/index.sh -reindex=descriptions,metadata,queries
#   ./scripts/index.sh -batch-size 10                        # up to 10 separate texts per embed request (default)
#   ./scripts/index.sh -batch-size 1                         # single-input requests
#   ./scripts/index.sh -reindex=descriptions                 # invalidate descriptions store, re-populate
#   ./scripts/index.sh -reindex=descriptions,queries         # multiple stores
#   ./scripts/index.sh -workers 16                           # parallel batch workers (1 for local, 8–16 for cloud)
#   ./scripts/index.sh -dsn postgres:///other                # different library
#
# Restart without -reindex to resume missing stores. Passing -reindex again
# forces another rebuild of the listed stores. Progress shows rows added in
# this invocation; counters reset on restart. Photos with no generated query
# source aren't queued solely because their photo_queries store is empty.
# Batches collect one store's inputs across photos; a long description's
# chunks and generated query phrasings each count as separate inputs. Each
# photo/store is committed only after all of its embeddings succeed.
#
# Dimension changes require a full -reindex. The model is probed first, then
# the three derived stores are cleared and resized atomically. Keep the same
# EMBED_MODEL / EMBED_DIM settings for indexing, resume, and search.
#
# Env: LIBRARY_DSN, EMBED_ENDPOINT (or legacy LM_STUDIO_BASE), EMBED_MODEL, EMBED_DIM, LLM_API_KEY
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR/.."
go run ./cmd/index "$@"
