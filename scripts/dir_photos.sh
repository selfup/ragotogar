#!/usr/bin/env bash
set -euo pipefail

# Usage: dir_photos.sh <photo_dir> [photo_describe flags...]
#
# Describes every supported image in <photo_dir> and writes everything
# (photo metadata, EXIF, parsed fields, full description, thumbnail BLOB)
# directly into tools/.sql_index/library.db via cmd/describe.
#
# After this finishes, run ./tools/index_and_vectorize.sh to build the
# LightRAG index and ./scripts/web.sh to browse the library.
#
# Examples:
#   ./scripts/dir_photos.sh ~/X100VI/JPEG/April2026
#   ./scripts/dir_photos.sh ~/X100VI/JPEG/April2026 -model mistralai/devstral-small-2-2512
#   ./scripts/dir_photos.sh ~/X100VI/JPEG/April2026 -force

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

PHOTO_DIR="${1:?Usage: dir_photos.sh <photo_dir> [photo_describe flags...]}"
shift

echo "==> Describing photos in $PHOTO_DIR"
"$SCRIPT_DIR/photo_describe.sh" "$@" "$PHOTO_DIR"
