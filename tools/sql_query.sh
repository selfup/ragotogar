#!/usr/bin/env bash
# Run an ad-hoc SQL query against the metadata index.
#
# Usage:
#   ./tools/sql_query.sh "SELECT camera_model, COUNT(*) FROM exif GROUP BY camera_model"
#   ./tools/sql_query.sh -f /path/to/query.sql
#   ./tools/sql_query.sh --db /other/path.db "SELECT ..."
#
# Defaults: headers on, column-aligned output, db at tools/.sql_index/library.db.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DB="$SCRIPT_DIR/.sql_index/library.db"
SQL_FILE=""
SQL=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --db)
      DB="$2"; shift 2;;
    -f|--file)
      SQL_FILE="$2"; shift 2;;
    -h|--help)
      sed -n 's/^# \{0,1\}//p' "$0" | head -n 9
      exit 0;;
    *)
      SQL="$1"; shift;;
  esac
done

if [[ ! -f "$DB" ]]; then
  echo "No database at $DB. Run ./tools/sql_sync.sh <dir> first." >&2
  exit 1
fi

if [[ -n "$SQL_FILE" ]]; then
  exec sqlite3 -header -column "$DB" ".read $SQL_FILE"
elif [[ -n "$SQL" ]]; then
  exec sqlite3 -header -column "$DB" "$SQL"
else
  echo "Usage: ./tools/sql_query.sh \"<SQL>\" | -f <file>" >&2
  exit 1
fi
