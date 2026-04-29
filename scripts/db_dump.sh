#!/usr/bin/env bash
# Dump the ragotogar Postgres library (schema + data + thumbnails BYTEA +
# pgvector chunks) to a timestamped pg_dump custom-format file. Idempotent —
# every run produces a new file, no overwrite.
#
# Usage:
#   ./scripts/db_dump.sh /path/to/backup_dir
#   DB_NAME=other ./scripts/db_dump.sh /path/to/backup_dir
#
# Output filename: <db>_YYYYMMDD_HHMMSS.dump
#
# Restore with: ./scripts/db_restore.sh /path/to/backup_dir
set -euo pipefail

DB_NAME="${DB_NAME:-ragotogar}"
DUMP_DIR="${1:?usage: $0 <dump_dir>}"

mkdir -p "$DUMP_DIR"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
OUT="$DUMP_DIR/${DB_NAME}_${TIMESTAMP}.dump"

echo "==> dumping $DB_NAME → $OUT"
# -Fc: custom format (compressed, restorable selectively, supports parallel
#      restore via pg_restore -j)
pg_dump -Fc -d "$DB_NAME" -f "$OUT"

SIZE=$(du -h "$OUT" | awk '{print $1}')
TABLES=$(pg_restore --list "$OUT" 2>/dev/null | awk '$2=="TABLE" && $3=="DATA" {n++} END {print n+0}')

echo "==> done"
echo "    file:   $OUT"
echo "    size:   $SIZE"
echo "    tables: $TABLES"
