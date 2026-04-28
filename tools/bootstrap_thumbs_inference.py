#!/usr/bin/env python3
"""
One-shot backfill for thumbnails + inference rows on a library.db that was
populated by the old sql_sync.py before Phase 1.5 (when those tables didn't
exist or were empty).

For each photos row missing a thumbnail: read the original file via
photos.file_path, magick-resize to 1024px, insert as BLOB. For each row
missing an inference row: look for a matching <name>.json sidecar in any
describe_* dir under the repo (or via --json-roots) and copy preview_ms /
inference_ms across. Fully optional — once cmd/describe writes direct,
new photos populate both tables on insert.

Usage:
    ./tools/bootstrap_thumbs_inference.sh
    ./tools/bootstrap_thumbs_inference.sh --json-roots describe_april describe_test
    ./tools/bootstrap_thumbs_inference.sh --model qwen/qwen3-vl-8b
    ./tools/bootstrap_thumbs_inference.sh --skip-thumbs    # only fill inference
    ./tools/bootstrap_thumbs_inference.sh --skip-inference # only fill thumbnails
"""

import argparse
import glob
import json
import os
import sqlite3
import subprocess
import sys
import tempfile

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
REPO_DIR = os.path.dirname(SCRIPT_DIR)
DEFAULT_DB = os.path.join(SCRIPT_DIR, ".sql_index", "library.db")
WIDTH = 1024
QUALITY = 85


def make_thumbnail(src):
    """Resize src to a 1024px-bound JPEG. Returns bytes or None on failure."""
    if not src or not os.path.isfile(src):
        return None
    fd, tmp = tempfile.mkstemp(suffix=".jpg")
    os.close(fd)
    try:
        proc = subprocess.run(
            ["magick", src, "-resize", f"{WIDTH}x{WIDTH}>",
             "-quality", str(QUALITY), "-strip", tmp],
            capture_output=True,
        )
        if proc.returncode != 0:
            return None
        with open(tmp, "rb") as f:
            return f.read()
    finally:
        try: os.remove(tmp)
        except OSError: pass


def index_jsons(roots):
    """Map photo name → first matching .json sidecar found across roots."""
    out = {}
    for root in roots:
        if not os.path.isdir(root):
            continue
        for p in glob.glob(os.path.join(root, "**", "*.json"), recursive=True):
            name = os.path.basename(p)[:-5]
            if name and name not in out:
                out[name] = p
    return out


def main():
    ap = argparse.ArgumentParser(description="Backfill thumbnails + inference rows on an existing library.db")
    ap.add_argument("--db", default=DEFAULT_DB, help=f"library DB (default: {DEFAULT_DB})")
    ap.add_argument("--json-roots", nargs="*", default=None,
                    help="dirs to scan for .json sidecars (default: every describe_* under the repo root)")
    ap.add_argument("--model", default="qwen/qwen3-vl-8b",
                    help="model name to record in inference.model when the JSON sidecar doesn't carry one")
    ap.add_argument("--skip-thumbs", action="store_true")
    ap.add_argument("--skip-inference", action="store_true")
    args = ap.parse_args()

    if not os.path.isfile(args.db):
        print(f"db not found: {args.db}", file=sys.stderr)
        sys.exit(1)

    json_roots = args.json_roots or sorted(glob.glob(os.path.join(REPO_DIR, "describe_*")))
    json_index = {} if args.skip_inference else index_jsons(json_roots)
    if not args.skip_inference:
        print(f"scanning {len(json_roots)} describe_* dir(s) for JSON sidecars: {len(json_index)} found")

    conn = sqlite3.connect(args.db)
    conn.execute("PRAGMA foreign_keys = ON")
    rows = conn.execute("SELECT id, name, file_path FROM photos ORDER BY name").fetchall()
    print(f"photos in DB: {len(rows)}")

    thumb_done = thumb_skip = inf_done = inf_skip = 0
    for photo_id, name, file_path in rows:
        if not args.skip_thumbs:
            have = conn.execute("SELECT 1 FROM thumbnails WHERE photo_id = ?", (photo_id,)).fetchone()
            if have is None:
                data = make_thumbnail(file_path)
                if data is None:
                    print(f"  [skip thumb] {name}: {file_path!r} not readable", file=sys.stderr)
                    thumb_skip += 1
                else:
                    conn.execute(
                        "INSERT INTO thumbnails (photo_id, bytes, width) VALUES (?, ?, ?)",
                        (photo_id, data, WIDTH),
                    )
                    thumb_done += 1
                    if thumb_done % 25 == 0:
                        print(f"  ...{thumb_done} thumbnails so far")
                        conn.commit()

        if not args.skip_inference:
            have = conn.execute("SELECT 1 FROM inference WHERE photo_id = ?", (photo_id,)).fetchone()
            if have is None:
                json_path = json_index.get(name)
                if not json_path:
                    inf_skip += 1
                    continue
                try:
                    with open(json_path) as f:
                        data = json.load(f)
                except Exception as e:
                    print(f"  [skip inference] {name}: {e}", file=sys.stderr)
                    inf_skip += 1
                    continue
                conn.execute(
                    "INSERT INTO inference (photo_id, raw_response, model, preview_ms, inference_ms) "
                    "VALUES (?, NULL, ?, ?, ?)",
                    (photo_id, data.get("model") or args.model,
                     data.get("preview_ms"), data.get("inference_ms")),
                )
                inf_done += 1

    conn.commit()
    conn.close()

    print()
    if not args.skip_thumbs:
        print(f"thumbnails: filled {thumb_done}, skipped {thumb_skip} (file_path not readable)")
    if not args.skip_inference:
        print(f"inference:  filled {inf_done}, skipped {inf_skip} (no matching JSON sidecar)")


if __name__ == "__main__":
    main()
