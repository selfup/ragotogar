#!/usr/bin/env bash
set -euo pipefail

# Usage: ./flatten.sh /path/to/directory
# Pulls all files from subdirectories into the target directory, then removes empty subdirectories.

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <directory>"
  exit 1
fi

TARGET_DIR="$1"

if [[ ! -d "$TARGET_DIR" ]]; then
  echo "Error: '$TARGET_DIR' is not a directory"
  exit 1
fi

# Move all files from subdirectories into the target directory
find "$TARGET_DIR" -mindepth 2 -type f -print0 | while IFS= read -r -d '' file; do
  filename="$(basename "$file")"

  # Skip macOS ._ resource fork files
  if [[ "$filename" == ._* ]]; then
    continue
  fi

  dest="$TARGET_DIR/$filename"

  # Handle name collisions by appending a counter
  if [[ -e "$dest" ]]; then
    base="${filename%.*}"
    ext="${filename##*.}"
    if [[ "$base" == "$filename" ]]; then
      ext=""
    fi
    counter=1
    while true; do
      if [[ -n "$ext" && "$ext" != "$base" ]]; then
        dest="$TARGET_DIR/${base}_${counter}.${ext}"
      else
        dest="$TARGET_DIR/${filename}_${counter}"
      fi
      [[ ! -e "$dest" ]] && break
      ((counter++))
    done
  fi

  mv "$file" "$dest"
  echo "Moved: $file -> $dest"
done

# Remove now-empty subdirectories
find "$TARGET_DIR" -mindepth 1 -type d -empty -delete

echo "Done. All files flattened into: $TARGET_DIR"
