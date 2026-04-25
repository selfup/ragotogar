#!/usr/bin/env bash
set -euo pipefail

# Usage: dir_photos.sh <photo_dir> [output_dir] [photo_describe flags...]
#
# Runs the full pipeline on a directory of photos:
#   1. Describe all photos (EXIF + LLM) → JSON files in output_dir
#   2. Convert all JSON → markdown + HTML via cashier
#
# Examples:
#   ./scripts/dir_photos.sh ~/X100VI/JPEG/April2026
#   ./scripts/dir_photos.sh ~/X100VI/JPEG/April2026 describe_april
#   ./scripts/dir_photos.sh ~/X100VI/JPEG/April2026 describe_april -model mistralai/ministral-3b

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

PHOTO_DIR="${1:?Usage: dir_photos.sh <photo_dir> [output_dir] [photo_describe flags...]}"
shift

OUT_DIR="${1:-describe_output}"
[[ "$OUT_DIR" == -* ]] && OUT_DIR="describe_output" || shift

mkdir -p "$OUT_DIR"

echo "==> Describing photos in $PHOTO_DIR → $OUT_DIR"
"$SCRIPT_DIR/photo_describe.sh" -output "$OUT_DIR" "$@" "$PHOTO_DIR"

echo ""
echo "==> Converting JSON → md + html in $OUT_DIR"
go run "$SCRIPT_DIR/../cmd/cashier" all "$OUT_DIR"
