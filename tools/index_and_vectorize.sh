#!/usr/bin/env bash
# Index photo description JSONs into a LightRAG knowledge graph.
#
# Usage:
#   ./tools/index_and_vectorize.sh /path/to/description_jsons
#   ./tools/index_and_vectorize.sh --reindex /path/to/description_jsons
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VENV_DIR="$SCRIPT_DIR/.venv"

if [[ ! -d "$VENV_DIR" ]]; then
  echo "No venv found. Run ./setup.sh first." >&2
  exit 1
fi

source "$VENV_DIR/bin/activate"
python "$SCRIPT_DIR/index_and_vectorize.py" "$@"
