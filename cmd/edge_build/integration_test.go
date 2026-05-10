package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestLoadIDSpace_BytewiseOrder verifies that loadIDSpace's SQL sort
// uses COLLATE "C" — i.e., byte-wise lex regardless of pg's
// lc_collate. The Names slice must be byte-sorted so:
//  1. corpus_hash is stable across hosts with different default
//     collations
//  2. compact_id assignment matches the within-lexeme posting list
//     ordering buildFSTAndPostings produces (which also uses
//     COLLATE "C")
//
// On a typical macOS dev install with en_US.UTF-8 default collation,
// pg orders mixed-case + punctuation differently than byte-wise. This
// test catches a regression that drops COLLATE "C" from the SQL.
func TestLoadIDSpace_BytewiseOrder(t *testing.T) {
	db := newTempDB(t)

	// Names with mixed case + punctuation — typical en_US.UTF-8
	// collation reorders these by language rules ("alpha" sorts near
	// "ALPHA" in locale-aware sort, but byte-wise puts all
	// uppercase before lowercase since 0x41 < 0x61).
	insert := []string{"alpha", "Alpha", "ALPHA", "0_zero", "_under", "a1b", "A1B"}
	for _, n := range insert {
		if _, err := db.Exec(`INSERT INTO photos (id, name) VALUES ($1, $1)`, n); err != nil {
			t.Fatalf("insert %s: %v", n, err)
		}
	}

	ids, err := loadIDSpace(db)
	if err != nil {
		t.Fatalf("loadIDSpace: %v", err)
	}
	if len(ids.Names) != len(insert) {
		t.Fatalf("got %d names, want %d", len(ids.Names), len(insert))
	}

	// Names slice must be strictly byte-ascending. Equivalent to
	// `sort.StringsAreSorted(ids.Names)` but emits a diagnostic
	// pointing at the offending pair on failure.
	for i := 1; i < len(ids.Names); i++ {
		if ids.Names[i-1] >= ids.Names[i] {
			t.Errorf("not byte-sorted at index %d: %q >= %q (bytes %v vs %v)",
				i, ids.Names[i-1], ids.Names[i],
				[]byte(ids.Names[i-1]), []byte(ids.Names[i]))
		}
	}

	// Cross-check that the order DOES diverge from pg's locale-default
	// — otherwise the test isn't actually exercising the collation
	// difference and might give a false sense of coverage if the host
	// happens to have C as default collation.
	rows, err := db.Query(`SELECT name FROM photos ORDER BY name`)
	if err != nil {
		t.Fatalf("locale-default query: %v", err)
	}
	defer rows.Close()
	var localeOrder []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		localeOrder = append(localeOrder, n)
	}
	if reflect.DeepEqual(localeOrder, ids.Names) {
		t.Logf("note: this host's default collation already matches byte-wise — fixture didn't exercise the divergence (the test still asserts byte-sort correctness, just doesn't differentiate WITH and WITHOUT the fix)")
	} else {
		t.Logf("verified: pg's default collation reorders these names — locale=%v vs byte=%v", localeOrder, ids.Names)
	}
}

// TestBuildFSTAndPostings_LiveDB exercises the SQL → fstWriter
// pipeline against a real pg fixture. The unit tests in fst_test.go
// only feed pre-sorted Go slices to the writer; they can't catch
// collation issues in the SQL ORDER BY clause.
//
// This test would have caught the +0.33/wooden bug — pg's default
// collation ordered punctuation-leading lexemes differently than
// byte-wise lex, tripping fstWriter's eager order check the moment
// the user ran the build against their actual library. The unit
// tests passed because they bypassed pg entirely.
//
// Lesson: any function whose contract spans a pg query and a Go
// consumer with strict input requirements (here: byte-sorted) needs
// at least one test that runs the actual SQL.
func TestBuildFSTAndPostings_LiveDB(t *testing.T) {
	db := newTempDB(t)

	// Adversarial fixture: prose with mixed-case words and punctuation
	// that pg's English tokenizer keeps as lexemes. Whether specific
	// punctuation-leading tokens (`+0.33`, `/wooden`) survive the
	// parser depends on pg version / config — but ordinary words like
	// "ABC" and "abc" exercise the same en_US-vs-byte ordering
	// difference (uppercase byte 0x41-0x5A precedes lowercase 0x61-0x7A
	// while locale sort interleaves cases).
	seed := func(name, subject string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO photos (id, name) VALUES ($1, $1)`, name); err != nil {
			t.Fatalf("insert photo %s: %v", name, err)
		}
		if _, err := db.Exec(`INSERT INTO descriptions (photo_id, subject) VALUES ($1, $2)`, name, subject); err != nil {
			t.Fatalf("insert description %s: %v", name, err)
		}
	}

	seed("p1", "exposure was +0.33 EV with /wooden chair")
	seed("p2", "another shot at +0.5 EV setting")
	seed("p3", "ABC abc XYZ xyz mixed case probe")
	seed("p4", "Z_terminal _under-Score 0digit-leading")

	ids, err := loadIDSpace(db)
	if err != nil {
		t.Fatalf("loadIDSpace: %v", err)
	}
	if len(ids.Names) != 4 {
		t.Fatalf("idSpace count = %d, want 4", len(ids.Names))
	}

	dir := t.TempDir()
	fstPath := filepath.Join(dir, "terms.fst")
	postPath := filepath.Join(dir, "postings.bin")
	stats, err := buildFSTAndPostings(db, ids, fstPath, postPath)
	if err != nil {
		t.Fatalf(`buildFSTAndPostings: %v
this fails without COLLATE "C" on the SQL ORDER BY when pg's
lc_collate is non-C (typical en_US.UTF-8 on macOS)`, err)
	}
	if stats.UniqueTerms == 0 {
		t.Fatalf("expected lexemes from fixture, got 0 unique terms")
	}
	if stats.TotalPostings == 0 {
		t.Fatalf("expected postings, got 0")
	}
	t.Logf("built FST: %d terms, %d postings", stats.UniqueTerms, stats.TotalPostings)
}
