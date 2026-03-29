#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/.files.env"

TRANSFERS=2
EXCLUDE_VIDEOS=false
MONTH_FILTER=""
YEAR_FILTER=""

MONTHS_ORDERED=(January February March April May June July August September October November December)

normalize_month() {
  local input
  input="$(echo "$1" | tr '[:upper:]' '[:lower:]')"
  for m in "${MONTHS_ORDERED[@]}"; do
    local lower
    lower="$(echo "$m" | tr '[:upper:]' '[:lower:]')"
    if [[ "$lower" == "$input" || "$lower" == "$input"* ]]; then
      echo "$m"
      return
    fi
  done
  echo ""
}

# Build the list of month names to include.
# Single month: from that month through December.
# Comma-delimited: only those specific months.
build_month_list() {
  local raw="$1"
  if [[ "$raw" == *,* ]]; then
    IFS=',' read -ra parts <<< "$raw"
    for part in "${parts[@]}"; do
      part="$(echo "$part" | xargs)"
      local normalized
      normalized=$(normalize_month "$part")
      if [[ -z "$normalized" ]]; then
        echo "Error: unrecognized month '$part'" >&2
        exit 1
      fi
      echo "$normalized"
    done
  else
    local start_month
    start_month=$(normalize_month "$raw")
    if [[ -z "$start_month" ]]; then
      echo "Error: unrecognized month '$raw'" >&2
      exit 1
    fi
    local found=false
    for m in "${MONTHS_ORDERED[@]}"; do
      [[ "$m" == "$start_month" ]] && found=true
      [[ "$found" == true ]] && echo "$m"
    done
  fi
}

usage() {
  echo "Usage: $0 [-t transfers] [--no-videos] [-m month] [-y year] <source_directory> <nas_volume>"
  echo ""
  echo "Options:"
  echo "  -t NUM        Number of parallel transfers (default: 2)"
  echo "  --no-videos   Exclude video directories (MOV, MP4, BRAW, NEV, NDF)"
  echo "  -m MONTH      Month filter (e.g. Jan, March, \"Feb,Apr,Jun\")"
  echo "                  Single month: from that month through December"
  echo "                  Comma-delimited: only those specific months"
  echo "  -y YEAR       Year filter (e.g. 2025)"
  echo "                  Combined with -m: only matching months in that year"
  echo "                  Without -m: all months in that year"
  echo ""
  echo "Examples:"
  echo "  $0 -t 4 --no-videos /Volumes/CameraCards /Volumes/NAS/Media"
  echo "  $0 -m Mar /Volumes/Organized /Volumes/NAS/Media          # March onward, all years"
  echo "  $0 -m \"Feb,Apr\" -y 2025 /Volumes/Organized /Volumes/NAS  # Feb+Apr 2025 only"
  echo "  $0 -y 2024 /Volumes/Organized /Volumes/NAS/Media          # all of 2024"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -t)
      TRANSFERS="$2"
      shift 2
      ;;
    --no-videos)
      EXCLUDE_VIDEOS=true
      shift
      ;;
    -m|--month)
      MONTH_FILTER="$2"
      shift 2
      ;;
    -y|--year)
      YEAR_FILTER="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    -*)
      echo "Error: Unknown option '$1'"
      usage
      exit 1
      ;;
    *)
      break
      ;;
  esac
done

if [[ $# -ne 2 ]]; then
  usage
  exit 1
fi

SOURCE_DIR="$1"
NAS_DEST="$2"

EXCLUDE_ARGS=(--exclude='._*' --exclude='.DS_Store')

if [[ "$EXCLUDE_VIDEOS" == true ]]; then
  for vdir in "${VIDEO_DIRS[@]}"; do
    EXCLUDE_ARGS+=(--exclude="$vdir/**")
  done
fi

# Build date-folder include filters for rclone.
# Date folders are named like: January1st2025, February15th2024
DATE_FILTER_ARGS=()
HAS_DATE_FILTER=false

if [[ -n "$MONTH_FILTER" || -n "$YEAR_FILTER" ]]; then
  HAS_DATE_FILTER=true

  if [[ -n "$MONTH_FILTER" ]]; then
    TARGET_MONTHS=()
    while IFS= read -r line; do
      TARGET_MONTHS+=("$line")
    done < <(build_month_list "$MONTH_FILTER")
  else
    TARGET_MONTHS=("${MONTHS_ORDERED[@]}")
  fi

  year_glob="${YEAR_FILTER:-*}"

  for month in "${TARGET_MONTHS[@]}"; do
    DATE_FILTER_ARGS+=(--filter "+ ${month}*${year_glob}/**")
  done
  DATE_FILTER_ARGS+=(--filter "- *")
fi

if [[ ! -d "$SOURCE_DIR" ]]; then
  echo "Error: '$SOURCE_DIR' is not a directory"
  exit 1
fi

if [[ ! -d "$NAS_DEST" ]]; then
  echo "Error: '$NAS_DEST' is not mounted or does not exist"
  exit 1
fi

echo "=== Syncing cameras to NAS (rclone) ==="
echo "Source: $SOURCE_DIR"
echo "Destination: $NAS_DEST"
echo "Transfers: $TRANSFERS"
[[ "$EXCLUDE_VIDEOS" == true ]] && echo "Excluding video directories: ${VIDEO_DIRS[*]}"
if [[ "$HAS_DATE_FILTER" == true ]]; then
  [[ -n "$MONTH_FILTER" ]] && echo "Month filter: ${TARGET_MONTHS[*]}"
  [[ -n "$YEAR_FILTER" ]] && echo "Year filter: $YEAR_FILTER"
fi
echo ""

# Sync top-level files only when no date filter is active
if [[ "$HAS_DATE_FILTER" == false ]]; then
  rclone copy "$SOURCE_DIR/" "$NAS_DEST/" \
    "${EXCLUDE_ARGS[@]}" \
    --exclude='*/**' \
    --update \
    --transfers="$TRANSFERS" \
    --progress \
    -v
fi

for camera_dir in "$SOURCE_DIR"/*/; do
  [[ -d "$camera_dir" ]] || continue

  camera_name=$(basename "$camera_dir")

  if [[ "$EXCLUDE_VIDEOS" == true ]]; then
    skip=false
    for vdir in "${VIDEO_DIRS[@]}"; do
      if [[ "$camera_name" == "$vdir" ]]; then
        skip=true
        break
      fi
    done
    [[ "$skip" == true ]] && continue
  fi

  echo "--- $camera_name ---"

  if [[ "$HAS_DATE_FILTER" == true ]]; then
    rclone copy "$camera_dir" "$NAS_DEST/$camera_name/" \
      --exclude='._*' \
      --exclude='.DS_Store' \
      "${DATE_FILTER_ARGS[@]}" \
      --update \
      --transfers="$TRANSFERS" \
      --progress \
      -v
  else
    rclone copy "$camera_dir" "$NAS_DEST/$camera_name/" \
      --exclude='._*' \
      --exclude='.DS_Store' \
      --update \
      --transfers="$TRANSFERS" \
      --progress \
      -v
  fi

  echo ""
done

echo "Done!"
