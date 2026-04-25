#!/usr/bin/env bash
set -euo pipefail

DIR="${1:-describe_oneshot}"

for JSON in "$DIR"/*.json; do
  STEM=$(basename "$JSON" .json)
  ./scripts/photo.sh "$JSON" "${DIR}/${STEM}.md" "${DIR}/${STEM}.html"
done
