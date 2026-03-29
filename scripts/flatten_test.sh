#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
FLATTEN_SH="$SCRIPT_DIR/flatten.sh"
TEST_DIR="/tmp/flatten_test_$$"
FAIL=0

pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1"; FAIL=1; }

cleanup() { rm -rf "$TEST_DIR"; }
trap cleanup EXIT

# ---------- Test 1: Basic flattening ----------
echo "--- Test 1: Basic flattening ---"
mkdir -p "$TEST_DIR/t1/sub1" "$TEST_DIR/t1/sub2/deep"
echo "a" > "$TEST_DIR/t1/sub1/file_a.txt"
echo "b" > "$TEST_DIR/t1/sub2/file_b.txt"
echo "c" > "$TEST_DIR/t1/sub2/deep/file_c.txt"
echo "top" > "$TEST_DIR/t1/top.txt"

bash "$FLATTEN_SH" "$TEST_DIR/t1" >/dev/null 2>&1

[[ -f "$TEST_DIR/t1/file_a.txt" ]] && pass "file_a.txt moved to root" || fail "file_a.txt missing"
[[ -f "$TEST_DIR/t1/file_b.txt" ]] && pass "file_b.txt moved to root" || fail "file_b.txt missing"
[[ -f "$TEST_DIR/t1/file_c.txt" ]] && pass "file_c.txt moved to root" || fail "file_c.txt missing"
[[ -f "$TEST_DIR/t1/top.txt" ]] && pass "top.txt still present" || fail "top.txt missing"
[[ ! -d "$TEST_DIR/t1/sub1" ]] && pass "sub1 removed" || fail "sub1 still exists"
[[ ! -d "$TEST_DIR/t1/sub2" ]] && pass "sub2 removed" || fail "sub2 still exists"
echo ""

# ---------- Test 2: Name collision handling ----------
echo "--- Test 2: Name collision handling ---"
mkdir -p "$TEST_DIR/t2/dir1" "$TEST_DIR/t2/dir2"
echo "original" > "$TEST_DIR/t2/photo.jpg"
echo "dup1" > "$TEST_DIR/t2/dir1/photo.jpg"
echo "dup2" > "$TEST_DIR/t2/dir2/photo.jpg"

bash "$FLATTEN_SH" "$TEST_DIR/t2" >/dev/null 2>&1

[[ -f "$TEST_DIR/t2/photo.jpg" ]] && pass "original photo.jpg preserved" || fail "original photo.jpg missing"
[[ -f "$TEST_DIR/t2/photo_1.jpg" ]] && pass "first collision renamed to photo_1.jpg" || fail "photo_1.jpg missing"
[[ -f "$TEST_DIR/t2/photo_2.jpg" ]] && pass "second collision renamed to photo_2.jpg" || fail "photo_2.jpg missing"

# Verify no data was lost
count=$(find "$TEST_DIR/t2" -type f | wc -l | tr -d ' ')
[[ "$count" == "3" ]] && pass "all 3 files present" || fail "expected 3 files, got $count"
echo ""

# ---------- Test 3: Skips ._ resource fork files ----------
echo "--- Test 3: Skips ._ resource fork files ---"
mkdir -p "$TEST_DIR/t3/sub"
echo "real" > "$TEST_DIR/t3/sub/photo.jpg"
echo "fork" > "$TEST_DIR/t3/sub/._photo.jpg"

bash "$FLATTEN_SH" "$TEST_DIR/t3" >/dev/null 2>&1

[[ -f "$TEST_DIR/t3/photo.jpg" ]] && pass "photo.jpg moved" || fail "photo.jpg missing"
# The ._ file should not have been moved to root
[[ ! -f "$TEST_DIR/t3/._photo.jpg" ]] && pass "._photo.jpg not moved to root" || fail "._photo.jpg was moved to root"
echo ""

# ---------- Test 4: Empty directory ----------
echo "--- Test 4: Empty directory ---"
mkdir -p "$TEST_DIR/t4"

bash "$FLATTEN_SH" "$TEST_DIR/t4" >/dev/null 2>&1

count=$(find "$TEST_DIR/t4" -type f | wc -l | tr -d ' ')
[[ "$count" == "0" ]] && pass "empty dir stays empty" || fail "unexpected files found"
echo ""

# ---------- Test 5: Empty subdirectories are removed ----------
echo "--- Test 5: Empty subdirectories are removed ---"
mkdir -p "$TEST_DIR/t5/a/b/c"
echo "file" > "$TEST_DIR/t5/a/b/c/data.txt"

bash "$FLATTEN_SH" "$TEST_DIR/t5" >/dev/null 2>&1

[[ -f "$TEST_DIR/t5/data.txt" ]] && pass "data.txt flattened" || fail "data.txt missing"
dir_count=$(find "$TEST_DIR/t5" -mindepth 1 -type d | wc -l | tr -d ' ')
[[ "$dir_count" == "0" ]] && pass "all subdirectories removed" || fail "subdirectories remain ($dir_count)"
echo ""

# ---------- Test 6: Files without extensions get collision suffix ----------
echo "--- Test 6: Files without extensions ---"
mkdir -p "$TEST_DIR/t6/sub"
echo "a" > "$TEST_DIR/t6/Makefile"
echo "b" > "$TEST_DIR/t6/sub/Makefile"

bash "$FLATTEN_SH" "$TEST_DIR/t6" >/dev/null 2>&1

[[ -f "$TEST_DIR/t6/Makefile" ]] && pass "original Makefile preserved" || fail "original Makefile missing"
[[ -f "$TEST_DIR/t6/Makefile_1" ]] && pass "collision Makefile_1 created" || fail "Makefile_1 missing"
echo ""

# ---------- Test 7: Deeply nested structure ----------
echo "--- Test 7: Deeply nested structure ---"
mkdir -p "$TEST_DIR/t7/a/b/c/d/e"
echo "deep" > "$TEST_DIR/t7/a/b/c/d/e/deep.txt"
echo "mid" > "$TEST_DIR/t7/a/b/mid.txt"

bash "$FLATTEN_SH" "$TEST_DIR/t7" >/dev/null 2>&1

[[ -f "$TEST_DIR/t7/deep.txt" ]] && pass "deep.txt flattened" || fail "deep.txt missing"
[[ -f "$TEST_DIR/t7/mid.txt" ]] && pass "mid.txt flattened" || fail "mid.txt missing"
dir_count=$(find "$TEST_DIR/t7" -mindepth 1 -type d | wc -l | tr -d ' ')
[[ "$dir_count" == "0" ]] && pass "all nesting removed" || fail "directories remain ($dir_count)"
echo ""

# ---------- Test 8: Invalid arguments ----------
echo "--- Test 8: Invalid arguments ---"
if bash "$FLATTEN_SH" >/dev/null 2>&1; then
  fail "should exit non-zero with no args"
else
  pass "exits non-zero with no args"
fi

if bash "$FLATTEN_SH" "/tmp/nonexistent_$$" >/dev/null 2>&1; then
  fail "should exit non-zero for nonexistent dir"
else
  pass "exits non-zero for nonexistent dir"
fi
echo ""

# ---------- Summary ----------
if [[ "$FAIL" -eq 0 ]]; then
  echo "All tests passed."
else
  echo "Some tests FAILED."
  exit 1
fi
