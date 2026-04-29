#!/usr/bin/env bash
set -euo pipefail

# Full pipeline against a photo directory:
#   1. Describe every image and write into the Postgres library
#   2. Embed each photo's description into the chunks table (pgvector)
#   3. Start the web server
#
# Usage:
#   PHOTO_DIR=/Volumes/T9/X100VI/JPEG/April21st2024 ./full_run.sh
#   PHOTO_DIR=...                                   ./full_run.sh --rebuild
#
# --rebuild re-describes photos already in the DB (-force on cmd/describe)
# AND truncates+rebuilds the chunks table (-reindex on cmd/index). Use it
# after switching the vision model so existing rows pick up the new
# describer's output.
#
# Override the vision model: LM_MODEL=qwen/qwen3-vl-8b ./full_run.sh
#
# Prereq: ./scripts/bootstrap.sh (one-time, sets up Postgres + pgvector)

describe_force=""
index_reindex=""
if [[ "${1:-}" == "--rebuild" ]]; then
    describe_force="-force"
    index_reindex="-reindex"
fi

brew services start postgresql@18

# shellcheck disable=SC2086 # word-split intentional — flags don't contain spaces
LM_MODEL=gemma-4-31b-it ./scripts/photo_describe.sh $describe_force --preview-workers 8 --inference-workers 2 "$PHOTO_DIR"

# shellcheck disable=SC2086
./scripts/index.sh $index_reindex

./scripts/web.sh
