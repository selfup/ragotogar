#!/usr/bin/env bash
# Classify photo descriptions into typed enum fields (classified table).
#
# Usage:
#   ./scripts/classify.sh                        # incremental — skips photos already classified
#   ./scripts/classify.sh -reclassify            # TRUNCATE classified and rebuild all
#   ./scripts/classify.sh -workers 4             # tune parallelism (default 8)
#   ./scripts/classify.sh -dsn postgres:///alt   # different library
#
# Env: LIBRARY_DSN, LM_STUDIO_BASE, CLASSIFY_MODEL
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR/.."
go run ./cmd/classify "$@"
