#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "Syncing dependencies with uv..."
uv sync --project "$SCRIPT_DIR"

echo "Done."
