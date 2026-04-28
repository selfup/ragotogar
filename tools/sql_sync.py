#!/usr/bin/env python3
"""
Sync photo description JSONs into a SQLite metadata index.

Walks a directory of *.json sidecars (produced by cmd/describe + cashier) and
populates the photos / exif / descriptions tables at tools/.sql_index/library.db.

Phase 1 of the architecture: structured-query foundation. JSONs remain the
source of truth; this is a derived index. Idempotent — safe to re-run.

Usage:
    ./tools/sql_sync.sh <dir>
    ./tools/sql_sync.sh --reset <dir>
    ./tools/sql_sync.sh --db /path/to/other.db <dir>
"""

import argparse
import json
import os
import re
import sqlite3
import sys
from datetime import datetime, timezone

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
DEFAULT_DB = os.path.join(SCRIPT_DIR, ".sql_index", "library.db")
SCHEMA_VERSION = 1


SCHEMA = """
CREATE TABLE IF NOT EXISTS schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS photos (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    json_path     TEXT NOT NULL,
    file_path     TEXT,
    md_path       TEXT,
    html_path     TEXT,
    jpg_path      TEXT,
    file_basename TEXT,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_photos_name ON photos(name);

CREATE TABLE IF NOT EXISTS exif (
    photo_id              TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    camera_make           TEXT,
    camera_model          TEXT,
    lens_model            TEXT,
    lens_info             TEXT,
    date_taken            TEXT,
    date_taken_year       INTEGER,
    date_taken_month      INTEGER,
    focal_length_mm       REAL,
    focal_length_35mm     REAL,
    f_number              REAL,
    exposure_time_seconds REAL,
    iso                   INTEGER,
    exposure_compensation REAL,
    exposure_mode         TEXT,
    metering_mode         TEXT,
    white_balance         TEXT,
    flash                 TEXT,
    image_width           INTEGER,
    image_height          INTEGER,
    gps_latitude          REAL,
    gps_longitude         REAL,
    artist                TEXT,
    software              TEXT
);
CREATE INDEX IF NOT EXISTS idx_exif_camera     ON exif(camera_model);
CREATE INDEX IF NOT EXISTS idx_exif_make       ON exif(camera_make);
CREATE INDEX IF NOT EXISTS idx_exif_date       ON exif(date_taken);
CREATE INDEX IF NOT EXISTS idx_exif_year_month ON exif(date_taken_year, date_taken_month);
CREATE INDEX IF NOT EXISTS idx_exif_iso        ON exif(iso);
CREATE INDEX IF NOT EXISTS idx_exif_aperture   ON exif(f_number);
CREATE INDEX IF NOT EXISTS idx_exif_focal      ON exif(focal_length_mm);
CREATE INDEX IF NOT EXISTS idx_exif_artist     ON exif(artist);

CREATE TABLE IF NOT EXISTS descriptions (
    photo_id          TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    subject           TEXT,
    setting           TEXT,
    light             TEXT,
    colors            TEXT,
    composition       TEXT,
    full_description  TEXT
);
"""


# ── parsers ───────────────────────────────────────────────────────────────

def _clean(s):
    if s is None:
        return None
    if isinstance(s, str):
        s = s.strip()
        return s if s else None
    return s


def parse_float(v):
    """Parse '23.0', '5.6', 23.0, '' → float or None. Tolerates leading 'f/'."""
    v = _clean(v)
    if v is None:
        return None
    if isinstance(v, (int, float)):
        return float(v)
    s = v.lstrip("fF/").strip()
    try:
        return float(s)
    except ValueError:
        return None


def parse_int(v):
    v = _clean(v)
    if v is None:
        return None
    try:
        return int(float(v))
    except (ValueError, TypeError):
        return None


_TRAILING_UNITS = re.compile(r"\s*(mm|MM)\s*$")


def parse_dimension_mm(v):
    """'23.0 mm' → 23.0; '23.0' → 23.0; '' → None."""
    v = _clean(v)
    if v is None:
        return None
    s = _TRAILING_UNITS.sub("", str(v))
    try:
        return float(s)
    except ValueError:
        return None


def parse_exposure_time(v):
    """'1/250' → 0.004, '0.5' → 0.5, '' → None."""
    v = _clean(v)
    if v is None:
        return None
    s = str(v)
    if "/" in s:
        try:
            num, denom = s.split("/", 1)
            return float(num) / float(denom)
        except (ValueError, ZeroDivisionError):
            return None
    try:
        return float(s)
    except ValueError:
        return None


def parse_exif_date(v):
    """'2024:04:21 16:27:54' → ('2024-04-21T16:27:54', 2024, 4)."""
    v = _clean(v)
    if v is None:
        return None, None, None
    parts = v.split()
    date_parts = parts[0].split(":") if parts else []
    if len(date_parts) != 3:
        return None, None, None
    try:
        year, month, day = int(date_parts[0]), int(date_parts[1]), int(date_parts[2])
    except ValueError:
        return None, None, None
    if not (1 <= month <= 12):
        return None, None, None
    iso = f"{year:04d}-{month:02d}-{day:02d}"
    if len(parts) > 1:
        iso += f"T{parts[1]}"
    return iso, year, month


# ── sync ──────────────────────────────────────────────────────────────────

def init_schema(conn, reset=False):
    if reset:
        conn.executescript("""
            DROP TABLE IF EXISTS descriptions;
            DROP TABLE IF EXISTS exif;
            DROP TABLE IF EXISTS photos;
            DROP TABLE IF EXISTS schema_version;
        """)
    conn.executescript(SCHEMA)
    conn.execute(
        "INSERT OR IGNORE INTO schema_version(version, applied_at) VALUES (?, ?)",
        (SCHEMA_VERSION, datetime.now(timezone.utc).isoformat()),
    )
    conn.commit()


def sidecar(json_path, ext):
    p = json_path[:-5] + ext
    return os.path.abspath(p) if os.path.exists(p) else None


def upsert_photo(conn, data, json_path):
    name = data["name"]
    conn.execute("""
        INSERT INTO photos (id, name, json_path, file_path, md_path, html_path, jpg_path, file_basename)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            json_path     = excluded.json_path,
            file_path     = excluded.file_path,
            md_path       = excluded.md_path,
            html_path     = excluded.html_path,
            jpg_path      = excluded.jpg_path,
            file_basename = excluded.file_basename,
            updated_at    = datetime('now')
    """, (
        name, name,
        os.path.abspath(json_path),
        data.get("path"),
        sidecar(json_path, ".md"),
        sidecar(json_path, ".html"),
        sidecar(json_path, ".jpg"),
        data.get("file"),
    ))


def upsert_exif(conn, data):
    meta = data.get("metadata") or {}
    iso_date, year, month = parse_exif_date(meta.get("date_time_original"))
    conn.execute("""
        INSERT INTO exif (
            photo_id, camera_make, camera_model, lens_model, lens_info,
            date_taken, date_taken_year, date_taken_month,
            focal_length_mm, focal_length_35mm, f_number, exposure_time_seconds,
            iso, exposure_compensation, exposure_mode, metering_mode, white_balance, flash,
            image_width, image_height, gps_latitude, gps_longitude, artist, software
        ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
        ON CONFLICT(photo_id) DO UPDATE SET
            camera_make           = excluded.camera_make,
            camera_model          = excluded.camera_model,
            lens_model            = excluded.lens_model,
            lens_info             = excluded.lens_info,
            date_taken            = excluded.date_taken,
            date_taken_year       = excluded.date_taken_year,
            date_taken_month      = excluded.date_taken_month,
            focal_length_mm       = excluded.focal_length_mm,
            focal_length_35mm     = excluded.focal_length_35mm,
            f_number              = excluded.f_number,
            exposure_time_seconds = excluded.exposure_time_seconds,
            iso                   = excluded.iso,
            exposure_compensation = excluded.exposure_compensation,
            exposure_mode         = excluded.exposure_mode,
            metering_mode         = excluded.metering_mode,
            white_balance         = excluded.white_balance,
            flash                 = excluded.flash,
            image_width           = excluded.image_width,
            image_height          = excluded.image_height,
            gps_latitude          = excluded.gps_latitude,
            gps_longitude         = excluded.gps_longitude,
            artist                = excluded.artist,
            software              = excluded.software
    """, (
        data["name"],
        _clean(meta.get("make")),
        _clean(meta.get("model")),
        _clean(meta.get("lens_model")),
        _clean(meta.get("lens_info")),
        iso_date, year, month,
        parse_dimension_mm(meta.get("focal_length")),
        parse_dimension_mm(meta.get("focal_length_in_35mm")),
        parse_float(meta.get("f_number")),
        parse_exposure_time(meta.get("exposure_time")),
        parse_int(meta.get("iso")),
        parse_float(meta.get("exposure_compensation")),
        _clean(meta.get("exposure_mode")),
        _clean(meta.get("metering_mode")),
        _clean(meta.get("white_balance")),
        _clean(meta.get("flash")),
        parse_int(meta.get("image_width")),
        parse_int(meta.get("image_height")),
        parse_float(meta.get("gps_latitude")),
        parse_float(meta.get("gps_longitude")),
        _clean(meta.get("artist")),
        _clean(meta.get("software")),
    ))


def upsert_description(conn, data):
    fields = data.get("fields") or {}
    conn.execute("""
        INSERT INTO descriptions (photo_id, subject, setting, light, colors, composition, full_description)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(photo_id) DO UPDATE SET
            subject          = excluded.subject,
            setting          = excluded.setting,
            light            = excluded.light,
            colors           = excluded.colors,
            composition      = excluded.composition,
            full_description = excluded.full_description
    """, (
        data["name"],
        _clean(fields.get("subject")),
        _clean(fields.get("setting")),
        _clean(fields.get("light")),
        _clean(fields.get("colors")),
        _clean(fields.get("composition")),
        _clean(data.get("description")),
    ))


def sync_one(conn, json_path):
    with open(json_path) as f:
        data = json.load(f)
    if "name" not in data:
        print(f"  [skip] {json_path}: no 'name' field", file=sys.stderr)
        return False
    upsert_photo(conn, data, json_path)
    upsert_exif(conn, data)
    upsert_description(conn, data)
    return True


def walk_jsons(root):
    for dirpath, _, files in os.walk(root):
        for fn in sorted(files):
            if fn.endswith(".json") and not fn.startswith("._"):
                yield os.path.join(dirpath, fn)


def main():
    ap = argparse.ArgumentParser(description="Sync photo JSONs into SQLite metadata index")
    ap.add_argument("dir", help="directory of photo description .json files")
    ap.add_argument("--db", default=DEFAULT_DB, help=f"SQLite DB path (default: {DEFAULT_DB})")
    ap.add_argument("--reset", action="store_true", help="drop and recreate tables before sync")
    args = ap.parse_args()

    if not os.path.isdir(args.dir):
        print(f"Not a directory: {args.dir}", file=sys.stderr)
        sys.exit(1)

    os.makedirs(os.path.dirname(args.db), exist_ok=True)
    conn = sqlite3.connect(args.db)
    conn.execute("PRAGMA foreign_keys = ON")
    init_schema(conn, reset=args.reset)

    synced = 0
    skipped = 0
    for path in walk_jsons(args.dir):
        try:
            if sync_one(conn, path):
                synced += 1
            else:
                skipped += 1
        except (json.JSONDecodeError, OSError) as e:
            print(f"  [error] {path}: {e}", file=sys.stderr)
            skipped += 1
    conn.commit()
    conn.close()
    print(f"synced {synced} photo(s) into {args.db} (skipped {skipped})")


if __name__ == "__main__":
    main()
