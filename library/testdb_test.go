package library

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

// adminDSN points at the maintenance database (`postgres`) so tests can
// CREATE/DROP transient databases. Mirrors cmd/describe/db_test.go's helper —
// duplicated rather than imported because cmd/describe is its own Go module.
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

// newTempDB creates a uniquely-named Postgres database, applies the v7
// schema, and returns an open connection. Cleanup drops the DB on test exit.
// Skips (rather than fails) when no Postgres is reachable so the suite still
// runs in environments where the user hasn't bootstrapped local Postgres.
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
	name := "ragotogar_libtest_" + hex.EncodeToString(rnd)
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

// testSchemaSQL is the minimum subset of the v7 schema the verify_cache and
// VerifyFilter tests need. The exif / descriptions / classified tables are
// declared empty so LoadPhoto's LEFT JOINs return NULLs cleanly without the
// caller faking column data. The full schema (chunks, FTS, all the EXIF
// indexes) lives in cmd/describe/schema.go — duplicated here only because
// cmd/describe is its own Go module.
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
    artist                TEXT
);

CREATE TABLE descriptions (
    photo_id          TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    subject           TEXT,
    setting           TEXT,
    light             TEXT,
    colors            TEXT,
    composition       TEXT,
    vantage           TEXT,
    ground_truth      TEXT,
    full_description  TEXT
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
    color_palette         TEXT
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
