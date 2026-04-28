#!/usr/bin/env bash
# Sync photo description JSONs into a SQLite metadata index.
#
# Usage:
#   ./tools/sql_sync.sh /path/to/describe_dir
#   ./tools/sql_sync.sh --reset /path/to/describe_dir
#   ./tools/sql_sync.sh --db /path/to/other.db /path/to/describe_dir
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

uv run --project "$SCRIPT_DIR" python "$SCRIPT_DIR/sql_sync.py" "$@"
