#!/usr/bin/env bash
# Search photo descriptions using a LightRAG knowledge graph.
#
# Usage:
#   ./tools/search.sh "bedroom photos with warm light"
#   ./tools/search.sh --mode naive "shallow depth of field"
#   ./tools/search.sh --mode local "what cameras were used"
#   ./tools/search.sh --mode global "summarize all indoor scenes"
#   ./tools/search.sh --sources --mode global "summarize all indoor scenes"
#   ./tools/search.sh --retrieve "indoor scenes with warm light"
#   ./tools/search.sh --precise "what is the most common framing I use indoors"
#
#   # Use a different model for search (default: ministral-3-3b)
#   SEARCH_MODEL="mistralai/devstral-small-2-2512" ./tools/search.sh "warm light"
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

uv run --project "$SCRIPT_DIR" python "$SCRIPT_DIR/search.py" "$@"
