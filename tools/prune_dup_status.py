#!/usr/bin/env python3
"""
Prune LightRAG duplicate-audit rows from kv_store_doc_status.json.

When LightRAG re-ingests content it has already seen, it inserts a `dup-*`
row into doc_status (status=failed, metadata.is_duplicate=true) to preserve
an audit trail. These accumulate on every re-run and bloat the status store.
This script removes them, writing a timestamped backup first.

Usage:
    python prune_dup_status.py            # dry run
    python prune_dup_status.py --apply    # rewrite the file
"""

import argparse
import json
import os
import shutil
import sys
from datetime import datetime

from rag_common import INDEX_DIR

STATUS_PATH = os.path.join(INDEX_DIR, "kv_store_doc_status.json")


def is_dup_row(key, value):
    if key.startswith("dup-"):
        return True
    meta = value.get("metadata") or {}
    return bool(meta.get("is_duplicate"))


def main():
    parser = argparse.ArgumentParser(description="Prune duplicate-audit rows from doc_status")
    parser.add_argument("--apply", action="store_true", help="rewrite the file (default: dry-run)")
    parser.add_argument("--path", default=STATUS_PATH, help=f"path to kv_store_doc_status.json (default: {STATUS_PATH})")
    args = parser.parse_args()

    if not os.path.exists(args.path):
        print(f"No doc_status file at {args.path}. Run index_and_vectorize.py first.", file=sys.stderr)
        sys.exit(1)

    with open(args.path) as f:
        data = json.load(f)

    dup_keys = [k for k, v in data.items() if is_dup_row(k, v)]
    kept = {k: v for k, v in data.items() if k not in set(dup_keys)}

    print(f"file:           {args.path}")
    print(f"total rows:     {len(data)}")
    print(f"duplicate rows: {len(dup_keys)}")
    print(f"would keep:     {len(kept)}")

    if not dup_keys:
        print("nothing to prune.")
        return

    if not args.apply:
        print("\ndry-run — pass --apply to rewrite the file.")
        return

    backup = f"{args.path}.bak.{datetime.now():%Y%m%d_%H%M%S}"
    shutil.copy2(args.path, backup)
    print(f"\nbackup written to {backup}")

    tmp = args.path + ".tmp"
    with open(tmp, "w") as f:
        json.dump(kept, f, ensure_ascii=False, indent=2)
    os.replace(tmp, args.path)
    print(f"rewrote {args.path} ({len(kept)} rows, removed {len(dup_keys)})")


if __name__ == "__main__":
    main()
