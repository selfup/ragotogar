#!/usr/bin/env bash
set -euo pipefail

# Full pipeline against a photo directory:
#   1. Describe every image and write into the Postgres library
#   2. Embed each photo's description into the chunks table (pgvector)
#   3. Start the web server
#
# Example:
#   PHOTO_DIR=/Volumes/T9/X100VI/JPEG/April21st2024 ./full_run.sh
#
# Prereq: ./scripts/bootstrap.sh (one-time, sets up Postgres + pgvector)

brew services start postgresql@18

LM_MODEL=mistralai/ministral-3-3b ./scripts/photo_describe.sh --preview-workers 8 --inference-workers 2 "$PHOTO_DIR"

# Embed descriptions → chunks (pgvector). Add -reindex to rebuild from scratch.
./scripts/index.sh

./scripts/web.sh
