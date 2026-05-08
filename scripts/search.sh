#!/usr/bin/env bash
# Vector search over the photo library — v12 three-store schema by default.
#
# Usage (v2 — default):
#   ./scripts/search.sh "warm light bedroom"
#   ./scripts/search.sh -retrieve "shallow depth of field"
#   ./scripts/search.sh -retrieve -verify "April photos with trees"
#   ./scripts/search.sh -merge-strategy=intersect "red truck"
#   ./scripts/search.sh -merge-strategy=weighted -weight-queries=2.0 "moody portrait"
#   ./scripts/search.sh -use-queries=false -use-metadata=false "X100VI"
#   ./scripts/search.sh -dsn postgres:///other "indoor scenes"
#
# Env: LIBRARY_DSN, TEXT_ENDPOINT + EMBED_ENDPOINT (or legacy LM_STUDIO_BASE),
#      SEARCH_MODEL, EMBED_MODEL
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR/.."
go run ./cmd/search "$@"
