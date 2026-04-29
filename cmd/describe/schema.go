package main

// schemaSQL is applied on every cmd/describe run via CREATE TABLE IF NOT
// EXISTS — idempotent on existing libraries, creates the schema fresh on
// an empty Postgres database.
//
// cmd/describe is the schema authority. cmd/web and the Python tools open
// the DB read-only (or with simple INSERTs that don't touch DDL).
//
// Phase 2 schema: Postgres + pgvector. The chunks table is new (vector(768)
// + HNSW). descriptions.fts is a generated tsvector replacing FTS5. Path
// columns (md_path/html_path/jpg_path/json_path) are gone (Phase 1.5
// cutover landed those as no-ops). Requires the `vector` extension —
// caller bootstraps the database via:
//
//   createdb ragotogar
//   psql ragotogar -c 'CREATE EXTENSION vector'
//
// v4 adds descriptions.vantage and descriptions.ground_truth — prose fields
// describing the camera POV and visible counts. Both feed the generated fts
// column so keyword search hits "from a balcony" / "two people" queries.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS photos (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    file_path     TEXT,
    file_basename TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
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
    focal_length_mm       DOUBLE PRECISION,
    focal_length_35mm     DOUBLE PRECISION,
    f_number              DOUBLE PRECISION,
    exposure_time_seconds DOUBLE PRECISION,
    iso                   INTEGER,
    exposure_compensation DOUBLE PRECISION,
    exposure_mode         TEXT,
    metering_mode         TEXT,
    white_balance         TEXT,
    flash                 TEXT,
    image_width           INTEGER,
    image_height          INTEGER,
    gps_latitude          DOUBLE PRECISION,
    gps_longitude         DOUBLE PRECISION,
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

-- Generated tsvector replaces SQLite FTS5 — same recall, native to the JOINs
-- the search path uses. Indexed for keyword queries via @@.
CREATE TABLE IF NOT EXISTS descriptions (
    photo_id          TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    subject           TEXT,
    setting           TEXT,
    light             TEXT,
    colors            TEXT,
    composition       TEXT,
    vantage           TEXT,
    ground_truth      TEXT,
    full_description  TEXT,
    fts               tsvector GENERATED ALWAYS AS (
                        to_tsvector('english',
                          coalesce(subject,'')          || ' ' ||
                          coalesce(setting,'')          || ' ' ||
                          coalesce(light,'')            || ' ' ||
                          coalesce(colors,'')           || ' ' ||
                          coalesce(composition,'')      || ' ' ||
                          coalesce(vantage,'')          || ' ' ||
                          coalesce(ground_truth,'')     || ' ' ||
                          coalesce(full_description,''))
                      ) STORED
);
CREATE INDEX IF NOT EXISTS idx_descriptions_fts ON descriptions USING gin(fts);

CREATE TABLE IF NOT EXISTS thumbnails (
    photo_id   TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    bytes      BYTEA NOT NULL,
    width      INTEGER NOT NULL DEFAULT 1024,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS inference (
    photo_id     TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    raw_response TEXT,
    model        TEXT,
    preview_ms   INTEGER,
    inference_ms INTEGER,
    described_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Vector chunks table: one row per chunk per photo. nomic-embed-text-v1.5
-- output dim is 768. HNSW index for similarity (vector_cosine_ops matches
-- the <=> distance operator the search path uses).
CREATE TABLE IF NOT EXISTS chunks (
    photo_id   TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    idx        SMALLINT NOT NULL,
    text       TEXT NOT NULL,
    embedding  vector(768) NOT NULL,
    PRIMARY KEY (photo_id, idx)
);
CREATE INDEX IF NOT EXISTS idx_chunks_embedding ON chunks USING hnsw (embedding vector_cosine_ops);
`
