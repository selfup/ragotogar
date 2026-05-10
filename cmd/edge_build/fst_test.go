package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/blevesearch/vellum"
)

// decodePostingList reads a posting record at offset and returns the
// reconstructed compact-id slice. Mirror of what cmd/edge will do at
// runtime — keeping it here as a test helper documents the exact
// on-disk contract the runtime depends on.
func decodePostingList(t *testing.T, postBytes []byte, offset uint64) []uint32 {
	t.Helper()
	p := postBytes[offset:]
	count, n := binary.Uvarint(p)
	if n <= 0 {
		t.Fatalf("bad count varint at offset %d", offset)
	}
	p = p[n:]

	ids := make([]uint32, 0, count)
	var prev uint32
	for i := range count {
		delta, n := binary.Uvarint(p)
		if n <= 0 {
			t.Fatalf("bad delta varint at posting %d (term offset %d)", i, offset)
		}
		prev += uint32(delta)
		ids = append(ids, prev)
		p = p[n:]
	}
	return ids
}

func newTempFSTWriter(t *testing.T) (*fstWriter, string, string) {
	t.Helper()
	dir := t.TempDir()
	fstPath := filepath.Join(dir, "terms.fst")
	postPath := filepath.Join(dir, "postings.bin")
	w, err := newFSTWriter(fstPath, postPath)
	if err != nil {
		t.Fatalf("newFSTWriter: %v", err)
	}
	return w, fstPath, postPath
}

func TestFSTWriter_RoundTrip(t *testing.T) {
	w, fstPath, postPath := newTempFSTWriter(t)

	pairs := []struct {
		lex string
		cid uint32
	}{
		{"apple", 0},
		{"apple", 5},
		{"apple", 100},
		{"banana", 2},
		{"banana", 3},
		{"cherry", 7},
	}
	for _, p := range pairs {
		if err := w.Add(p.lex, p.cid); err != nil {
			t.Fatalf("Add(%q, %d): %v", p.lex, p.cid, err)
		}
	}
	stats, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	if stats.UniqueTerms != 3 {
		t.Errorf("UniqueTerms = %d, want 3", stats.UniqueTerms)
	}
	if stats.TotalPostings != 6 {
		t.Errorf("TotalPostings = %d, want 6", stats.TotalPostings)
	}

	fst, err := vellum.Open(fstPath)
	if err != nil {
		t.Fatalf("vellum.Open: %v", err)
	}
	defer fst.Close()

	postBytes, err := os.ReadFile(postPath)
	if err != nil {
		t.Fatalf("read postings: %v", err)
	}

	expected := map[string][]uint32{
		"apple":  {0, 5, 100},
		"banana": {2, 3},
		"cherry": {7},
	}
	for term, want := range expected {
		offset, exists, err := fst.Get([]byte(term))
		if err != nil {
			t.Fatalf("Get(%q): %v", term, err)
		}
		if !exists {
			t.Fatalf("Get(%q): exists=false", term)
		}
		got := decodePostingList(t, postBytes, offset)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("postings for %q: got %v, want %v", term, got, want)
		}
	}

	if _, exists, err := fst.Get([]byte("missing")); err != nil {
		t.Fatalf("Get(missing): %v", err)
	} else if exists {
		t.Fatal("Get(missing) returned exists=true")
	}
}

// Same (lexeme, compact_id) pair fed twice should produce one entry —
// posting lists are sets, not multisets. Without this guard a lexeme
// that appears in both descriptions and exif for the same photo would
// double-count.
func TestFSTWriter_DupGuard(t *testing.T) {
	w, fstPath, postPath := newTempFSTWriter(t)

	for _, p := range []struct {
		lex string
		cid uint32
	}{
		{"apple", 0},
		{"apple", 0}, // duplicate
		{"apple", 5},
		{"apple", 5}, // duplicate
	} {
		if err := w.Add(p.lex, p.cid); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	stats, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}

	if stats.TotalPostings != 2 {
		t.Fatalf("TotalPostings = %d, want 2 (dup-guard collapse)", stats.TotalPostings)
	}

	fst, err := vellum.Open(fstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fst.Close()
	postBytes, err := os.ReadFile(postPath)
	if err != nil {
		t.Fatal(err)
	}
	offset, _, _ := fst.Get([]byte("apple"))
	got := decodePostingList(t, postBytes, offset)
	want := []uint32{0, 5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("postings: got %v, want %v", got, want)
	}
}

// fstWriter validates lexeme order eagerly at Add time. Vellum's own
// ErrOutOfOrder would fire only at the next-group flush (or Close),
// after a pile of postings have been written to disk; the eager check
// surfaces the violation at the offending Add.
func TestFSTWriter_UnsortedKeysError(t *testing.T) {
	w, _, _ := newTempFSTWriter(t)
	if err := w.Add("banana", 0); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if err := w.Add("apple", 0); err == nil {
		t.Fatal("expected error on out-of-order lexeme insertion at Add time")
	}
}

// Within-group compact_id ordering matters for delta encoding —
// cid < prevID would silently underflow uint32 and produce a corrupt
// posting list. Eager check surfaces the violation at Add.
func TestFSTWriter_WithinGroupUnsortedCompactID(t *testing.T) {
	w, _, _ := newTempFSTWriter(t)
	if err := w.Add("apple", 5); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if err := w.Add("apple", 3); err == nil {
		t.Fatal("expected error on out-of-order compact_id within a lexeme group")
	}
}

// Empty build (no Add calls) must produce a valid FST with zero terms,
// not crash on Close.
func TestFSTWriter_Empty(t *testing.T) {
	w, fstPath, _ := newTempFSTWriter(t)
	stats, err := w.Close()
	if err != nil {
		t.Fatalf("Close on empty: %v", err)
	}
	if stats.UniqueTerms != 0 || stats.TotalPostings != 0 {
		t.Fatalf("empty stats: %+v", stats)
	}
	fst, err := vellum.Open(fstPath)
	if err != nil {
		t.Fatalf("Open empty FST: %v", err)
	}
	if _, exists, _ := fst.Get([]byte("anything")); exists {
		t.Fatal("empty FST returned exists=true")
	}
	fst.Close()
}

// Compact-id deltas must encode high-cid values correctly. A single
// posting at compact_id=1_000_000 exercises the multi-byte varint
// path.
func TestFSTWriter_LargeCompactID(t *testing.T) {
	w, fstPath, postPath := newTempFSTWriter(t)
	if err := w.Add("term", 1_000_000); err != nil {
		t.Fatal(err)
	}
	if err := w.Add("term", 1_000_005); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	fst, err := vellum.Open(fstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fst.Close()
	postBytes, err := os.ReadFile(postPath)
	if err != nil {
		t.Fatal(err)
	}
	offset, _, _ := fst.Get([]byte("term"))
	got := decodePostingList(t, postBytes, offset)
	want := []uint32{1_000_000, 1_000_005}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
