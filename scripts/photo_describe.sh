#!/usr/bin/env bash
set -euo pipefail

# Wrapper around cmd/describe — extracts EXIF + generates LLM descriptions
# for photos in a directory via LM Studio.
#
# Usage:
#   ./scripts/photo_describe.sh /path/to/photos
#
#   # Custom output directory (relative paths resolved before cd-ing into Go module)
#   ./scripts/photo_describe.sh -output describe_test /Volumes/T9/X100VI/JPEG/March21st2026
#
#   # Preview what would be processed
#   ./scripts/photo_describe.sh -dry-run /path/to/photos
#
#   # Use a specific LM Studio model (e.g. a second loaded instance)
#   ./scripts/photo_describe.sh -model mistralai/devstral-small-2-2512:2 /path/to/photos
#
#   # Bump retry attempts for flaky models
#   ./scripts/photo_describe.sh -retries 5 /path/to/photos
#
# Flags: -output DIR, -model NAME, -dry-run, -retries N
# Env:   LM_STUDIO_BASE, LM_MODEL, RESIZE_PX, JPEG_QUALITY

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DESCRIBE_DIR="$SCRIPT_DIR/../cmd/describe"

# Resolve relative paths before cd-ing into the Go module directory.
args=()
next_is_output=false
for arg in "$@"; do
  if $next_is_output; then
    # Make -output path absolute relative to the caller's working directory
    [[ "$arg" != /* ]] && arg="$PWD/$arg"
    next_is_output=false
  elif [[ "$arg" == "-output" ]]; then
    next_is_output=true
  fi
  args+=("$arg")
done

cd "$DESCRIBE_DIR"
go run . "${args[@]}"
