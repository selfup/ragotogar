package main

// schemaSQL is applied on every cmd/describe run via CREATE TABLE IF NOT
// EXISTS — idempotent on existing libraries, creates the schema fresh on
// an empty Postgres database.
//
// cmd/describe is the schema authority. cmd/web and the Python tools open
// the DB read-only (or with simple INSERTs that don't touch DDL).
//
// Phase 2 schema: Postgres + pgvector. The chunks table is new (halfvec(2560)
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
//
// v5 adds the classified table — typed enum fields produced by cmd/classify
// from the description prose. Lets queries like "from a plane on the ground"
// become exact predicates (pov_container='from_plane' AND pov_altitude='ground')
// instead of fuzzy text matches.
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
    software              TEXT,
    -- v8: generated tsvector over high-signal text columns. Lets FTS+vector
    -- match queries like "2024", "X100VI", "Lightroom" against metadata that
    -- the descriptions.fts column never sees. Skips the generic-valued
    -- columns (exposure_mode, white_balance, flash) on purpose — values like
    -- "Auto" or "Did not fire" would drown rank signal otherwise.
    fts                   tsvector GENERATED ALWAYS AS (
                            to_tsvector('english',
                              coalesce(camera_make,'')              || ' ' ||
                              coalesce(camera_model,'')             || ' ' ||
                              coalesce(lens_model,'')               || ' ' ||
                              coalesce(lens_info,'')                || ' ' ||
                              coalesce(date_taken_year::text,'')    || ' ' ||
                              coalesce(software,'')                 || ' ' ||
                              coalesce(artist,''))
                          ) STORED
);
CREATE INDEX IF NOT EXISTS idx_exif_camera     ON exif(camera_model);
CREATE INDEX IF NOT EXISTS idx_exif_make       ON exif(camera_make);
CREATE INDEX IF NOT EXISTS idx_exif_date       ON exif(date_taken);
CREATE INDEX IF NOT EXISTS idx_exif_year_month ON exif(date_taken_year, date_taken_month);
CREATE INDEX IF NOT EXISTS idx_exif_iso        ON exif(iso);
CREATE INDEX IF NOT EXISTS idx_exif_aperture   ON exif(f_number);
CREATE INDEX IF NOT EXISTS idx_exif_focal      ON exif(focal_length_mm);
CREATE INDEX IF NOT EXISTS idx_exif_artist     ON exif(artist);
CREATE INDEX IF NOT EXISTS idx_exif_fts        ON exif USING gin(fts);

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
    condition         TEXT,
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
                          coalesce(condition,'')        || ' ' ||
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

-- v5: typed enum fields produced from description prose by cmd/classify.
-- Scalar columns get btree indexes for cheap WHERE filters; array columns
-- (subject_category, framing) get GIN for "contains" queries. classifier_model
-- records which LLM produced the row so re-classifying with a stronger model
-- is identifiable. extras JSONB is the forward-compat escape hatch for new
-- enums that haven't been promoted to columns yet.
CREATE TABLE IF NOT EXISTS classified (
    photo_id              TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    pov_container         TEXT,
    pov_altitude          TEXT,
    pov_angle             TEXT,
    subject_altitude      TEXT,
    subject_category      TEXT[],
    subject_distance      TEXT,
    subject_count         TEXT,
    animal_count          TEXT,
    scene_time_of_day     TEXT,
    scene_indoor_outdoor  TEXT,
    scene_weather         TEXT,
    framing               TEXT[],
    motion                TEXT,
    color_palette         TEXT,
    classified_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    classifier_model      TEXT NOT NULL,
    extras                JSONB
);
CREATE INDEX IF NOT EXISTS idx_classified_pov_container  ON classified(pov_container);
CREATE INDEX IF NOT EXISTS idx_classified_pov_altitude   ON classified(pov_altitude);
CREATE INDEX IF NOT EXISTS idx_classified_pov_angle      ON classified(pov_angle);
CREATE INDEX IF NOT EXISTS idx_classified_subject_alt    ON classified(subject_altitude);
CREATE INDEX IF NOT EXISTS idx_classified_subject_dist   ON classified(subject_distance);
CREATE INDEX IF NOT EXISTS idx_classified_subject_count  ON classified(subject_count);
CREATE INDEX IF NOT EXISTS idx_classified_animal_count   ON classified(animal_count);
CREATE INDEX IF NOT EXISTS idx_classified_time_of_day    ON classified(scene_time_of_day);
CREATE INDEX IF NOT EXISTS idx_classified_indoor         ON classified(scene_indoor_outdoor);
CREATE INDEX IF NOT EXISTS idx_classified_weather        ON classified(scene_weather);
CREATE INDEX IF NOT EXISTS idx_classified_motion         ON classified(motion);
CREATE INDEX IF NOT EXISTS idx_classified_palette        ON classified(color_palette);
CREATE INDEX IF NOT EXISTS idx_classified_subject_cat    ON classified USING gin(subject_category);
CREATE INDEX IF NOT EXISTS idx_classified_framing        ON classified USING gin(framing);

-- Vector chunks table: one row per chunk per photo. Qwen3-Embedding-4B
-- output dim is 2560. halfvec (16-bit float) keeps storage reasonable and is
-- the only HNSW-viable type at this dim — pgvector caps the vector type's
-- HNSW at 2000 dims, halfvec at 4000. halfvec_cosine_ops matches the <=>
-- distance operator the search path uses.
CREATE TABLE IF NOT EXISTS chunks (
    photo_id   TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    idx        SMALLINT NOT NULL,
    text       TEXT NOT NULL,
    embedding  halfvec(2560) NOT NULL,
    PRIMARY KEY (photo_id, idx)
);
CREATE INDEX IF NOT EXISTS idx_chunks_embedding ON chunks USING hnsw (embedding halfvec_cosine_ops);

-- v7: persistent cache for the LLM yes/no verify pass. Lookup is
-- (query, photo_id, verify_model); the verify_model column means swapping
-- SEARCH_MODEL (e.g. ministral-3-3b → devstral-small-2-2512) doesn't poison
-- cached verdicts from a different model. Stale rows are filtered at lookup
-- time by comparing verified_at against inference.described_at — re-describing
-- a photo bypasses any older cached verdict without an explicit invalidation.
CREATE TABLE IF NOT EXISTS verify_cache (
    query         TEXT NOT NULL,
    photo_id      TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    verify_model  TEXT NOT NULL,
    verdict       BOOLEAN NOT NULL,
    verified_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (query, photo_id, verify_model)
);
CREATE INDEX IF NOT EXISTS idx_verify_cache_query ON verify_cache(query, verify_model);

-- query_rewrite_cache stores LLM-rewritten boolean queries keyed on the
-- canonical (lowercased / trimmed) natural-language input. Hit on repeat
-- queries skips the LLM round-trip entirely; misses fall through to a live
-- LLMComplete call. PK includes rewrite_model so swapping the rewrite model
-- doesn't cross-contaminate cached output from a different vocabulary or
-- capability tier.
CREATE TABLE IF NOT EXISTS query_rewrite_cache (
    nl_query       TEXT NOT NULL,
    rewrite_model  TEXT NOT NULL,
    rewritten      TEXT NOT NULL,
    rewritten_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (nl_query, rewrite_model)
);

-- classify_filter_cache stores saved drop/keep verdicts from the post-
-- retrieval classifier-aware LLM filter. One row per (canonical NL query,
-- photo_id, classify_model). The filter takes a candidate's classifier
-- enums plus the user's NL request and decides whether the verdict
-- contradicts the request. Stored verdicts are checked for freshness
-- against classified.classified_at at lookup — re-classifying a photo
-- silently invalidates older verdicts.
CREATE TABLE IF NOT EXISTS classify_filter_cache (
    nl_query        TEXT NOT NULL,
    photo_id        TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    classify_model  TEXT NOT NULL,
    drop_verdict    BOOLEAN NOT NULL,
    filtered_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (nl_query, photo_id, classify_model)
);
`
