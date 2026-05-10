package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// adminDSN points at the maintenance database so tests can CREATE/DROP
// transient databases. Mirrors library/testdb_test.go's helper —
// duplicated rather than imported because cmd/edge_build is its own
// package main and Go's test infrastructure doesn't share between
// packages.
func adminDSN(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("TEST_LIBRARY_DSN"); v != "" {
		return v
	}
	if v := os.Getenv("LIBRARY_DSN"); v != "" {
		return rewriteDBName(v, "postgres")
	}
	return "postgres:///postgres"
}

func rewriteDBName(dsn, newDB string) string {
	idx := strings.LastIndex(dsn, "/")
	if idx < 0 || idx == len(dsn)-1 {
		return dsn + newDB
	}
	return dsn[:idx+1] + newDB
}

// newTempDB creates a uniquely-named Postgres database, applies the
// minimal schema cmd/edge_build's queries depend on, and returns an
// open connection. Cleanup drops the DB on test exit. Skips (rather
// than fails) when no Postgres is reachable so the suite still runs
// in environments where the user hasn't bootstrapped local Postgres.
func newTempDB(t *testing.T) *sql.DB {
	t.Helper()
	admin, err := sql.Open("pgx", adminDSN(t))
	if err != nil {
		t.Skipf("cannot reach Postgres for tests: %v (run ./scripts/bootstrap.sh)", err)
	}
	if err := admin.Ping(); err != nil {
		admin.Close()
		t.Skipf("cannot reach Postgres for tests: %v (run ./scripts/bootstrap.sh)", err)
	}
	defer admin.Close()

	rnd := make([]byte, 6)
	rand.Read(rnd)
	name := "ragotogar_edgebuild_test_" + hex.EncodeToString(rnd)
	if _, err := admin.Exec(fmt.Sprintf("CREATE DATABASE %s", name)); err != nil {
		t.Fatalf("create test db: %v", err)
	}

	dsn := rewriteDBName(adminDSN(t), name)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		dropTestDB(adminDSN(t), name)
		t.Fatalf("open test db: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		dropTestDB(adminDSN(t), name)
		t.Fatalf("ping test db: %v", err)
	}

	if _, err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		db.Close()
		dropTestDB(adminDSN(t), name)
		t.Skipf("vector extension not available: %v (run ./scripts/bootstrap.sh)", err)
	}
	if _, err := db.Exec(testSchemaSQL); err != nil {
		db.Close()
		dropTestDB(adminDSN(t), name)
		t.Fatalf("apply test schema: %v", err)
	}

	t.Cleanup(func() {
		db.Close()
		dropTestDB(adminDSN(t), name)
	})
	return db
}

func dropTestDB(adminDSNStr, name string) {
	admin, err := sql.Open("pgx", adminDSNStr)
	if err != nil {
		return
	}
	defer admin.Close()
	admin.Exec(fmt.Sprintf(
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s'", name,
	))
	admin.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", name))
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
