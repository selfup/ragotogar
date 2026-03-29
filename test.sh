#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
FAIL=0

run() {
  local name="$1"
  shift
  echo "========================================"
  echo "  $name"
  echo "========================================"
  if "$@"; then
    echo "  ✓ $name passed"
  else
    echo "  ✗ $name FAILED"
    FAIL=1
  fi
  echo ""
}

# Go tests
for mod in "$ROOT_DIR"/cmd/*/; do
  [ -f "$mod/go.mod" ] || continue
  name="go test $(basename "$mod")"
  run "$name" bash -c "cd '$mod' && go test -count=1 ./..."
done

# Bash tests
for test_script in "$ROOT_DIR"/scripts/*_test.sh; do
  [ -f "$test_script" ] || continue
  name="$(basename "$test_script")"
  run "$name" bash "$test_script"
done

echo "========================================"
if [[ "$FAIL" -eq 0 ]]; then
  echo "  All test suites passed."
else
  echo "  Some test suites FAILED."
  exit 1
fi
echo "========================================"
