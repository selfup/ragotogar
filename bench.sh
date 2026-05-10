#!/usr/bin/env bash
# Run all Go benchmarks across every module. Output goes to stdout in
# the standard `go test -bench` format — pipe through benchstat to
# compare two runs:
#
#   ./bench.sh > before.txt
#   <make changes>
#   ./bench.sh > after.txt
#   benchstat before.txt after.txt
#
# benchstat install: `go install golang.org/x/perf/cmd/benchstat@latest`
#
# Benchmarks live in benchmark_test.go files and stay out of the
# regular test path. Adding a new benchmark: write a `Benchmark*`
# function in the appropriate package; this script picks it up
# automatically.

set -euo pipefail
ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"

run_bench() {
  local dir="$1"
  local label="$2"
  echo "========================================"
  echo "  benchmarks: $label"
  echo "========================================"
  # -benchtime=1s is the Go default. Bump (-benchtime=5s) for stable
  # numbers when you actually need a baseline. -benchmem adds the
  # bytes/allocation columns benchstat understands.
  (cd "$dir" && go test -bench=. -benchmem -run='^$' ./...)
  echo ""
}

# Root module — library, cmd/cashier, cmd/edge, cmd/edge_build,
# cmd/index, cmd/search, cmd/web, prompts.
run_bench "$ROOT_DIR" "root module"

# Sub-modules.
for mod in "$ROOT_DIR"/cmd/*/; do
  [ -f "$mod/go.mod" ] || continue
  run_bench "$mod" "$(basename "$mod")"
done
