#!/usr/bin/env bash
set -euo pipefail

# example ENV: DESCRIBE_DIR=describe_test
# example ENV: PHOTO_DIR=/Volumes/Regis/X100VI/JPEG/April17th2026

# example script: DESCRIBE_DIR=describe_test PHOTO_DIR=/Volumes/T9/X100VI/JPEG/April21st2024 ./full_run.sh

# example run:

./scripts/photo_describe.sh --preview-workers 8 --inference-workers 2 -output $DESCRIBE_DIR $PHOTO_DIR

# -force re-renders everything
go run ./cmd/cashier all -workers 24 $DESCRIBE_DIR 

# --reindex runs through all data and re-populates the graph_rag
./tools/index_and_vectorize.sh $DESCRIBE_DIR

# dedupe all duplicate entries in lightrag
./tools/prune_dup_status.sh --apply

./scripts/web.sh -dir $DESCRIBE_DIR
