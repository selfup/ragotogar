#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/.files.env"

# Usage: ./sync_to_nas.sh /path/to/cameras /Volumes/NAS/Media

if [[ $# -ne 2 ]]; then
  echo "Usage: $0 <source_directory> <nas_volume>"
  echo ""
  echo "Example:"
  echo "  $0 /Volumes/CameraCards /Volumes/NAS/Media"
  exit 1
fi

SOURCE_DIR="$1"
NAS_DEST="$2"

if [[ ! -d "$SOURCE_DIR" ]]; then
  echo "Error: '$SOURCE_DIR' is not a directory"
  exit 1
fi

if [[ ! -d "$NAS_DEST" ]]; then
  echo "Error: '$NAS_DEST' is not mounted or does not exist"
  exit 1
fi

EXCLUDE_ARGS=()
for pattern in "${MACOS_EXCLUDES[@]}"; do
  EXCLUDE_ARGS+=(--exclude="$pattern")
done

echo "=== Syncing cameras to NAS ==="
echo "Source: $SOURCE_DIR"
echo "Destination: $NAS_DEST"
echo ""

rsync -avh --update --progress "${EXCLUDE_ARGS[@]}" \
  --exclude='*/' \
  "$SOURCE_DIR/" "$NAS_DEST/"

for camera_dir in "$SOURCE_DIR"/*/; do
  [[ -d "$camera_dir" ]] || continue

  camera_name=$(basename "$camera_dir")
  echo "--- $camera_name ---"

  rsync -avh --update --progress "${EXCLUDE_ARGS[@]}" "$camera_dir" "$NAS_DEST/$camera_name/"

  echo ""
done

echo "Done!"
