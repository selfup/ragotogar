package library

import (
	"database/sql"
	"testing"

	"ragotogar/library/testdb"
)

// newTempDB is a library-package convenience wrapper around testdb.New that
// applies the v12+ schema this package's pg-integration tests assume. The
// schema string lives here (not in the shared package) because each consumer
// has its own subset of the production schema — library's tests need exif,
// descriptions, query_generations, classified, inference, verify_cache; they
// don't need the photo_descriptions / photo_metadata / photo_queries vector
// stores (those are exercised in cmd/index / cmd/edge_build tests).
func newTempDB(t *testing.T) *sql.DB {
	t.Helper()
	return testdb.New(t, "lib", testdb.SchemaSQL(testSchemaSQL))
}

// testSchemaSQL is the minimum subset of the v7+ schema the verify_cache and
// VerifyFilter tests need. The exif / descriptions / classified tables are
// declared empty so LoadPhoto's LEFT JOINs return NULLs cleanly without the
// caller faking column data. The full schema authority is cmd/describe/schema.go;
// this duplicate exists because cmd/describe is a separate Go module and Go's
// test infrastructure doesn't share const declarations across modules.
const testSchemaSQL = `
CREATE TABLE photos (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    file_path     TEXT,
    file_basename TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE exif (
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
    white_balance         TEXT,
    flash                 TEXT,
    software              TEXT,
    artist                TEXT,
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
CREATE INDEX idx_exif_fts ON exif USING gin(fts);

CREATE TABLE descriptions (
    photo_id          TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    subject           TEXT,
    setting           TEXT,
    light             TEXT,
    colors            TEXT,
    composition       TEXT,
    vantage           TEXT,
    ground_truth      TEXT,
    condition         TEXT,
    mood              TEXT,
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
                          coalesce(mood,'')             || ' ' ||
                          coalesce(full_description,''))
                      ) STORED
);
CREATE INDEX idx_descriptions_fts ON descriptions USING gin(fts);

-- v12: query_generations is the source-of-truth for LLM-generated search
-- phrasings. LoadPhoto LEFT JOINs against it; if no row exists the
-- queries column scans as nil.
CREATE TABLE query_generations (
    photo_id        TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    schema_version  INTEGER NOT NULL,
    model           TEXT NOT NULL,
    prompt_hash     TEXT NOT NULL,
    queries         JSONB NOT NULL,
    generated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE classified (
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

CREATE TABLE inference (
    photo_id     TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    raw_response TEXT,
    model        TEXT,
    preview_ms   INTEGER,
    inference_ms INTEGER,
    described_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE verify_cache (
    query         TEXT NOT NULL,
    photo_id      TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    verify_model  TEXT NOT NULL,
    verdict       BOOLEAN NOT NULL,
    verified_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (query, photo_id, verify_model)
);
CREATE INDEX idx_verify_cache_query ON verify_cache(query, verify_model);

CREATE TABLE classify_filter_cache (
    nl_query        TEXT NOT NULL,
    photo_id        TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    classify_model  TEXT NOT NULL,
    drop_verdict    BOOLEAN NOT NULL,
    filtered_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (nl_query, photo_id, classify_model)
);
`

// seedPhoto inserts a photos + inference row pair so verify_cache rows can be
// inserted (FK) and the freshness check has a described_at to compare against.
// Returns the photo name (== id) for the caller to thread through assertions.
func seedPhoto(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO photos (id, name) VALUES ($1, $2)`, name, name); err != nil {
		t.Fatalf("seed photo %s: %v", name, err)
	}
	if _, err := db.Exec(`INSERT INTO inference (photo_id, model) VALUES ($1, 'test-model')`, name); err != nil {
		t.Fatalf("seed inference %s: %v", name, err)
	}
	return name
}
