#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
EXIF_FIX_SH="$SCRIPT_DIR/exif_fix.sh"
FAIL=0

pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1"; FAIL=1; }

# ---------- Test 1: Invalid arguments ----------
echo "--- Test 1: Invalid arguments ---"
if bash "$EXIF_FIX_SH" >/dev/null 2>&1; then
  fail "should exit non-zero with no args"
else
  pass "exits non-zero with no args"
fi

if bash "$EXIF_FIX_SH" "/tmp/nonexistent_$$" >/dev/null 2>&1; then
  fail "should exit non-zero for nonexistent dir"
else
  pass "exits non-zero for nonexistent dir"
fi
echo ""

# ---------- Tests requiring exiftool ----------
if ! command -v exiftool &>/dev/null; then
  echo "--- Skipping exiftool tests (not installed) ---"
  echo ""
  if [[ "$FAIL" -eq 0 ]]; then
    echo "All available tests passed. (exiftool not installed, some tests skipped)"
  else
    echo "Some tests FAILED."
    exit 1
  fi
  exit 0
fi

TEST_DIR="/tmp/exif_fix_test_$$"
cleanup() { rm -rf "$TEST_DIR"; }
trap cleanup EXIT

# ---------- Test 2: Processes supported image formats ----------
echo "--- Test 2: Processes supported image formats ---"
mkdir -p "$TEST_DIR/t2"

# Create a minimal test JPEG from scratch using exiftool
# exiftool can create a file from nothing with -overwrite_original
touch "$TEST_DIR/t2/test.jpg"
exiftool -overwrite_original -TagsFromFile @ -DateTimeOriginal="2023:06:15 14:30:00" "$TEST_DIR/t2/test.jpg" >/dev/null 2>&1 || true
# If that didn't produce a usable file, write minimal JPEG bytes via python
if ! exiftool -s3 -DateTimeOriginal "$TEST_DIR/t2/test.jpg" 2>/dev/null | grep -q "2023"; then
  python3 -c "
import struct, sys
# Minimal JPEG: SOI + APP0 + SOF + SOS + EOI
soi = b'\xff\xd8'
eoi = b'\xff\xd9'
app0 = b'\xff\xe0' + struct.pack('>H', 16) + b'JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00'
sof = b'\xff\xc0' + struct.pack('>H', 11) + b'\x08\x00\x01\x00\x01\x01\x01\x11\x00'
dqt = b'\xff\xdb' + struct.pack('>H', 67) + b'\x00' + bytes(range(1,65))
dht = b'\xff\xc4' + struct.pack('>H', 31) + b'\x00\x00\x01\x05\x01\x01\x01\x01\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b'
sos = b'\xff\xda' + struct.pack('>H', 8) + b'\x01\x01\x00\x00\x3f\x00\x7f\x50'
sys.stdout.buffer.write(soi + app0 + dqt + sof + dht + sos + eoi)
" > "$TEST_DIR/t2/test.jpg"
  exiftool -overwrite_original -DateTimeOriginal="2023:06:15 14:30:00" "$TEST_DIR/t2/test.jpg" >/dev/null 2>&1
fi

bash "$EXIF_FIX_SH" "$TEST_DIR/t2" >/dev/null 2>&1

# Check that FileModifyDate was updated to match DateTimeOriginal
mod_date=$(exiftool -s3 -FileModifyDate "$TEST_DIR/t2/test.jpg" | cut -d' ' -f1)
[[ "$mod_date" == "2023:06:15" ]] && pass "FileModifyDate updated to EXIF date" || fail "FileModifyDate not updated (got: $mod_date)"
echo ""

# ---------- Test 3: Non-image files ignored ----------
echo "--- Test 3: Non-image files ignored ---"
mkdir -p "$TEST_DIR/t3"
echo "not an image" > "$TEST_DIR/t3/readme.txt"
touch -t 202001010000 "$TEST_DIR/t3/readme.txt"
mtime_before=$(stat -f %m "$TEST_DIR/t3/readme.txt")

bash "$EXIF_FIX_SH" "$TEST_DIR/t3" >/dev/null 2>&1

mtime_after=$(stat -f %m "$TEST_DIR/t3/readme.txt")
[[ "$mtime_before" == "$mtime_after" ]] && pass "non-image file untouched" || fail "non-image file was modified"
echo ""

# ---------- Test 4: Recursive processing ----------
echo "--- Test 4: Recursive processing ---"
mkdir -p "$TEST_DIR/t4/sub/deep"
cp "$TEST_DIR/t2/test.jpg" "$TEST_DIR/t4/sub/deep/nested.jpg"

# Reset the file date to something different
touch -t 202001010000 "$TEST_DIR/t4/sub/deep/nested.jpg"

bash "$EXIF_FIX_SH" "$TEST_DIR/t4" >/dev/null 2>&1

mod_date=$(exiftool -s3 -FileModifyDate "$TEST_DIR/t4/sub/deep/nested.jpg" | cut -d' ' -f1)
[[ "$mod_date" == "2023:06:15" ]] && pass "nested file date fixed" || fail "nested file date not fixed (got: $mod_date)"
echo ""

# ---------- Summary ----------
if [[ "$FAIL" -eq 0 ]]; then
  echo "All tests passed."
else
  echo "Some tests FAILED."
  exit 1
fi
