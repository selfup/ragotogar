#!/usr/bin/env bash
# Index photo descriptions from the SQL library into a LightRAG knowledge graph.
#
# Usage:
#   ./tools/index_and_vectorize.sh
#   ./tools/index_and_vectorize.sh --reindex
#   ./tools/index_and_vectorize.sh --db /path/to/other.db
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

uv run --project "$SCRIPT_DIR" python "$SCRIPT_DIR/index_and_vectorize.py" "$@"
