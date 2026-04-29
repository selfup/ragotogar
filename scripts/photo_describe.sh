#!/usr/bin/env bash
set -euo pipefail

# Wrapper around cmd/describe — extracts EXIF, generates a 1024px preview,
# calls the LM Studio vision model, and writes photo + EXIF + parsed fields
# + thumbnail BLOB into the Postgres library (default DSN
# postgres:///ragotogar; override with -dsn or LIBRARY_DSN).
#
# Usage:
#   ./scripts/photo_describe.sh /path/to/photos
#
#   # Override the library DSN
#   ./scripts/photo_describe.sh -dsn postgres://localhost/other /path/to/photos
#
#   # Re-describe photos already in the DB
#   ./scripts/photo_describe.sh -force /path/to/photos
#
#   # Use a specific LM Studio model
#   ./scripts/photo_describe.sh -model mistralai/devstral-small-2-2512 /path/to/photos
#
# Flags: -dsn DSN, -force, -model NAME, -dry-run, -retries N,
#        -preview-workers N, -inference-workers N
# Env:   LIBRARY_DSN, VISION_ENDPOINT (or legacy LM_STUDIO_BASE), LM_MODEL,
#        CLASSIFY_MODEL (when -classify), TEXT_ENDPOINT (when -classify),
#        RESIZE_PX, JPEG_QUALITY

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DESCRIBE_DIR="$SCRIPT_DIR/../cmd/describe"

cd "$DESCRIBE_DIR"
go run . "$@"
