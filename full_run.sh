#!/usr/bin/env bash
set -euo pipefail

# Full pipeline against a photo directory:
#   1. Describe every image and write into the Postgres library
#   2. Classify the description prose into typed enum fields (POV, scene, etc.)
#   3. Embed each photo's description (incl. typed fields) into chunks (pgvector)
#   4. Start the web server
#
# Usage:
#   PHOTO_DIR=/Volumes/T9/X100VI/JPEG/April21st2024 ./full_run.sh
#   PHOTO_DIR=...                                   ./full_run.sh --rebuild
#
# --rebuild re-describes photos already in the DB (-force on cmd/describe),
# re-classifies (-reclassify on cmd/classify), AND truncates+rebuilds the
# chunks table (-reindex on cmd/index). Use it after switching the vision
# model OR the classifier prompt so existing rows pick up the new output.
#
# Override the vision model: LM_MODEL=qwen/qwen3-vl-8b ./full_run.sh
# Override the classifier: CLASSIFY_MODEL=mistralai/devstral-small-2-2512 ./full_run.sh
#
# Prereq: ./scripts/bootstrap.sh (one-time, sets up Postgres + pgvector)

describe_force=""
classify_reclassify=""
index_reindex=""
if [[ "${1:-}" == "--rebuild" ]]; then
    describe_force="-force"
    classify_reclassify="-reclassify"
    index_reindex="-reindex"
fi

brew services start postgresql@18

# shellcheck disable=SC2086 # word-split intentional — flags don't contain spaces
LM_MODEL=gemma-4-31b-it ./scripts/photo_describe.sh $describe_force --preview-workers 8 --inference-workers 2 "$PHOTO_DIR"

# shellcheck disable=SC2086
./scripts/classify.sh $classify_reclassify

# shellcheck disable=SC2086
./scripts/index.sh $index_reindex

./scripts/web.sh
