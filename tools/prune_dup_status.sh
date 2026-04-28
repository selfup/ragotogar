#!/usr/bin/env bash
# Prune LightRAG duplicate-audit rows from kv_store_doc_status.json.
#
# Usage:
#   ./tools/prune_dup_status.sh            # dry run
#   ./tools/prune_dup_status.sh --apply    # rewrite the file (writes a .bak first)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

uv run --project "$SCRIPT_DIR" python "$SCRIPT_DIR/prune_dup_status.py" "$@"
