#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
FAIL=0

# -race is on by default so concurrent code paths (cmd/index workers,
# library.SearchV2's per-store goroutines, library.VerifyFilter's 8-way
# pool, cmd/describe / cmd/classify worker pools) get exercised under
# the race detector on every run. Set TEST_RACE=0 to opt out for the
# ~30% speed-up when iterating on non-concurrent code.
RACE_FLAG="-race"
if [ "${TEST_RACE:-1}" = "0" ]; then
  RACE_FLAG=""
fi

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

# Go tests — sub-modules (cmd/organize, cmd/describe each have their own go.mod)
for mod in "$ROOT_DIR"/cmd/*/; do
  [ -f "$mod/go.mod" ] || continue
  name="go test $(basename "$mod")"
  run "$name" bash -c "cd '$mod' && go test $RACE_FLAG -count=1 ./..."
done

# Go tests — root module (cmd/cashier and any future root-module packages)
if [ -f "$ROOT_DIR/go.mod" ]; then
  run "go test (root module)" bash -c "cd '$ROOT_DIR' && go test $RACE_FLAG -count=1 ./..."
fi

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
