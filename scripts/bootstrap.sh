#!/usr/bin/env bash
# Bootstrap a fresh ragotogar checkout: install Postgres + pgvector via
# Homebrew, start the cluster, create the library database, and load the
# vector extension. Idempotent — safe to re-run.
#
# Usage:
#   ./scripts/bootstrap.sh
#   LIBRARY_DB_NAME=foo ./scripts/bootstrap.sh    # use a different DB name
#
# Prereqs: macOS + Homebrew. Docker is the documented fallback if brew
# isn't available; see ARCHITECTURE.md.
set -euo pipefail

DB_NAME="${LIBRARY_DB_NAME:-ragotogar}"
PG_FORMULA="postgresql@18"

if ! command -v brew >/dev/null 2>&1; then
    echo "Homebrew required (https://brew.sh) — see ARCHITECTURE.md for the Docker fallback" >&2
    exit 1
fi

echo "==> Postgres formula"
if ! brew list "$PG_FORMULA" >/dev/null 2>&1; then
    brew install "$PG_FORMULA"
else
    echo "  $PG_FORMULA $(brew list --versions "$PG_FORMULA" | awk '{print $2}') already installed"
fi

echo "==> pgvector extension"
if ! brew list pgvector >/dev/null 2>&1; then
    brew install pgvector
else
    echo "  pgvector $(brew list --versions pgvector | awk '{print $2}') already installed"
fi

echo "==> brew services"
if brew services list | grep -E "^${PG_FORMULA}[[:space:]]+started" >/dev/null; then
    echo "  $PG_FORMULA already running"
else
    brew services start "$PG_FORMULA"
    # Wait for the cluster to accept connections (max ~15s)
    for _ in {1..30}; do
        if psql -d postgres -c 'SELECT 1' >/dev/null 2>&1; then break; fi
        sleep 0.5
    done
fi

echo "==> library database '$DB_NAME'"
if psql -lqt | cut -d \| -f 1 | grep -qw "$DB_NAME"; then
    echo "  database already exists"
else
    createdb "$DB_NAME"
    echo "  created"
fi

echo "==> vector extension"
psql -d "$DB_NAME" -c 'CREATE EXTENSION IF NOT EXISTS vector' >/dev/null
EXT_VER=$(psql -d "$DB_NAME" -tAc "SELECT extversion FROM pg_extension WHERE extname='vector'")
echo "  pgvector $EXT_VER loaded into $DB_NAME"

echo "==> library schema (cmd/describe -init-only)"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LIBRARY_DSN="postgres:///$DB_NAME" \
    bash -c "cd '$SCRIPT_DIR/../cmd/describe' && go run . -init-only"

echo
echo "Library DSN: postgres:///$DB_NAME"
echo "Next:        ./scripts/dir_photos.sh /path/to/photos"
