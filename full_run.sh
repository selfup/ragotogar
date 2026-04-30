#!/usr/bin/env bash
set -euo pipefail

# Full pipeline against one or more photo directories:
#   1. Describe every image in each dir → Postgres library
#   2. Classify the description prose into typed enum fields
#   3. Embed each photo's description (incl. typed fields) into chunks (pgvector)
#   4. Start the web server
#
# Usage:
#   ./full_run.sh /path/to/photos
#   ./full_run.sh /path/to/photos1 /path/to/photos2 /path/to/photos3
#   ./full_run.sh /Volumes/T9/X100VI/JPEG/March          # prefix — expands to March*
#   ./full_run.sh /Volumes/T9/X100VI/JPEG/*              # shell glob — every subdir
#   ./full_run.sh --rebuild /path1 /path2                # rebuild flag is positional-agnostic
#   PHOTO_DIR=/path/to/photos ./full_run.sh              # legacy single-dir env still works
#
# Each positional argument is one of:
#   - A directory  → used as-is. cmd/describe recursively walks up to 3 levels
#     deep (see cmd/describe/main.go collectFiles), so passing a parent dir like
#     .../JPEG processes every date-subfolder's photos in ONE describe run.
#   - A parent/prefix path (e.g. .../JPEG/March) → expands to every sibling
#     under .../JPEG whose name starts with "March", and runs ONE describe
#     pass per matched subdirectory (mirrors scripts/batch_photo_describe.sh).
#     Per-subdir runs give separable logs and per-folder failure recovery; the
#     parent-dir form is one giant log but identical end-state in the DB.
#
# Key distinction:
#   .../JPEG          → real dir (used as-is) → one describe run, all months
#   .../JPEG/March    → not a dir → prefix expansion → one run per March*
#   .../JPEG/*        → shell-expanded by bash → one run per subdir of JPEG
#                       (use this when you want per-subdir logs but every month)
#
# If a path is both a real directory AND you wanted prefix-style expansion,
# the real-directory branch wins. Use a trailing wildcard or list explicit
# paths to force the per-subdir form.
#
# --rebuild re-describes photos already in the DB (-force on cmd/describe),
# re-classifies (-reclassify on cmd/classify), AND truncates+rebuilds the
# chunks table (-reindex on cmd/index). Use it after switching the vision
# model OR the classifier prompt.
#
# Override the vision model: LM_MODEL=qwen/qwen3-vl-8b ./full_run.sh /path
# Override the classifier:   CLASSIFY_MODEL=mistralai/devstral-small-2-2512 ./full_run.sh /path
#
# Prereq: ./scripts/bootstrap.sh (one-time, sets up Postgres + pgvector)

REBUILD=0
ARGS=()
for arg in "$@"; do
    case "$arg" in
        --rebuild) REBUILD=1 ;;
        --help|-h)
            sed -n 's/^# \{0,1\}//p' "$0" | sed -n '/^Usage:/,/^Prereq:/p'
            exit 0
            ;;
        *) ARGS+=("$arg") ;;
    esac
done

# Backward-compat: if no positional dirs and PHOTO_DIR env is set, use it.
if [[ ${#ARGS[@]} -eq 0 && -n "${PHOTO_DIR:-}" ]]; then
    ARGS=("$PHOTO_DIR")
fi

if [[ ${#ARGS[@]} -eq 0 ]]; then
    echo "usage: $0 [--rebuild] <dir|parent/prefix> [...]" >&2
    echo "       (or set PHOTO_DIR env for the legacy single-dir form)" >&2
    exit 1
fi

# Expand each entry: directories pass through; parent/prefix entries expand
# to all matching siblings — same shape as batch_photo_describe.sh.
DIRS=()
for entry in "${ARGS[@]}"; do
    if [[ -d "$entry" ]]; then
        DIRS+=("$entry")
        continue
    fi
    parent="$(dirname "$entry")"
    prefix="$(basename "$entry")"
    if [[ ! -d "$parent" ]]; then
        echo "error: not a directory and parent does not exist: $entry" >&2
        exit 1
    fi
    matches=()
    while IFS= read -r -d '' d; do
        matches+=("$d")
    done < <(find "$parent" -mindepth 1 -maxdepth 1 -type d -name "${prefix}*" -print0 | sort -z)
    if [[ ${#matches[@]} -eq 0 ]]; then
        echo "error: no directories matching '${prefix}*' in $parent" >&2
        exit 1
    fi
    DIRS+=("${matches[@]}")
done

echo "full_run: ${#DIRS[@]} director$([[ ${#DIRS[@]} -eq 1 ]] && echo y || echo ies) to process"
for d in "${DIRS[@]}"; do echo "  - $d"; done
[[ $REBUILD -eq 1 ]] && echo "  mode: --rebuild (force describe + reclassify + reindex)"
echo

describe_force=""
classify_reclassify=""
index_reindex=""
if [[ $REBUILD -eq 1 ]]; then
    describe_force="-force"
    classify_reclassify="-reclassify"
    index_reindex="-reindex"
fi

brew services start postgresql@18

# Describe runs per-directory (cmd/describe takes one input dir at a time).
# Pipeline mode: -classify on cmd/describe runs the small text classifier
# inline after each photo's vision describe + DB write. Same goroutine, so
# vision and text inference are sequential within a worker — no LM Studio
# model contention. Classify failures are logged but don't fail the photo;
# the standalone classify pass below catches anything that slipped through.
# Honor an outer LM_MODEL if the caller set one, otherwise default to gemma.
# The previous form (LM_MODEL=foo cmd ...) silently shadowed any outer value
# the user exported on the same line — e.g. `LM_MODEL=ministral ./full_run.sh`
# would still see gemma. ${LM_MODEL:-gemma-4-31b-it} fixes that.
for d in "${DIRS[@]}"; do
    echo "=== describe + classify: $d ==="
    # shellcheck disable=SC2086 # word-split intentional — flags don't contain spaces
    LM_MODEL="${LM_MODEL:-gemma-4-31b-it}" CLASSIFY_MODEL="${CLASSIFY_MODEL:-nvidia/nemotron-3-nano-4b}" ./scripts/photo_describe.sh $describe_force -classify --preview-workers 8 --inference-workers 4 "$d"
done

# Safety-net catch-up: run cmd/classify standalone for any photo whose inline
# classify failed (logged in describe output) or for the --rebuild path which
# needs a clean TRUNCATE. Cheap when nothing's to do (single SELECT).
echo "=== classify catch-up ==="
# shellcheck disable=SC2086
CLASSIFY_MODEL="${CLASSIFY_MODEL:-nvidia/nemotron-3-nano-4b}" ./scripts/classify.sh $classify_reclassify

echo "=== index ==="
# shellcheck disable=SC2086
./scripts/index.sh $index_reindex

echo "=== web ==="
./scripts/web.sh
