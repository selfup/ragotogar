#!/usr/bin/env bash
# One-shot backfill: thumbnails (from photos.file_path → magick) + inference
# rows (from describe_*/<name>.json sidecars). Use this when an old library.db
# was populated by sql_sync.py before Phase 1.5 made cmd/describe write both
# tables on insert.
#
# Usage:
#   ./tools/bootstrap_thumbs_inference.sh
#   ./tools/bootstrap_thumbs_inference.sh --skip-inference
#   ./tools/bootstrap_thumbs_inference.sh --json-roots describe_april
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
uv run --project "$SCRIPT_DIR" python "$SCRIPT_DIR/bootstrap_thumbs_inference.py" "$@"
