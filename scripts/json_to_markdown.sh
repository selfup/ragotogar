#!/usr/bin/env bash
#
# json_to_markdown.sh — Convert photo description JSONs to human-readable markdown.
#
# Usage:
#   ./scripts/json_to_markdown.sh /path/to/json_dir              # one .md per .json
#   ./scripts/json_to_markdown.sh -combined /path/to/json_dir     # single combined .md
#   ./scripts/json_to_markdown.sh -output /tmp/out /path/to/json_dir
#
# Output goes to <json_dir>/markdown/ by default.

set -euo pipefail

COMBINED=false
OUTPUT_DIR=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    -combined) COMBINED=true; shift ;;
    -output)   OUTPUT_DIR="$2"; shift 2 ;;
    *)         break ;;
  esac
done

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 [-combined] [-output DIR] <json_dir>"
  exit 1
fi

JSON_DIR="$1"

if [[ ! -d "$JSON_DIR" ]]; then
  echo "Error: $JSON_DIR is not a directory"
  exit 1
fi

# Collect JSON files
shopt -s nullglob
json_files=("$JSON_DIR"/*.json)
shopt -u nullglob

if [[ ${#json_files[@]} -eq 0 ]]; then
  echo "No .json files found in $JSON_DIR"
  exit 1
fi

# Sort by filename
IFS=$'\n' json_files=($(sort <<<"${json_files[*]}")); unset IFS

OUTPUT_DIR="${OUTPUT_DIR:-$JSON_DIR/markdown}"
mkdir -p "$OUTPUT_DIR"

# Convert one JSON file to markdown, output to stdout
render_one() {
  local f="$1"
  local name file date_orig make model focal fn et iso ev wb meter mode flash w h

  name=$(jq -r '.name' "$f")
  file=$(jq -r '.file' "$f")
  date_orig=$(jq -r '.metadata.date_time_original // empty' "$f")
  make=$(jq -r '.metadata.make // empty' "$f")
  model=$(jq -r '.metadata.model // empty' "$f")
  focal=$(jq -r '.metadata.focal_length // empty' "$f")
  fn=$(jq -r '.metadata.f_number // empty' "$f")
  et=$(jq -r '.metadata.exposure_time // empty' "$f")
  iso=$(jq -r '.metadata.iso // empty' "$f")
  ev=$(jq -r '.metadata.exposure_compensation // empty' "$f")
  wb=$(jq -r '.metadata.white_balance // empty' "$f")
  meter=$(jq -r '.metadata.metering_mode // empty' "$f")
  mode=$(jq -r '.metadata.exposure_mode // empty' "$f")
  flash=$(jq -r '.metadata.flash // empty' "$f")
  w=$(jq -r '.metadata.image_width // empty' "$f")
  h=$(jq -r '.metadata.image_height // empty' "$f")

  local duration
  duration=$(jq -r '.duration // empty' "$f")

  # Format date for display: "2026:03:21 09:32:17" -> "March 21, 2026 at 09:32:17"
  local display_date="$date_orig"
  if [[ -n "$date_orig" ]]; then
    # Parse EXIF date format
    local y m d rest
    y="${date_orig%%:*}"
    rest="${date_orig#*:}"
    m="${rest%%:*}"
    rest="${rest#*:}"
    d="${rest%% *}"
    local t="${rest#* }"
    # Month name
    local months=("" "January" "February" "March" "April" "May" "June"
                  "July" "August" "September" "October" "November" "December")
    local mi=$((10#$m))
    local di=$((10#$d))
    if [[ $mi -ge 1 && $mi -le 12 ]]; then
      display_date="${months[$mi]} $di, $y at $t"
    fi
  fi

  echo "# $name"
  echo ""
  echo "**File:** \`$file\`"
  [[ -n "$display_date" ]] && echo "**Date:** $display_date"
  [[ -n "$duration" ]] && echo "**LLM Processing Time:** $duration"
  echo ""

  # Camera settings table
  echo "## Camera Settings"
  echo ""
  echo "| Setting | Value |"
  echo "|---------|-------|"
  [[ -n "$make" ]]  && echo "| Camera | $make $model |"
  [[ -n "$focal" ]] && echo "| Focal Length | $focal |"
  [[ -n "$fn" ]]    && echo "| Aperture | f/$fn |"
  [[ -n "$et" ]]    && echo "| Shutter Speed | $et |"
  [[ -n "$iso" ]]   && echo "| ISO | $iso |"
  [[ -n "$ev" ]]    && echo "| Exposure Comp | $ev |"
  [[ -n "$wb" ]]    && echo "| White Balance | $wb |"
  [[ -n "$meter" ]] && echo "| Metering | $meter |"
  [[ -n "$mode" ]]  && echo "| Exposure Mode | $mode |"
  [[ -n "$flash" ]] && echo "| Flash | $flash |"
  [[ -n "$w" && -n "$h" ]] && echo "| Resolution | ${w}×${h} |"
  echo ""

  # Description fields
  local field_names=("subject" "setting" "light" "colors" "composition")
  local field_titles=("Subject" "Setting" "Light" "Colors" "Composition")

  for i in "${!field_names[@]}"; do
    local val
    val=$(jq -r ".fields.${field_names[$i]} // empty" "$f")
    if [[ -n "$val" ]]; then
      echo "## ${field_titles[$i]}"
      echo ""
      echo "$val"
      echo ""
    fi
  done
}

if [[ "$COMBINED" == true ]]; then
  # Single combined file
  outfile="$OUTPUT_DIR/all_photos.md"
  {
    echo "# Photo Descriptions"
    echo ""
    echo "_${#json_files[@]} photos generated from $(basename "$JSON_DIR") descriptions_"
    echo ""
    echo "---"
    echo ""
    for f in "${json_files[@]}"; do
      render_one "$f"
      echo "---"
      echo ""
    done
  } > "$outfile"
  echo "Wrote combined markdown: $outfile"
else
  # One file per JSON
  for f in "${json_files[@]}"; do
    base=$(basename "$f" .json)
    outfile="$OUTPUT_DIR/$base.md"
    render_one "$f" > "$outfile"
    echo "  $outfile"
  done
  echo "Wrote ${#json_files[@]} markdown files to $OUTPUT_DIR"
fi
