#!/usr/bin/env bash
set -euo pipefail

# Wrapper around cmd/describe — extracts EXIF, generates a 1024px preview,
# calls the LM Studio vision model, and writes the photo + EXIF + parsed
# fields + thumbnail BLOB into tools/.sql_index/library.db.
#
# Usage:
#   ./scripts/photo_describe.sh /path/to/photos
#
#   # Override the library DB path
#   ./scripts/photo_describe.sh -db /tmp/other.db /path/to/photos
#
#   # Re-describe photos already in the DB
#   ./scripts/photo_describe.sh -force /path/to/photos
#
#   # Use a specific LM Studio model
#   ./scripts/photo_describe.sh -model mistralai/devstral-small-2-2512:2 /path/to/photos
#
# Flags: -db PATH, -force, -model NAME, -dry-run, -retries N,
#        -preview-workers N, -inference-workers N
# Env:   LM_STUDIO_BASE, LM_MODEL, RESIZE_PX, JPEG_QUALITY

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DESCRIBE_DIR="$SCRIPT_DIR/../cmd/describe"

# Resolve relative -db paths before cd-ing into the Go module so users can
# pass a path relative to the caller's cwd.
args=()
next_is_db=false
for arg in "$@"; do
  if $next_is_db; then
    [[ "$arg" != /* ]] && arg="$PWD/$arg"
    next_is_db=false
  elif [[ "$arg" == "-db" ]]; then
    next_is_db=true
  fi
  args+=("$arg")
done

cd "$DESCRIBE_DIR"
go run . "${args[@]}"
