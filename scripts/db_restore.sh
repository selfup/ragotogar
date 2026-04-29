#!/usr/bin/env bash
# Restore the ragotogar Postgres library from a pg_dump file.
#
# DESTRUCTIVE — drops the existing database before restoring. Confirms
# interactively unless -y is passed. Per data-safety practice, the script:
#   1. validates the dump file is readable
#   2. shows the current row count of the live DB
#   3. asks for explicit "yes" confirmation
#
# Usage:
#   ./scripts/db_restore.sh /path/to/backup_dir          # use latest .dump in dir
#   ./scripts/db_restore.sh /path/to/specific.dump       # restore a specific file
#   ./scripts/db_restore.sh -y /path/to/backup_dir       # skip confirm prompt
#   DB_NAME=other ./scripts/db_restore.sh /path/to/dir
set -euo pipefail

YES=0
ARGS=()
for arg in "$@"; do
    case "$arg" in
        -y|--yes) YES=1 ;;
        -h|--help)
            sed -n 's/^# \{0,1\}//p' "$0" | head -20
            exit 0
            ;;
        *) ARGS+=("$arg") ;;
    esac
done

if [[ ${#ARGS[@]} -ne 1 ]]; then
    echo "usage: $0 [-y] <dump_dir|dump_file>" >&2
    exit 1
fi
TARGET="${ARGS[0]}"
DB_NAME="${DB_NAME:-ragotogar}"

# Pick the dump file: latest in dir, or use the given file directly.
if [[ -d "$TARGET" ]]; then
    DUMP_FILE=$(find "$TARGET" -maxdepth 1 -name '*.dump' -type f | sort | tail -1)
    if [[ -z "$DUMP_FILE" ]]; then
        echo "error: no *.dump files in $TARGET" >&2
        exit 1
    fi
elif [[ -f "$TARGET" ]]; then
    DUMP_FILE="$TARGET"
else
    echo "error: $TARGET is neither a directory nor a regular file" >&2
    exit 1
fi

# Validate the dump's TOC reads cleanly before we touch anything.
if ! pg_restore --list "$DUMP_FILE" >/dev/null 2>&1; then
    echo "error: $DUMP_FILE is not a valid pg_dump file (or is corrupt)" >&2
    exit 1
fi

DUMP_SIZE=$(du -h "$DUMP_FILE" | awk '{print $1}')
echo "==> dump file: $DUMP_FILE  ($DUMP_SIZE)"

# Show what we're about to destroy, if the DB exists.
if psql -lqt 2>/dev/null | cut -d \| -f 1 | grep -qw "$DB_NAME"; then
    EXISTING_PHOTOS=$(psql -d "$DB_NAME" -tAc "SELECT COUNT(*) FROM photos" 2>/dev/null || echo "?")
    EXISTING_CHUNKS=$(psql -d "$DB_NAME" -tAc "SELECT COUNT(*) FROM chunks" 2>/dev/null || echo "?")
    echo "==> CURRENT $DB_NAME: $EXISTING_PHOTOS photos, $EXISTING_CHUNKS chunks → will be DROPPED"
else
    echo "==> $DB_NAME does not exist; will be created from the dump"
fi

if [[ $YES -ne 1 ]]; then
    read -rp "Type 'yes' to proceed: " ANS
    if [[ "$ANS" != "yes" ]]; then
        echo "aborted (confirmation not received)"
        exit 1
    fi
fi

echo "==> dropping $DB_NAME (--force terminates any open connections, e.g. cmd/web)"
dropdb --force --if-exists "$DB_NAME"

echo "==> creating $DB_NAME"
createdb "$DB_NAME"

# pg_dump -Fc includes the CREATE EXTENSION statement for pgvector; no need
# to run it separately. --no-owner / --no-acl avoid cross-user permission
# issues when restoring on a different machine.
echo "==> restoring from $DUMP_FILE"
pg_restore --no-owner --no-acl -d "$DB_NAME" "$DUMP_FILE"

echo "==> verifying"
RESTORED_PHOTOS=$(psql -d "$DB_NAME" -tAc "SELECT COUNT(*) FROM photos" 2>/dev/null || echo "?")
RESTORED_CHUNKS=$(psql -d "$DB_NAME" -tAc "SELECT COUNT(*) FROM chunks" 2>/dev/null || echo "?")
RESTORED_VERSION=$(psql -d "$DB_NAME" -tAc "SELECT MAX(version) FROM schema_version" 2>/dev/null || echo "?")
echo "    photos:  $RESTORED_PHOTOS"
echo "    chunks:  $RESTORED_CHUNKS"
echo "    schema:  v$RESTORED_VERSION"
