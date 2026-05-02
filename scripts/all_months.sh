#!/usr/bin/env bash
set -uo pipefail

# Run full_run.sh once per month against <base>/<Month>. Each invocation uses
# full_run.sh's prefix-expansion (./full_run.sh <base>/January expands to every
# subdir starting with "January", e.g. January2024, January2025) — so this
# wrapper is a year-spanning sweep across all date folders, batched per month.
#
# Usage:
#   ./all_months.sh /Volumes/T9/X100VI/JPEG
#   ./all_months.sh ~/X100VI/JPEG/
#
# Months with no matching subdirectories don't kill the run — full_run.sh
# exits non-zero on a no-match prefix; this loop catches that and keeps going.
# A summary at the end shows which months ran and which were skipped.

if [[ $# -ne 1 ]]; then
    echo "usage: $0 <base_path>" >&2
    exit 1
fi

# Strip trailing slash so ~/JPEG/ + January doesn't become ~/JPEG//January.
base="${1%/}"

months=(
    January February March April May June
    July August September October November December
)

ran=()
skipped=()
failed=()

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

for month in "${months[@]}"; do
    target="$base/$month"
    echo
    echo "=========================================================="
    echo "  $month  →  $target"
    echo "=========================================================="
    if "$script_dir/full_run.sh" "$target"; then
        ran+=("$month")
    else
        rc=$?
        # full_run.sh's prefix expansion exits 1 with "no directories matching"
        # when a month has no folders. Treat that as "skipped", not "failed".
        # Any other non-zero is a real failure worth surfacing.
        if [[ $rc -eq 1 ]]; then
            skipped+=("$month")
        else
            failed+=("$month (rc=$rc)")
        fi
    fi
done

echo
echo "=========================================================="
echo "  Summary"
echo "=========================================================="
echo "ran:     ${ran[*]:-(none)}"
echo "skipped: ${skipped[*]:-(none)}"
echo "failed:  ${failed[*]:-(none)}"

# Non-zero exit only if a month failed for non-skip reasons.
if [[ ${#failed[@]} -gt 0 ]]; then
    exit 1
fi
