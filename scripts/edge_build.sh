#!/usr/bin/env bash
# Build the seven static artifacts cmd/edge serves search out of.
# Reads from the v12 three-store pg library, emits FST + int8 vector
# lanes + payload + manifest into -out. Idempotent: same pg state in
# produces the same corpus_hash and byte-stable artifacts. See EDGE.md
# for design + locked decisions.
#
# Usage:
#   ./scripts/edge_build.sh -out /tmp/edge_artifacts \
#                           -embed-model text-embedding-qwen3-embedding-4b
#
#   ./scripts/edge_build.sh -out /tmp/edge_artifacts \
#                           -dsn postgres:///ragotogar_three_store_test \
#                           -embed-model text-embedding-qwen3-embedding-4b
#
# Required flags:
#   -out          output directory (created if missing)
#   -embed-model  operator-asserted embedder version recorded per lane
#                 in manifest.json. MUST match the EMBED_MODEL that
#                 cmd/index used to populate photo_descriptions /
#                 photo_metadata / photo_queries — cmd/edge runtime
#                 fails loudly at startup if its EMBED_MODEL diverges.
#
# Optional flags:
#   -dsn          Postgres library DSN (default: LIBRARY_DSN env or
#                 postgres:///ragotogar)
#
# Output (in -out dir):
#   terms.fst                          vellum FST: lexeme → uint64 offset
#   postings.bin                       per-term varint-packed compact-id deltas
#   vectors.{descriptions,metadata,queries}.bin       flat int8 [rows × 2560]
#   vectors.{descriptions,metadata,queries}.rowmap.bin uint32 LE × rows
#   payload.bin                        per-compact-id caption + 5 tag enums
#   manifest.json                      schema_version, corpus_hash, dim,
#                                      quantization, lanes, id_space, payload
#
# Re-running against an unchanged corpus yields a byte-identical
# corpus_hash; re-describe / re-classify activity invalidates it.
#
# Env: LIBRARY_DSN
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR/.."
go run ./cmd/edge_build "$@"
