package main

// schemaSQL is applied on every cmd/describe run via CREATE TABLE IF NOT
// EXISTS — idempotent on existing libraries, creates a fresh one if missing.
//
// cmd/describe is the only schema authority. cmd/web and the Python tools
// open the DB read-only (or with simple INSERTs that don't touch DDL).
//
// Phase 1.5 collapsed-slice schema. Photos table is the post-cutover shape:
// no md_path / html_path / jpg_path / json_path columns — derived artifacts
// live in `rendered`-equivalent tables (`thumbnails`) or are templated on
// demand.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS photos (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    file_path     TEXT,
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

CREATE VIRTUAL TABLE IF NOT EXISTS descriptions_fts USING fts5(
    subject, setting, light, colors, composition, full_description,
    content=descriptions, content_rowid=rowid,
    tokenize='porter unicode61'
);

CREATE TRIGGER IF NOT EXISTS descriptions_ai AFTER INSERT ON descriptions BEGIN
    INSERT INTO descriptions_fts(rowid, subject, setting, light, colors, composition, full_description)
    VALUES (new.rowid, new.subject, new.setting, new.light, new.colors, new.composition, new.full_description);
END;
CREATE TRIGGER IF NOT EXISTS descriptions_ad AFTER DELETE ON descriptions BEGIN
    INSERT INTO descriptions_fts(descriptions_fts, rowid, subject, setting, light, colors, composition, full_description)
    VALUES ('delete', old.rowid, old.subject, old.setting, old.light, old.colors, old.composition, old.full_description);
END;
CREATE TRIGGER IF NOT EXISTS descriptions_au AFTER UPDATE ON descriptions BEGIN
    INSERT INTO descriptions_fts(descriptions_fts, rowid, subject, setting, light, colors, composition, full_description)
    VALUES ('delete', old.rowid, old.subject, old.setting, old.light, old.colors, old.composition, old.full_description);
    INSERT INTO descriptions_fts(rowid, subject, setting, light, colors, composition, full_description)
    VALUES (new.rowid, new.subject, new.setting, new.light, new.colors, new.composition, new.full_description);
END;

CREATE TABLE IF NOT EXISTS thumbnails (
    photo_id   TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    bytes      BLOB NOT NULL,
    width      INTEGER NOT NULL DEFAULT 1024,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS inference (
    photo_id     TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    raw_response TEXT,
    model        TEXT,
    preview_ms   INTEGER,
    inference_ms INTEGER,
    described_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`

// legacyPathCols are columns from the slice-1 schema that the cutover removes.
// Dropped via ALTER TABLE on existing databases; absent on fresh ones (the
// CREATE TABLE above already omits them).
var legacyPathCols = []string{"json_path", "md_path", "html_path", "jpg_path"}
