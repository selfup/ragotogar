package main

import (
	"database/sql"
	"testing"
	"time"
)

func mustTime(rfc string) sql.NullTime {
	tm, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		panic(err)
	}
	return sql.NullTime{Time: tm, Valid: true}
}

func TestCorpusHash_Determinism(t *testing.T) {
	names := []string{"alpha", "beta", "gamma"}
	desc := mustTime("2026-05-10T12:00:00Z")
	cls := mustTime("2026-05-10T13:00:00Z")
	h1 := corpusHash(names, desc, cls)
	h2 := corpusHash(names, desc, cls)
	if h1 != h2 {
		t.Fatalf("non-deterministic: %s vs %s", h1, h2)
	}
	// Sha256 hex is 64 chars; sanity check the shape.
	if len(h1) != 64 {
		t.Errorf("hash length = %d, want 64", len(h1))
	}
}

func TestCorpusHash_NameSensitivity(t *testing.T) {
	desc := mustTime("2026-05-10T12:00:00Z")
	cls := mustTime("2026-05-10T13:00:00Z")
	h1 := corpusHash([]string{"a", "b"}, desc, cls)
	h2 := corpusHash([]string{"a", "c"}, desc, cls)
	if h1 == h2 {
		t.Fatal("hash unchanged across name diff")
	}
}

func TestCorpusHash_NameOrderSensitivity(t *testing.T) {
	desc := mustTime("2026-05-10T12:00:00Z")
	cls := mustTime("2026-05-10T13:00:00Z")
	h1 := corpusHash([]string{"a", "b"}, desc, cls)
	h2 := corpusHash([]string{"b", "a"}, desc, cls)
	if h1 == h2 {
		t.Fatal("hash unchanged across name reorder — corpus_hash must capture id_space order")
	}
}

func TestCorpusHash_DescribedTimestampSensitivity(t *testing.T) {
	names := []string{"a"}
	cls := mustTime("2026-05-10T13:00:00Z")
	h1 := corpusHash(names, mustTime("2026-05-10T12:00:00Z"), cls)
	h2 := corpusHash(names, mustTime("2026-05-10T12:00:01Z"), cls)
	if h1 == h2 {
		t.Fatal("hash unchanged across described_at diff")
	}
}

func TestCorpusHash_ClassifiedTimestampSensitivity(t *testing.T) {
	names := []string{"a"}
	desc := mustTime("2026-05-10T12:00:00Z")
	h1 := corpusHash(names, desc, mustTime("2026-05-10T13:00:00Z"))
	h2 := corpusHash(names, desc, mustTime("2026-05-10T13:00:01Z"))
	if h1 == h2 {
		t.Fatal("hash unchanged across classified_at diff")
	}
}

func TestCorpusHash_NullVsValid(t *testing.T) {
	names := []string{"a"}
	cls := mustTime("2026-05-10T13:00:00Z")
	var nullDesc sql.NullTime
	h1 := corpusHash(names, nullDesc, cls)
	h2 := corpusHash(names, mustTime("2026-05-10T12:00:00Z"), cls)
	if h1 == h2 {
		t.Fatal("hash didn't distinguish null from valid described_at")
	}
}

func TestCorpusHash_TimestampFieldsNotSwappable(t *testing.T) {
	// A described_at value at time T must not produce the same hash as
	// a classified_at value at the same time T. The "D:" / "C:" prefix
	// in the hash function exists for this.
	names := []string{"a"}
	t1 := mustTime("2026-05-10T12:00:00Z")
	t2 := mustTime("2026-05-10T13:00:00Z")
	h1 := corpusHash(names, t1, t2)
	h2 := corpusHash(names, t2, t1)
	if h1 == h2 {
		t.Fatal("hash didn't distinguish swapped described_at / classified_at")
	}
}

// Separator-byte guard: the 0x00 between names prevents adjacent-name
// concatenation from colliding with a different split.
func TestCorpusHash_SeparatorGuard(t *testing.T) {
	desc := mustTime("2026-05-10T12:00:00Z")
	cls := mustTime("2026-05-10T13:00:00Z")
	h1 := corpusHash([]string{"ab", "c"}, desc, cls)
	h2 := corpusHash([]string{"a", "bc"}, desc, cls)
	if h1 == h2 {
		t.Fatal("separator collision: 'ab'+'c' hashed same as 'a'+'bc'")
	}
}

func TestCorpusHash_EmptyNames(t *testing.T) {
	desc := mustTime("2026-05-10T12:00:00Z")
	cls := mustTime("2026-05-10T13:00:00Z")
	h := corpusHash(nil, desc, cls)
	if len(h) != 64 {
		t.Errorf("empty names hash length = %d, want 64", len(h))
	}
	// Determinism check too.
	h2 := corpusHash([]string{}, desc, cls)
	if h != h2 {
		t.Fatalf("nil names vs empty slice produced different hashes: %s vs %s", h, h2)
	}
}
