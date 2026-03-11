#!/usr/bin/env bash
set -euo pipefail

# Usage: ./organize_media.sh /path/to/directory

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <directory>"
  exit 1
fi

TARGET_DIR="$1"

if [[ ! -d "$TARGET_DIR" ]]; then
  echo "Error: '$TARGET_DIR' is not a directory"
  exit 1
fi

SIDECAR_EXTS=(dxo dop pp3 xml)

ordinal_suffix() {
  local day=$1
  case "$day" in
    1|21|31) echo "${day}st" ;;
    2|22)    echo "${day}nd" ;;
    3|23)    echo "${day}rd" ;;
    *)       echo "${day}th" ;;
  esac
}

format_date() {
  local epoch=$1
  local month year day_num day_ord
  month=$(date -r "$epoch" +"%B")
  day_num=$(date -r "$epoch" +"%-d")
  year=$(date -r "$epoch" +"%Y")
  day_ord=$(ordinal_suffix "$day_num")
  echo "${month}${day_ord}${year}"
}

get_type_folder() {
  local ext
  ext=$(echo "$1" | tr '[:upper:]' '[:lower:]')
  case "$ext" in
    jpg|jpeg)                          echo "JPEG" ;;
    hif|heif|heic)                     echo "HIF" ;;
    mov)                               echo "MOV" ;;
    mp4)                               echo "MP4" ;;
    braw)                              echo "BRAW" ;;
    nev) echo "NEV" ;;
    ndf) echo "NDF" ;;
    raf|arw|nef|cr2|cr3|dng|orf|rw2|pef) echo "RAW" ;;
    *)                                 echo "" ;;
  esac
}

is_sidecar() {
  local ext
  ext=$(echo "$1" | tr '[:upper:]' '[:lower:]')
  for s in "${SIDECAR_EXTS[@]}"; do
    [[ "$ext" == "$s" ]] && return 0
  done
  return 1
}

# Move a file and any matching sidecars to a destination directory
move_with_sidecars() {
  local file=$1
  local dest_dir=$2
  local source_dir
  source_dir=$(dirname "$file")
  local filename
  filename=$(basename "$file")
  local base="${filename%.*}"

  mv "$file" "$dest_dir/$filename"
  echo "  $filename -> $(basename "$dest_dir")/"

  # Find and move matching sidecar files (e.g., photo.jpg.xmp or photo.xmp)
  for s in "${SIDECAR_EXTS[@]}"; do
    local s_upper
    s_upper=$(echo "$s" | tr '[:lower:]' '[:upper:]')
    for pattern in "$source_dir/$base.$s" "$source_dir/$filename.$s" \
                   "$source_dir/$base.$s_upper" "$source_dir/$filename.$s_upper"; do
      if [[ -f "$pattern" ]]; then
        local sidecar_name
        sidecar_name=$(basename "$pattern")
        mv "$pattern" "$dest_dir/$sidecar_name"
        echo "  $sidecar_name -> $(basename "$dest_dir")/ (sidecar)"
      fi
    done
  done
}

moved=0
skipped=0

echo "=== Pass 1: Organizing files by type ==="

for file in "$TARGET_DIR"/*; do
  [[ -f "$file" ]] || continue

  filename=$(basename "$file")
  ext="${filename##*.}"

  # Skip files with no extension
  [[ "$ext" == "$filename" ]] && continue

  # Skip sidecar files — they travel with their parent
  is_sidecar "$ext" && continue

  type_folder=$(get_type_folder "$ext")
  [[ -z "$type_folder" ]] && { ((skipped++)); continue; }

  mkdir -p "$TARGET_DIR/$type_folder"
  move_with_sidecars "$file" "$TARGET_DIR/$type_folder"
  ((moved++))
done

echo "  Moved $moved files ($skipped skipped)"

echo ""
echo "=== Pass 2: Organizing files by creation date ==="

for type_dir in "$TARGET_DIR"/*/; do
  [[ -d "$type_dir" ]] || continue

  type_name=$(basename "$type_dir")

  for file in "$type_dir"*; do
    [[ -f "$file" ]] || continue

    filename=$(basename "$file")
    ext="${filename##*.}"

    # Skip sidecars — they travel with their parent
    is_sidecar "$ext" && continue

    birthtime=$(stat -f %B "$file")
    date_folder=$(format_date "$birthtime")

    # Already in the right date subfolder
    if [[ "$(basename "$(dirname "$file")")" == "$date_folder" ]]; then
      continue
    fi

    mkdir -p "$type_dir$date_folder"
    move_with_sidecars "$file" "$type_dir$date_folder"
  done
done

echo ""
echo "=== Pass 3: Reuniting orphaned sidecar files ==="

for file in "$TARGET_DIR"/*; do
  [[ -f "$file" ]] || continue

  filename=$(basename "$file")
  ext="${filename##*.}"

  is_sidecar "$ext" || continue

  # Try base name match (photo.xml -> find photo.mp4)
  # and full name match (photo.mp4.xml -> find photo.mp4)
  base="${filename%.*}"
  base_of_base="${base%.*}"

  parent_path=""
  # Search type folders and their date subfolders for the parent
  for candidate in "$TARGET_DIR"/"$base".* "$TARGET_DIR"/*/"$base".* "$TARGET_DIR"/*/*/"$base".*; do
    [[ -f "$candidate" ]] || continue
    cand_ext="${candidate##*.}"
    is_sidecar "$cand_ext" && continue
    parent_path="$candidate"
    break
  done

  # If base was like photo.mp4 (from photo.mp4.xml), try base_of_base
  if [[ -z "$parent_path" && "$base_of_base" != "$base" ]]; then
    for candidate in "$TARGET_DIR"/"$base_of_base".* "$TARGET_DIR"/*/"$base_of_base".* "$TARGET_DIR"/*/*/"$base_of_base".*; do
      [[ -f "$candidate" ]] || continue
      cand_ext="${candidate##*.}"
      is_sidecar "$cand_ext" && continue
      parent_path="$candidate"
      break
    done
  fi

  if [[ -n "$parent_path" ]]; then
    dest_dir=$(dirname "$parent_path")
    mv "$file" "$dest_dir/$filename"
    echo "  $filename -> ${dest_dir#$TARGET_DIR/}/ (reunited)"
  else
    # XML sidecars default to MP4 folder (Sony workflow)
    ext_lower=$(echo "$ext" | tr '[:upper:]' '[:lower:]')
    if [[ "$ext_lower" == "xml" ]]; then
      birthtime=$(stat -f %B "$file")
      date_folder=$(format_date "$birthtime")
      mkdir -p "$TARGET_DIR/MP4/$date_folder"
      mv "$file" "$TARGET_DIR/MP4/$date_folder/$filename"
      echo "  $filename -> MP4/$date_folder/"
    else
      echo "  $filename — no parent found, leaving in place"
    fi
  fi
done

echo ""
echo "Done!"
