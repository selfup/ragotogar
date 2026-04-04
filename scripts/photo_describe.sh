#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DESCRIBE_DIR="$SCRIPT_DIR/../cmd/describe"

# Resolve relative paths before cd-ing into the Go module directory.
args=()
next_is_output=false
for arg in "$@"; do
  if $next_is_output; then
    # Make -output path absolute relative to the caller's working directory
    [[ "$arg" != /* ]] && arg="$PWD/$arg"
    next_is_output=false
  elif [[ "$arg" == "-output" ]]; then
    next_is_output=true
  fi
  args+=("$arg")
done

cd "$DESCRIBE_DIR"
go run . "${args[@]}"
