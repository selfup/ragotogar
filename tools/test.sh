#!/usr/bin/env bash
# Run the tools/ pytest suite.
#
# Usage:
#   ./tools/test.sh
#   ./tools/test.sh -k parse_float        # filter to matching tests
#   ./tools/test.sh -v                    # verbose
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

uv run --project "$SCRIPT_DIR" pytest "$SCRIPT_DIR" "$@"
