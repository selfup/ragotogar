package main

import (
	"database/sql"
	"testing"

	"ragotogar/library/testdb"
)

// classifyTestSchema is the minimum schema cmd/classify's listTodo touches:
// photos + descriptions + classified. classifier_model is NOT NULL in the
// production schema, so the test schema enforces it too — catches any test
// that forgets to set it.
const classifyTestSchema = `
CREATE TABLE photos (
    id   TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);
CREATE TABLE descriptions (
    photo_id         TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    full_description TEXT
);
CREATE TABLE classified (
    photo_id         TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    classifier_model TEXT NOT NULL,
    classified_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

func newTempDB(t *testing.T) *sql.DB {
	t.Helper()
	return testdb.New(t, "classify", testdb.SchemaSQL(classifyTestSchema))
}

func seedPhotoWithDescription(t *testing.T, db *sql.DB, name, fullDesc string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO photos (id, name) VALUES ($1, $1)`, name); err != nil {
		t.Fatalf("photos %s: %v", name, err)
	}
	if fullDesc != "" {
		if _, err := db.Exec(
			`INSERT INTO descriptions (photo_id, full_description) VALUES ($1, $2)`,
			name, fullDesc,
		); err != nil {
			t.Fatalf("descriptions %s: %v", name, err)
		}
	}
}

func seedClassifiedRow(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO classified (photo_id, classifier_model) VALUES ($1, 'test-classifier')`,
		name,
	); err != nil {
		t.Fatalf("classified %s: %v", name, err)
	}
}

// TestListTodo_EmptyDB: no photos, no work.
func TestListTodo_EmptyDB(t *testing.T) {
	db := newTempDB(t)

	for _, reclassify := range []bool{false, true} {
		todo, err := listTodo(db, reclassify)
		if err != nil {
			t.Fatalf("listTodo(reclassify=%v): %v", reclassify, err)
		}
		if len(todo) != 0 {
			t.Errorf("reclassify=%v: got %v, want empty", reclassify, todo)
		}
	}
}

// TestListTodo_PhotoWithoutDescriptionSkipped: cmd/classify operates on
// description prose; a photo that was organized but never described is
// not in scope regardless of reclassify flag.
func TestListTodo_PhotoWithoutDescriptionSkipped(t *testing.T) {
	db := newTempDB(t)
	seedPhotoWithDescription(t, db, "bare", "") // no descriptions row

	for _, reclassify := range []bool{false, true} {
		todo, err := listTodo(db, reclassify)
		if err != nil {
			t.Fatalf("listTodo(reclassify=%v): %v", reclassify, err)
		}
		if len(todo) != 0 {
			t.Errorf("reclassify=%v: got %v, want empty (no description row)", reclassify, todo)
		}
	}
}

// TestListTodo_PhotoWithNullDescriptionSkipped: the WHERE filter is
// `full_description IS NOT NULL`. A descriptions row with NULL prose is
// out of scope. Distinct from the no-row case above.
func TestListTodo_PhotoWithNullDescriptionSkipped(t *testing.T) {
	db := newTempDB(t)
	if _, err := db.Exec(`INSERT INTO photos (id, name) VALUES ('p', 'p')`); err != nil {
		t.Fatalf("photos: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO descriptions (photo_id, full_description) VALUES ('p', NULL)`); err != nil {
		t.Fatalf("descriptions: %v", err)
	}

	todo, err := listTodo(db, false)
	if err != nil {
		t.Fatalf("listTodo: %v", err)
	}
	if len(todo) != 0 {
		t.Errorf("got %v, want empty (full_description IS NULL)", todo)
	}
}

// TestListTodo_IncrementalReturnsOnlyUnclassified: the default path —
// classify what's new, leave existing alone.
func TestListTodo_IncrementalReturnsOnlyUnclassified(t *testing.T) {
	db := newTempDB(t)
	seedPhotoWithDescription(t, db, "p1", "a description")
	seedPhotoWithDescription(t, db, "p2", "another")
	seedPhotoWithDescription(t, db, "p3", "and another")

	seedClassifiedRow(t, db, "p2") // p2 already classified

	todo, err := listTodo(db, false)
	if err != nil {
		t.Fatalf("listTodo: %v", err)
	}
	want := []string{"p1", "p3"} // ORDER BY photo_id; p2 excluded
	if len(todo) != len(want) {
		t.Fatalf("got %v, want %v", todo, want)
	}
	for i, name := range want {
		if todo[i] != name {
			t.Errorf("todo[%d] = %q, want %q", i, todo[i], name)
		}
	}
}

// TestListTodo_ReclassifyReturnsEveryDescribedPhoto: the rebuild path —
// every photo with a description, regardless of whether it already has a
// classifier verdict. Drives `cmd/classify -reclassify`.
func TestListTodo_ReclassifyReturnsEveryDescribedPhoto(t *testing.T) {
	db := newTempDB(t)
	seedPhotoWithDescription(t, db, "p1", "x")
	seedPhotoWithDescription(t, db, "p2", "y")
	seedPhotoWithDescription(t, db, "p3", "z")
	seedClassifiedRow(t, db, "p2") // p2 already has a classified row

	todo, err := listTodo(db, true)
	if err != nil {
		t.Fatalf("listTodo: %v", err)
	}
	if len(todo) != 3 {
		t.Fatalf("got %v, want 3 names", todo)
	}
	// ORDER BY photo_id: p1, p2, p3
	if todo[0] != "p1" || todo[1] != "p2" || todo[2] != "p3" {
		t.Errorf("got %v, want [p1 p2 p3] (ORDER BY photo_id)", todo)
	}
}

// TestListTodo_OrderedDeterministically: callers (and the parallel worker
// pool in run) rely on a stable order so resumability across runs is
// predictable. ORDER BY photo_id is the contract.
func TestListTodo_OrderedDeterministically(t *testing.T) {
	db := newTempDB(t)
	// Insert in non-lexicographic order to force the ORDER BY to do work.
	for _, name := range []string{"zebra", "alpha", "mango", "blue"} {
		seedPhotoWithDescription(t, db, name, "d")
	}

	todo, err := listTodo(db, true)
	if err != nil {
		t.Fatalf("listTodo: %v", err)
	}
	want := []string{"alpha", "blue", "mango", "zebra"}
	for i, name := range want {
		if todo[i] != name {
			t.Errorf("todo[%d] = %q, want %q (full: %v)", i, todo[i], name, todo)
		}
	}
}
