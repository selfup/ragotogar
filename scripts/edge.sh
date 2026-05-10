#!/usr/bin/env bash
# Run the ragotogar edge search server. Loads the artifacts cmd/edge_build
# produced and serves search out of them via HTTP. pg stays the system of
# record + hydration store but is NOT in the search query path. See
# EDGE.md for design + URL contract.
#
# Usage:
#   ./scripts/edge.sh -artifacts /tmp/edge_artifacts
#
#   ./scripts/edge.sh -artifacts /tmp/edge_artifacts \
#                     -dsn postgres:///ragotogar_three_store_test \
#                     -addr :8081
#
# Required flags:
#   -artifacts    directory produced by ./scripts/edge_build.sh
#
# Optional flags:
#   -addr         HTTP listen address (default: :8081)
#   -dsn          Postgres library DSN (default: LIBRARY_DSN env or
#                 postgres:///ragotogar). Used for liveness check at
#                 startup; downstream hydration is the caller's job.
#   -embed-model  embedder model name. Symmetric with cmd/edge_build's
#                 flag — pass the same value here. Overrides
#                 EMBED_MODEL env. Required (via flag or env) for the
#                 per-lane drift check; cmd/edge fails loudly at
#                 startup if its value differs from each lane's
#                 manifest.embedder_version. Re-build artifacts or
#                 correct the value to match.
#
# Optional env:
#   EMBED_ENDPOINT, LM_STUDIO_BASE   query-encode endpoint
#   LLM_API_KEY                       bearer token for cloud providers
#
# Endpoints:
#   GET /health
#       returns corpus_hash, photo count, manifest version, quantization.
#
#   GET /search?q=<query>&...
#       parameters (mirror cmd/web's URL contract):
#         q              required; phrase-quoted queries return HTTP 400
#                        (FST has no position info — see EDGE.md)
#         vector         1|0 (default 1) — enable vector arm
#         lexical        1|0 (default 1) — enable FST arm
#         descriptions   1|0 (default 1) — include photo_descriptions
#         metadata       1|0 (default 1) — include photo_metadata
#         queries        1|0 (default 1) — include photo_queries
#         merge          union|intersect|weighted (default union)
#         wd|wm|wq       per-store weights under merge=weighted
#         cosine         per-lane cosine floor (default 0.50; the
#                        live corpus has low embedder cosines, drop
#                        to 0.30 for descriptive queries)
#         topk           response cap (default 0 = unbounded; cosine
#                        threshold is the only bound, matching
#                        cmd/web's behavior — pass ?topk=N to truncate)
#       response: {query, stripped_query, negation, elapsed_ms,
#                  vector_arm{}, fst_arm{}, fused_total, after_negation,
#                  hits[{compact_id, name, caption, tags{}, score}]}
#
# Workflow:
#   1. Run ./scripts/edge_build.sh to produce/refresh artifacts.
#   2. Run this script to serve. Re-run cmd/edge_build whenever
#      describe/classify/index produces new pg rows you want
#      reachable at the edge.
#
# Env: LIBRARY_DSN, EMBED_MODEL, EMBED_ENDPOINT, LM_STUDIO_BASE, LLM_API_KEY
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR/.."
go run ./cmd/edge "$@"
