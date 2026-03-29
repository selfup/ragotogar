#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SYNC_SH="$SCRIPT_DIR/sync_to_nas.sh"
TEST_DIR="/tmp/sync_to_nas_test_$$"
FAIL=0

pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1"; FAIL=1; }

cleanup() { rm -rf "$TEST_DIR"; }
trap cleanup EXIT

# ---------- Test 1: Basic sync ----------
echo "--- Test 1: Basic sync ---"
mkdir -p "$TEST_DIR/src/CameraA" "$TEST_DIR/src/CameraB" "$TEST_DIR/dest"
echo "a1" > "$TEST_DIR/src/CameraA/IMG_001.jpg"
echo "a2" > "$TEST_DIR/src/CameraA/IMG_002.jpg"
echo "b1" > "$TEST_DIR/src/CameraB/VID_001.mov"
echo "top" > "$TEST_DIR/src/toplevel.txt"

bash "$SYNC_SH" "$TEST_DIR/src" "$TEST_DIR/dest" >/dev/null 2>&1

[[ -f "$TEST_DIR/dest/toplevel.txt" ]] && pass "top-level file synced" || fail "top-level file missing"
[[ -f "$TEST_DIR/dest/CameraA/IMG_001.jpg" ]] && pass "CameraA/IMG_001.jpg synced" || fail "CameraA/IMG_001.jpg missing"
[[ -f "$TEST_DIR/dest/CameraA/IMG_002.jpg" ]] && pass "CameraA/IMG_002.jpg synced" || fail "CameraA/IMG_002.jpg missing"
[[ -f "$TEST_DIR/dest/CameraB/VID_001.mov" ]] && pass "CameraB/VID_001.mov synced" || fail "CameraB/VID_001.mov missing"
echo ""

# ---------- Test 2: Idempotent re-run ----------
echo "--- Test 2: Idempotent re-run ---"
# Capture dest file mtimes before re-run
mtime_before=$(stat -f %m "$TEST_DIR/dest/CameraA/IMG_001.jpg")
sleep 1

bash "$SYNC_SH" "$TEST_DIR/src" "$TEST_DIR/dest" >/dev/null 2>&1

mtime_after=$(stat -f %m "$TEST_DIR/dest/CameraA/IMG_001.jpg")
[[ "$mtime_before" == "$mtime_after" ]] && pass "file not re-copied (--update)" || fail "file was re-copied unnecessarily"
echo ""

# ---------- Test 3: Incremental copy ----------
echo "--- Test 3: Incremental copy ---"
echo "new" > "$TEST_DIR/src/CameraA/IMG_003.jpg"

bash "$SYNC_SH" "$TEST_DIR/src" "$TEST_DIR/dest" >/dev/null 2>&1

[[ -f "$TEST_DIR/dest/CameraA/IMG_003.jpg" ]] && pass "new file synced" || fail "new file missing"
echo ""

# ---------- Test 4: .DS_Store and ._ files excluded ----------
echo "--- Test 4: Excludes .DS_Store and ._ files ---"
echo "junk" > "$TEST_DIR/src/CameraA/.DS_Store"
echo "fork" > "$TEST_DIR/src/CameraA/._IMG_001.jpg"
echo "topjunk" > "$TEST_DIR/src/.DS_Store"

bash "$SYNC_SH" "$TEST_DIR/src" "$TEST_DIR/dest" >/dev/null 2>&1

[[ ! -f "$TEST_DIR/dest/CameraA/.DS_Store" ]] && pass ".DS_Store excluded from camera dir" || fail ".DS_Store was copied"
[[ ! -f "$TEST_DIR/dest/CameraA/._IMG_001.jpg" ]] && pass "._IMG_001.jpg excluded" || fail "._IMG_001.jpg was copied"
[[ ! -f "$TEST_DIR/dest/.DS_Store" ]] && pass "top-level .DS_Store excluded" || fail "top-level .DS_Store was copied"
echo ""

# ---------- Test 5: Nested subdirectories in camera folders ----------
echo "--- Test 5: Nested subdirectories ---"
mkdir -p "$TEST_DIR/src/CameraA/DCIM/100CANON"
echo "nested" > "$TEST_DIR/src/CameraA/DCIM/100CANON/IMG_100.jpg"

bash "$SYNC_SH" "$TEST_DIR/src" "$TEST_DIR/dest" >/dev/null 2>&1

[[ -f "$TEST_DIR/dest/CameraA/DCIM/100CANON/IMG_100.jpg" ]] && pass "nested file synced" || fail "nested file missing"
echo ""

# ---------- Test 6: Source not modified ----------
echo "--- Test 6: Source files unchanged ---"
src_count=$(find "$TEST_DIR/src" -type f | wc -l | tr -d ' ')
[[ "$src_count" -gt 0 ]] && pass "source still has files" || fail "source files missing"
[[ "$(cat "$TEST_DIR/src/CameraA/IMG_001.jpg")" == "a1" ]] && pass "source content intact" || fail "source content changed"
echo ""

# ---------- Test 7: Invalid arguments ----------
echo "--- Test 7: Invalid arguments ---"
if bash "$SYNC_SH" >/dev/null 2>&1; then
  fail "should exit non-zero with no args"
else
  pass "exits non-zero with no args"
fi

if bash "$SYNC_SH" "/tmp/nonexistent_$$" "$TEST_DIR/dest" >/dev/null 2>&1; then
  fail "should exit non-zero for nonexistent source"
else
  pass "exits non-zero for nonexistent source"
fi

if bash "$SYNC_SH" "$TEST_DIR/src" "/tmp/nonexistent_$$" >/dev/null 2>&1; then
  fail "should exit non-zero for nonexistent dest"
else
  pass "exits non-zero for nonexistent dest"
fi
echo ""

# ---------- Summary ----------
if [[ "$FAIL" -eq 0 ]]; then
  echo "All tests passed."
else
  echo "Some tests FAILED."
  exit 1
fi
