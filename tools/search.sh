#!/usr/bin/env bash
# Search photo descriptions using a LightRAG knowledge graph.
#
# Usage:
#   ./tools/search.sh "bedroom photos with warm light"
#   ./tools/search.sh --mode naive "shallow depth of field"
#   ./tools/search.sh --mode local "what cameras were used"
#   ./tools/search.sh --mode global "summarize all indoor scenes"
#
#   # Use a different model for search (default: nemotron-3-nano-4b)
#   SEARCH_MODEL="mistralai/devstral-small-2-2512" ./tools/search.sh "warm light"
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VENV_DIR="$SCRIPT_DIR/.venv"

if [[ ! -d "$VENV_DIR" ]]; then
  echo "No venv found. Run ./setup.sh first." >&2
  exit 1
fi

source "$VENV_DIR/bin/activate"
python "$SCRIPT_DIR/search.py" "$@"
