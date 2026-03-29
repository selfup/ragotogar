#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ORGANIZE_SH="$SCRIPT_DIR/organize.sh"
TEST_DIR="/tmp/organize_test_$$"
FAIL=0

pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1"; FAIL=1; }

cleanup() { rm -rf "$TEST_DIR"; }
trap cleanup EXIT

# ---------- Test 1: Basic organize (type folders created) ----------
echo "--- Test 1: Basic organize by type ---"
mkdir -p "$TEST_DIR/t1"
touch "$TEST_DIR/t1/photo.jpg"
touch "$TEST_DIR/t1/image.heic"
touch "$TEST_DIR/t1/raw.arw"
touch "$TEST_DIR/t1/video.mov"
touch "$TEST_DIR/t1/clip.mp4"
touch "$TEST_DIR/t1/audio.wav"

bash "$ORGANIZE_SH" "$TEST_DIR/t1" >/dev/null 2>&1

[[ -d "$TEST_DIR/t1/JPEG" ]] && pass "JPEG folder created" || fail "JPEG folder missing"
[[ -d "$TEST_DIR/t1/HIF" ]] && pass "HIF folder created" || fail "HIF folder missing"
[[ -d "$TEST_DIR/t1/RAW" ]] && pass "RAW folder created" || fail "RAW folder missing"
[[ -d "$TEST_DIR/t1/MOV" ]] && pass "MOV folder created" || fail "MOV folder missing"
[[ -d "$TEST_DIR/t1/MP4" ]] && pass "MP4 folder created" || fail "MP4 folder missing"
[[ -d "$TEST_DIR/t1/AUDIO" ]] && pass "AUDIO folder created" || fail "AUDIO folder missing"

# Verify no media files left in root
media_in_root=$(find "$TEST_DIR/t1" -maxdepth 1 -type f -name "*.jpg" -o -name "*.heic" -o -name "*.arw" -o -name "*.mov" -o -name "*.mp4" -o -name "*.wav" | wc -l | tr -d ' ')
[[ "$media_in_root" == "0" ]] && pass "no media files left in root" || fail "$media_in_root media files still in root"
echo ""

# ---------- Test 2: Files organized into date subfolders ----------
echo "--- Test 2: Date subfolders created ---"
# After organize, files should be in TypeFolder/DateFolder/
jpeg_files=$(find "$TEST_DIR/t1/JPEG" -type f -name "*.jpg")
[[ -n "$jpeg_files" ]] && pass "jpg file inside JPEG tree" || fail "no jpg found in JPEG"

# Check that files are at depth 2 (TypeFolder/DateFolder/file)
for f in $jpeg_files; do
  rel="${f#$TEST_DIR/t1/}"
  depth=$(echo "$rel" | tr '/' '\n' | wc -l | tr -d ' ')
  [[ "$depth" == "3" ]] && pass "jpg at correct depth (type/date/file)" || fail "jpg at wrong depth: $rel"
done
echo ""

# ---------- Test 3: Sidecar files travel with parent ----------
echo "--- Test 3: Sidecars travel with parent ---"
mkdir -p "$TEST_DIR/t3"
touch "$TEST_DIR/t3/DSC_001.arw"
touch "$TEST_DIR/t3/DSC_001.dop"
touch "$TEST_DIR/t3/DSC_001.xmp"

bash "$ORGANIZE_SH" "$TEST_DIR/t3" >/dev/null 2>&1

# The .dop should be in the same directory as the .arw
arw_path=$(find "$TEST_DIR/t3" -name "DSC_001.arw" -type f)
if [[ -n "$arw_path" ]]; then
  arw_dir=$(dirname "$arw_path")
  [[ -f "$arw_dir/DSC_001.dop" ]] && pass "dop sidecar next to arw" || fail "dop sidecar not next to arw"
else
  fail "arw file not found after organize"
fi
echo ""

# ---------- Test 4: Unknown extensions left in place ----------
echo "--- Test 4: Unknown extensions left in root ---"
mkdir -p "$TEST_DIR/t4"
touch "$TEST_DIR/t4/readme.txt"
touch "$TEST_DIR/t4/photo.jpg"

bash "$ORGANIZE_SH" "$TEST_DIR/t4" >/dev/null 2>&1

[[ -f "$TEST_DIR/t4/readme.txt" ]] && pass "unknown ext left in root" || fail "unknown ext was moved"
echo ""

# ---------- Test 5: Empty directory ----------
echo "--- Test 5: Empty directory ---"
mkdir -p "$TEST_DIR/t5"

if bash "$ORGANIZE_SH" "$TEST_DIR/t5" >/dev/null 2>&1; then
  pass "empty directory handled without error"
else
  fail "empty directory caused error"
fi
echo ""

# ---------- Test 6: No files lost ----------
echo "--- Test 6: No files lost ---"
mkdir -p "$TEST_DIR/t6"
for i in $(seq 1 20); do
  touch "$TEST_DIR/t6/img_$(printf '%03d' $i).jpg"
done

before=$(find "$TEST_DIR/t6" -type f | wc -l | tr -d ' ')
bash "$ORGANIZE_SH" "$TEST_DIR/t6" >/dev/null 2>&1
after=$(find "$TEST_DIR/t6" -type f | wc -l | tr -d ' ')

[[ "$before" == "$after" ]] && pass "file count preserved ($before files)" || fail "file count changed: $before -> $after"
echo ""

# ---------- Test 7: -mtime flag accepted ----------
echo "--- Test 7: -mtime flag ---"
mkdir -p "$TEST_DIR/t7"
touch "$TEST_DIR/t7/test.jpg"

if bash "$ORGANIZE_SH" -mtime "$TEST_DIR/t7" >/dev/null 2>&1; then
  pass "-mtime flag accepted"
else
  fail "-mtime flag caused error"
fi
echo ""

# ---------- Test 8: Invalid arguments ----------
echo "--- Test 8: Invalid arguments ---"
if bash "$ORGANIZE_SH" >/dev/null 2>&1; then
  fail "should exit non-zero with no args"
else
  pass "exits non-zero with no args"
fi

if bash "$ORGANIZE_SH" "/tmp/nonexistent_$$" >/dev/null 2>&1; then
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
