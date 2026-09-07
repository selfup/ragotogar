#!/usr/bin/env bash
# Start the llama.cpp embedding server: ./scripts/embedding_server.sh
set -euo pipefail

exec llama-server \
  -m "$HOME/.lmstudio/models/Qwen/Qwen3-Embedding-4B-GGUF/Qwen3-Embedding-4B-Q4_K_M.gguf" \
  --embedding \
  --parallel 10 \
  --ctx-size 10240 \
  --batch-size 16384 \
  --ubatch-size 16384 \
  -ngl 99 \
  --host 127.0.0.1 \
  --port 1234
