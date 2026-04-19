#!/usr/bin/env bash
set -euo pipefail

if ! command -v rclone >/dev/null 2>&1; then
  echo "FAILURE: rclone not installed (brew install rclone)" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CLONE_SH="$SCRIPT_DIR/clone.sh"
TEST_DIR="/tmp/clone_test_$$"
LOG="$TEST_DIR/out.log"
FAIL=0

mkdir -p "$TEST_DIR/src/JPEG"/{January1st2025,February15th2025,March3rd2024,June10th2024,December25th2023}
mkdir -p "$TEST_DIR/src/HIF"/{March1st2025,April5th2024}
mkdir -p "$TEST_DIR/src/MOV/January8th2025"
mkdir -p "$TEST_DIR/dest"

for d in "$TEST_DIR"/src/JPEG/*/; do touch "$d/test.jpg"; done
for d in "$TEST_DIR"/src/HIF/*/; do touch "$d/test.hif"; done
touch "$TEST_DIR/src/MOV/January8th2025/test.mov"
touch "$TEST_DIR/src/toplevel.txt"

run_test() {
  local name="$1"
  shift
  rm -rf "$TEST_DIR/dest"
  mkdir -p "$TEST_DIR/dest"
  bash "$CLONE_SH" "$@" "$TEST_DIR/src" "$TEST_DIR/dest" >"$LOG" 2>&1

  local files
  files="$(cd "$TEST_DIR/dest" && find . -type f | sed 's|^\./||' | sort)"
  echo "=== $name ==="
  echo "$files"
  echo ""
}

echo "--- Test 1: Single month (Mar = March onward, all years) ---"
run_test "single month" -m Mar
# Expect: March, April, June, December folders across all years (no Jan/Feb)

echo "--- Test 2: Comma-delimited (Feb,Jun) ---"
run_test "comma months" -m "Feb,Jun"
# Expect: only February and June folders

echo "--- Test 3: Year only (2024) ---"
run_test "year only" -y 2024
# Expect: only *2024 folders

echo "--- Test 4: Month + Year (Mar, 2025) ---"
run_test "month+year" -m Mar -y 2025
# Expect: March onward but only 2025

echo "--- Test 5: No filters ---"
run_test "no filters"
# Expect: everything including toplevel.txt

echo "--- Test 6: --no-videos with year (2025) ---"
run_test "no-videos+year" --no-videos -y 2025
# Expect: 2025 folders but no MOV

echo "--- Test 7: Year with no matches (1999) ---"
run_test "empty year" -y 1999
# Expect: nothing

rm -rf "$TEST_DIR"
echo "All tests ran. Cleanup done."
