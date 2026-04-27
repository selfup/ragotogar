#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

if [[ -d "$SCRIPT_DIR/.venv" ]]; then
  echo "Removing existing .venv for a clean rebuild..."
  rm -rf "$SCRIPT_DIR/.venv"
fi

echo "Syncing dependencies with uv..."
uv sync --project "$SCRIPT_DIR"

echo "Done."
