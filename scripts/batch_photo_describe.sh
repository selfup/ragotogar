#!/usr/bin/env bash
set -euo pipefail

# Wraps photo_describe.sh to run against every sibling directory matching a
# basename prefix. Example:
#
#   ./scripts/batch_photo_describe.sh -output describe_test /Volumes/T9/X100VI/JPEG/March
#
# expands to every directory under /Volumes/T9/X100VI/JPEG whose name starts
# with "March" (e.g. March21st2026, March22nd2026) and runs photo_describe.sh
# on each one in sequence, reusing the same flags.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PHOTO_DESCRIBE="$SCRIPT_DIR/photo_describe.sh"

if [[ $# -lt 1 ]]; then
  echo "usage: $0 [photo_describe flags] <parent_dir>/<prefix>" >&2
  exit 1
fi

# Last positional arg is the <parent>/<prefix>. Everything else is passed through.
prefix_arg="${!#}"
passthrough_args=("${@:1:$#-1}")

parent_dir="$(dirname "$prefix_arg")"
prefix="$(basename "$prefix_arg")"

if [[ ! -d "$parent_dir" ]]; then
  echo "error: parent directory does not exist: $parent_dir" >&2
  exit 1
fi

# Collect matching subdirectories in sorted order.
matches=()
while IFS= read -r -d '' dir; do
  matches+=("$dir")
done < <(find "$parent_dir" -mindepth 1 -maxdepth 1 -type d -name "${prefix}*" -print0 | sort -z)

if [[ ${#matches[@]} -eq 0 ]]; then
  echo "error: no directories in $parent_dir match '${prefix}*'" >&2
  exit 1
fi

echo "batch_photo_describe: ${#matches[@]} director$([[ ${#matches[@]} -eq 1 ]] && echo y || echo ies) matching '${prefix}*' in $parent_dir"
for dir in "${matches[@]}"; do
  echo "  - $dir"
done
echo

for dir in "${matches[@]}"; do
  echo "=== photo_describe: $dir ==="
  "$PHOTO_DESCRIBE" "${passthrough_args[@]}" "$dir"
done
