### When lms3800x is running on port 8080

EMBED_MODEL="text-embedding-qwen3-embedding-0.6b" EMBED_DIM=1024 ./scripts/web.sh -addr :8090

### When local lms is running

EMBED_MODEL="text-embedding-qwen3-embedding-0.6b" EMBED_DIM=1024 ./scripts/web.sh

### Replica on host

```bash
./scripts/replica.sh -spawn \
    -listen 127.0.0.1:1234 \
    -backends http://127.0.0.1:1235,http://127.0.0.1:1236 \
    -model "$HOME/.lmstudio/models/Qwen/Qwen3-Embedding-0.6B-GGUF/Qwen3-Embedding-0.6B-Q8_0.gguf" \
    -- \
    --embeddings \
    -c 16384 \
    --parallel 4 \
    -b 4096 \
    -ub 4096 \
    --cache-ram 0 \
    -ngl 99 \
    -fa on \
    --pooling last \
    --alias qwen3-embedding-0.6b
```

### Embedding on the host

PORT=8080 \
HOST=127.0.0.1 \
CTX=16384 \
PARALLEL=4 \
BATCH=4096 \
UBATCH=4096 \
EXTRA="--cache-ram 0" \
NGL=99 \
FA=on \
POOLING=last \
ALIAS=qwen3-embedding-0.6b \
./llama-embed.sh \
~/.lmstudio/models/Qwen/Qwen3-Embedding-0.6B-GGUF/Qwen3-Embedding-0.6B-Q8_0.gguf

```bash
#!/usr/bin/env bash
#
# llama-embed.sh — wrap llama-server in embedding mode with sane defaults
# that approximate LM Studio's embedding endpoint behavior.
#
# Usage:
#   ./llama-embed.sh <path-to.gguf>
#
# Env overrides:
#   PORT      (default 1234 — change if running alongside a chat server)
#   HOST      (default 127.0.0.1)
#   CTX       (default 8192  — total; split across PARALLEL slots)
#   PARALLEL  (default 8)
#   BATCH     (default 8192  — logical batch, -b)
#   UBATCH    (default 2048  — physical/micro batch, -ub)
#   NGL       (default 99    — full GPU offload)
#   FA        (default on    — Flash Attention; on|off|auto)
#   POOLING   (default empty — read from GGUF metadata; override: none|mean|cls|last|rank)
#   ALIAS     (default derived from filename without .gguf extension)
#   EXTRA     (any additional llama-server flags, appended verbatim)

set -euo pipefail

# --- arg check ---------------------------------------------------------------
if [[ $# -lt 1 ]]; then
  echo "usage: $0 <path-to.gguf>" >&2
  exit 2
fi

MODEL="$1"
if [[ ! -f "$MODEL" ]]; then
  echo "error: model not found: $MODEL" >&2
  exit 1
fi

# --- llama-server presence ---------------------------------------------------
if ! command -v llama-server >/dev/null 2>&1; then
  echo "error: llama-server not on PATH" >&2
  exit 1
fi

# --- defaults ----------------------------------------------------------------
PORT="${PORT:-1234}"
HOST="${HOST:-127.0.0.1}"
CTX="${CTX:-8192}"
PARALLEL="${PARALLEL:-8}"
BATCH="${BATCH:-8192}"
UBATCH="${UBATCH:-2048}"
NGL="${NGL:-99}"
FA="${FA:-on}"
POOLING="${POOLING:-}"
ALIAS="${ALIAS:-$(basename "$MODEL" .gguf)}"
EXTRA="${EXTRA:-}"

# --- pooling override (optional) ---------------------------------------------
POOLING_ARGS=()
if [[ -n "$POOLING" ]]; then
  POOLING_ARGS=(--pooling "$POOLING")
fi

# --- log what we're about to run --------------------------------------------
PER_SLOT=$(( CTX / PARALLEL ))
cat <<EOF >&2
llama-embed.sh
  model     : $MODEL
  alias     : $ALIAS
  endpoint  : http://$HOST:$PORT  (POST /v1/embeddings)
  context   : $CTX total / $PARALLEL slots = $PER_SLOT per slot
  batch     : -b $BATCH / -ub $UBATCH
  ngl       : $NGL
  fa        : $FA
  pooling   : ${POOLING:-from GGUF metadata}
  extra     : ${EXTRA:-<none>}

EOF

# --- run ---------------------------------------------------------------------
# shellcheck disable=SC2086  # EXTRA is intentionally word-split
exec llama-server \
  -m "$MODEL" \
  --host "$HOST" \
  --port "$PORT" \
  --embeddings \
  -c "$CTX" \
  --parallel "$PARALLEL" \
  -b "$BATCH" \
  -ub "$UBATCH" \
  -ngl "$NGL" \
  -fa "$FA" \
  --alias "$ALIAS" \
  "${POOLING_ARGS[@]}" \
  $EXTRA
  ```
