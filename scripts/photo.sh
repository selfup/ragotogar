#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

"$SCRIPT_DIR/cashier.sh" photo "$1" "$2"
"$SCRIPT_DIR/cashier.sh" build "$2" "$3"

open "$3"
