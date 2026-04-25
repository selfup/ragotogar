#!/usr/bin/env bash
# Index photo description JSONs into a LightRAG knowledge graph.
#
# Usage:
#   ./tools/index_and_vectorize.sh /path/to/description_jsons
#   ./tools/index_and_vectorize.sh --reindex /path/to/description_jsons
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

uv run --project "$SCRIPT_DIR" python "$SCRIPT_DIR/index_and_vectorize.py" "$@"
