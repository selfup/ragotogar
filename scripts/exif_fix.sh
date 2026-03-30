#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/.files.env"

if ! command -v exiftool &>/dev/null; then
  echo "Error: exiftool is not installed"
  echo "  brew install exiftool"
  exit 1
fi

usage() {
  echo "Usage: $0 <directory>"
  echo ""
  echo "Updates file created and modified dates from EXIF DateTimeOriginal."
  echo "Recursively processes image files (jpg, jpeg, hif, heif, heic, raf, arw,"
  echo "nef, cr2, cr3, dng, orf, rw2, pef) in the given directory."
  echo ""
  echo "Example:"
  echo "  $0 /Volumes/CameraCards/organized"
}

if [[ $# -ne 1 ]]; then
  usage
  exit 1
fi

TARGET_DIR="$1"

if [[ ! -d "$TARGET_DIR" ]]; then
  echo "Error: '$TARGET_DIR' is not a directory"
  exit 1
fi

EXT_ARGS=()
for ext in "${PHOTO_EXTS[@]}"; do
  EXT_ARGS+=(-ext "$ext")
done

echo "=== Fixing file dates from EXIF data ==="
echo "Directory: $TARGET_DIR"
echo ""

exiftool -r \
  "${EXT_ARGS[@]}" \
  '-FileModifyDate<DateTimeOriginal' \
  '-FileCreateDate<DateTimeOriginal' \
  -overwrite_original \
  -progress \
  "$TARGET_DIR"

echo ""
echo "Done!"
