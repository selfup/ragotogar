#!/usr/bin/env bash
set -euo pipefail

# Full pipeline against a photo directory:
#   1. Describe every image and write into tools/.sql_index/library.db
#   2. Build the LightRAG knowledge graph from the SQL library
#   3. Dedupe LightRAG's audit log
#   4. Start the web server
#
# Example:
#   PHOTO_DIR=/Volumes/T9/X100VI/JPEG/April21st2024 ./full_run.sh

LM_MODEL=mistralai/ministral-3-3b ./scripts/photo_describe.sh --preview-workers 8 --inference-workers 2 "$PHOTO_DIR"

# --reindex rebuilds the LightRAG graph from scratch
INDEX_MODEL=mistralai/ministral-3-3b  ./tools/index_and_vectorize.sh

# Dedupe LightRAG's kv_store_doc_status.json
./tools/prune_dup_status.sh --apply

./scripts/web.sh
