#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/.files.env"

TRANSFERS=2
EXCLUDE_VIDEOS=false

usage() {
  echo "Usage: $0 [-t transfers] [--no-videos] <source_directory> <nas_volume>"
  echo ""
  echo "Options:"
  echo "  -t NUM        Number of parallel transfers (default: 2)"
  echo "  --no-videos   Exclude video directories (MOV, MP4, BRAW, NEV, NDF)"
  echo ""
  echo "Example:"
  echo "  $0 -t 4 --no-videos /Volumes/CameraCards /Volumes/NAS/Media"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -t)
      TRANSFERS="$2"
      shift 2
      ;;
    --no-videos)
      EXCLUDE_VIDEOS=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    -*)
      echo "Error: Unknown option '$1'"
      usage
      exit 1
      ;;
    *)
      break
      ;;
  esac
done

if [[ $# -ne 2 ]]; then
  usage
  exit 1
fi

SOURCE_DIR="$1"
NAS_DEST="$2"

EXCLUDE_ARGS=(--exclude='._*' --exclude='.DS_Store')

if [[ "$EXCLUDE_VIDEOS" == true ]]; then
  for vdir in "${VIDEO_DIRS[@]}"; do
    EXCLUDE_ARGS+=(--exclude="$vdir/**")
  done
fi

if [[ ! -d "$SOURCE_DIR" ]]; then
  echo "Error: '$SOURCE_DIR' is not a directory"
  exit 1
fi

if [[ ! -d "$NAS_DEST" ]]; then
  echo "Error: '$NAS_DEST' is not mounted or does not exist"
  exit 1
fi

echo "=== Syncing cameras to NAS (rclone) ==="
echo "Source: $SOURCE_DIR"
echo "Destination: $NAS_DEST"
echo "Transfers: $TRANSFERS"
[[ "$EXCLUDE_VIDEOS" == true ]] && echo "Excluding video directories: ${VIDEO_DIRS[*]}"
echo ""

# Sync top-level files only (no subdirectories)
rclone copy "$SOURCE_DIR/" "$NAS_DEST/" \
  "${EXCLUDE_ARGS[@]}" \
  --exclude='*/**' \
  --update \
  --transfers="$TRANSFERS" \
  --progress \
  -v

for camera_dir in "$SOURCE_DIR"/*/; do
  [[ -d "$camera_dir" ]] || continue

  camera_name=$(basename "$camera_dir")

  if [[ "$EXCLUDE_VIDEOS" == true ]]; then
    skip=false
    for vdir in "${VIDEO_DIRS[@]}"; do
      if [[ "$camera_name" == "$vdir" ]]; then
        skip=true
        break
      fi
    done
    [[ "$skip" == true ]] && continue
  fi

  echo "--- $camera_name ---"

  rclone copy "$camera_dir" "$NAS_DEST/$camera_name/" \
    --exclude='._*' \
    --exclude='.DS_Store' \
    --update \
    --transfers="$TRANSFERS" \
    --progress \
    -v

  echo ""
done

echo "Done!"
