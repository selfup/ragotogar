#!/usr/bin/env bash
# Vector search over the photo library.
#
# Usage:
#   ./scripts/search.sh "warm light bedroom"
#   ./scripts/search.sh -retrieve "shallow depth of field"
#   ./scripts/search.sh -retrieve -verify "April photos with trees"
#   ./scripts/search.sh -dsn postgres:///other "indoor scenes"
#
# Env: LIBRARY_DSN, LM_STUDIO_BASE, SEARCH_MODEL, EMBED_MODEL
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR/.."
go run ./cmd/search "$@"
