// Package testdb is a lifecycle helper for transient Postgres test databases
// shared across every module's _test.go files. Each call to New creates a
// uniquely-named database, enables the pgvector extension, applies a
// caller-supplied schema, and registers a t.Cleanup hook that drops the
// database on test exit.
//
// Before consolidation, four packages (library, cmd/describe, cmd/edge_build,
// cmd/web) each duplicated this exact lifecycle code with subtle divergences.
// That violated DRY but also meant a fix to one (e.g. the 46de583 collation
// COLLATE "C" lesson) didn't automatically propagate to the others. Now there
// is a single implementation; each consumer supplies only its own schema.
//
// Postgres unavailability skips (not fails) the test so the suite stays green
// on machines that haven't run ./scripts/bootstrap.sh.
package testdb

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

// AdminDSN returns the DSN of the maintenance database tests use to issue
// cluster-level DDL (CREATE/DROP DATABASE).
//
// Resolution order:
//  1. TEST_LIBRARY_DSN — explicit override; used verbatim.
//  2. LIBRARY_DSN — production DSN; the dbname is rewritten to "postgres".
//  3. "postgres:///postgres" — local-socket fallback.
func AdminDSN(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("TEST_LIBRARY_DSN"); v != "" {
		return v
	}
	if v := os.Getenv("LIBRARY_DSN"); v != "" {
		return RewriteDBName(v, "postgres")
	}
	return "postgres:///postgres"
}

// RewriteDBName swaps the dbname segment of a DSN with newDB. Handles the two
// shapes ragotogar uses: postgres:///dbname (path-style) and
// postgres://host:port/dbname (URL-style). DSNs with query strings aren't
// supported — the project doesn't use them.
func RewriteDBName(dsn, newDB string) string {
	idx := strings.LastIndex(dsn, "/")
	if idx < 0 || idx == len(dsn)-1 {
		return dsn + newDB
	}
	return dsn[:idx+1] + newDB
}

// ApplySchema initializes the schema in a freshly-created test database. New
// invokes it after enabling the pgvector extension. Two common shapes:
//
//	db := testdb.New(t, "lib", testdb.SchemaSQL(myDDL))
//	db := testdb.New(t, "describe", func(db *sql.DB) error { return initSchema(db) })
type ApplySchema func(*sql.DB) error

// SchemaSQL adapts a static DDL string into an ApplySchema for callers that
// don't need a function callback.
func SchemaSQL(ddl string) ApplySchema {
	return func(db *sql.DB) error {
		_, err := db.Exec(ddl)
		return err
	}
}

// New creates a uniquely-named transient Postgres database, enables pgvector,
// applies the supplied schema, and returns the open *sql.DB. Cleanup drops
// the database on test exit.
//
// Skips (not fails) the test when:
//   - Postgres is unreachable.
//   - pgvector is not installed.
//
// prefix is interpolated into the database name so a `psql -l` listing during
// a stuck test makes the originator obvious (e.g. ragotogar_lib_test_a1b2c3,
// ragotogar_describe_test_…, ragotogar_edge_build_test_…).
func New(t *testing.T, prefix string, schema ApplySchema) *sql.DB {
	t.Helper()
	db, _ := NewWithDSN(t, prefix, schema)
	return db
}

// NewWithDSN is New + the DSN of the freshly-created database. Use when the
// caller needs to pass the DSN to a function that opens its own connection
// (e.g. cmd/search's run() takes a DSN string). The *sql.DB is still owned
// by t.Cleanup; the DSN is purely informational.
func NewWithDSN(t *testing.T, prefix string, schema ApplySchema) (*sql.DB, string) {
	t.Helper()

	admin, err := sql.Open("pgx", AdminDSN(t))
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
	name := fmt.Sprintf("ragotogar_%s_test_%s", prefix, hex.EncodeToString(rnd))
	if _, err := admin.Exec(fmt.Sprintf("CREATE DATABASE %s", name)); err != nil {
		t.Fatalf("create test db: %v", err)
	}

	dsn := RewriteDBName(AdminDSN(t), name)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		Drop(AdminDSN(t), name)
		t.Fatalf("open test db: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		Drop(AdminDSN(t), name)
		t.Fatalf("ping test db: %v", err)
	}

	if _, err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		db.Close()
		Drop(AdminDSN(t), name)
		t.Skipf("vector extension not available: %v (run ./scripts/bootstrap.sh)", err)
	}

	if schema != nil {
		if err := schema(db); err != nil {
			db.Close()
			Drop(AdminDSN(t), name)
			t.Fatalf("apply schema: %v", err)
		}
	}

	t.Cleanup(func() {
		db.Close()
		Drop(AdminDSN(t), name)
	})
	return db, dsn
}

// Drop terminates any backends attached to name and drops the database.
// Best-effort: errors are swallowed so cleanup paths can call it without
// extra plumbing. Exposed so callers in unusual lifecycle situations can
// invoke it manually; New's t.Cleanup handles the common case.
func Drop(adminDSN, name string) {
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		return
	}
	defer admin.Close()
	admin.Exec(fmt.Sprintf(
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s'", name,
	))
	admin.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", name))
}
