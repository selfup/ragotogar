#!/usr/bin/env bash
set -euo pipefail

# Load the vision and indexing/search models into LM Studio.
#
# - Qwen3-VL 8B: MLX vision model (no parallel — MLX serializes requests)
# - Ministral 3B: GGUF with continuous batching for LightRAG workers
#
# See STRATEGIES.md for context-length and parallel sizing rationale.

lms load qwen/qwen3-vl-8b --context-length 64000
lms load mistralai/ministral-3-3b --context-length 64000 --parallel 8
