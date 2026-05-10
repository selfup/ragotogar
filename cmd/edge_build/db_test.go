package main

import (
	"database/sql"
	"testing"

	"ragotogar/library/testdb"
)

// newTempDB is the cmd/edge_build convenience wrapper around testdb.New.
// The schema lives in this file (not in the shared package) because each
// consumer needs a different subset of the production schema —
// cmd/edge_build's queries hit photos, descriptions, exif, classified,
// inference, the three v12 vector stores, and query_generations.
func newTempDB(t *testing.T) *sql.DB {
	t.Helper()
	return testdb.New(t, "edge_build", testdb.SchemaSQL(testSchemaSQL))
}

// testSchemaSQL is the minimum subset of the v12+ schema cmd/edge_build
// queries against. Only the columns the build actually reads are
// declared — no FTS indexes, no FK indexes, no migrations table. The
// full schema authority is cmd/describe/schema.go.
//
// Generated `fts` columns on descriptions / exif use to_tsvector('english')
// — same config the production schema uses, so the lexemes pg keeps
// here match the lexemes the build reads from a real library.
const testSchemaSQL = `
CREATE TABLE photos (
    id   TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE descriptions (
    photo_id         TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    subject          TEXT,
    full_description TEXT,
    mood             TEXT,
    fts              tsvector GENERATED ALWAYS AS (
                       to_tsvector('english',
                         coalesce(subject,'')          || ' ' ||
                         coalesce(full_description,'') || ' ' ||
                         coalesce(mood,''))
                     ) STORED
);

CREATE TABLE exif (
    photo_id      TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    camera_make   TEXT,
    camera_model  TEXT,
    fts           tsvector GENERATED ALWAYS AS (
                    to_tsvector('english',
                      coalesce(camera_make,'') || ' ' ||
                      coalesce(camera_model,''))
                  ) STORED
);

CREATE TABLE classified (
    photo_id              TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    classified_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    subject_altitude      TEXT,
    scene_indoor_outdoor  TEXT,
    scene_time_of_day     TEXT,
    scene_weather         TEXT,
    pov_container         TEXT
);

CREATE TABLE inference (
    photo_id     TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    described_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE photo_descriptions (
    photo_id        TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    schema_version  INTEGER NOT NULL,
    chunk_index     INTEGER NOT NULL,
    chunk_text      TEXT NOT NULL,
    embedding       halfvec(2560) NOT NULL,
    UNIQUE (photo_id, schema_version, chunk_index)
);

CREATE TABLE photo_metadata (
    photo_id        TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    schema_version  INTEGER NOT NULL,
    metadata_text   TEXT NOT NULL,
    embedding       halfvec(2560) NOT NULL,
    UNIQUE (photo_id, schema_version)
);

CREATE TABLE photo_queries (
    photo_id        TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    schema_version  INTEGER NOT NULL,
    query_index     INTEGER NOT NULL,
    query_text      TEXT NOT NULL,
    embedding       halfvec(2560) NOT NULL,
    UNIQUE (photo_id, schema_version, query_index)
);

CREATE TABLE query_generations (
    photo_id       TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    schema_version INTEGER NOT NULL,
    model          TEXT NOT NULL,
    prompt_hash    TEXT NOT NULL,
    queries        JSONB NOT NULL
);
`
