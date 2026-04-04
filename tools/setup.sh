#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VENV_DIR="$SCRIPT_DIR/.venv"

if [[ ! -d "$VENV_DIR" ]]; then
  echo "Creating virtual environment..."
  python3 -m venv "$VENV_DIR"
fi

echo "Installing dependencies..."
"$VENV_DIR/bin/pip" install -q -r "$SCRIPT_DIR/requirements.txt"

echo ""
echo "Setup complete. Activate with:"
echo "  source $VENV_DIR/bin/activate"
echo ""
echo "Then run:"
echo "  python search.py index /path/to/description_jsons"
echo "  python search.py query \"bedroom with warm light\""
