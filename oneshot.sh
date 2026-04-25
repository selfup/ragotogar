#!/usr/bin/env bash
set -euo pipefail

PHOTO="${1:?Usage: oneshot.sh <photo_path> [photo_describe flags...]}"
shift

OUT_DIR=describe_oneshot

mkdir -p "$OUT_DIR"

./scripts/photo_describe.sh -output "$OUT_DIR" "$@" "$PHOTO"

JSON=$(find "$OUT_DIR" -maxdepth 1 -name "*.json" | head -1)
[[ -z "$JSON" ]] && { echo "No JSON produced in $OUT_DIR" >&2; exit 1; }

STEM=$(basename "$JSON" .json)
./scripts/photo.sh "$JSON" "${OUT_DIR}/${STEM}.md" "${OUT_DIR}/${STEM}.html"
