package main

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Property tests for fstWriter beyond the example coverage in fst_test.go.
// The example tests pin specific shapes (unsorted error, dup-guard, large
// cid); the property tests sweep random-but-sorted inputs to catch
// invariants the examples instantiate.

// genSortedLexemes returns n random alphanumeric strings sorted in
// byte-wise lexicographic order — the exact order vellum requires.
// Adjacent duplicates are deduped so the writer sees one group per lex.
// The duplicate-group case is exercised separately in fst_test.go's
// existing TestFSTWriter_RoundTrip.
func genSortedLexemes(rng *rand.Rand, n int) []string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	raw := make([]string, 0, n)
	for range n {
		l := rng.IntN(5) + 1
		buf := make([]byte, l)
		for i := range buf {
			buf[i] = alphabet[rng.IntN(len(alphabet))]
		}
		raw = append(raw, string(buf))
	}
	sort.Strings(raw)
	out := raw[:0]
	for i, s := range raw {
		if i == 0 || s != raw[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// TestProperty_FSTWriter_SortedInputAlwaysAccepted generates 100 random
// sorted batches of (lexeme, cid) pairs and asserts every Add succeeds.
// Combined with the existing TestFSTWriter_UnsortedKeysError this is the
// full "Adds accept input iff input is sorted" property.
func TestProperty_FSTWriter_SortedInputAlwaysAccepted(t *testing.T) {
	dir := t.TempDir()
	rng := rand.New(rand.NewPCG(1, 2))

	for trial := range 100 {
		lexemes := genSortedLexemes(rng, rng.IntN(50)+5) // 5..55 unique terms
		fstPath := filepath.Join(dir, fmt.Sprintf("fst_%d.bin", trial))
		postPath := filepath.Join(dir, fmt.Sprintf("post_%d.bin", trial))

		w, err := newFSTWriter(fstPath, postPath)
		if err != nil {
			t.Fatalf("trial=%d newFSTWriter: %v", trial, err)
		}

		// lexemes are deduped + sorted, so every iteration opens a fresh
		// group with cids starting at 1.
		expectPostings := 0
		for _, lex := range lexemes {
			nCids := rng.IntN(4) + 1
			for i := range nCids {
				cid := uint32(i + 1)
				if err := w.Add(lex, cid); err != nil {
					t.Fatalf("trial=%d Add(%q,%d): %v (lexemes=%v)", trial, lex, cid, err, lexemes)
				}
				expectPostings++
			}
		}

		stats, err := w.Close()
		if err != nil {
			t.Fatalf("trial=%d Close: %v", trial, err)
		}
		if stats.UniqueTerms != len(lexemes) {
			t.Errorf("trial=%d UniqueTerms = %d, want %d", trial, stats.UniqueTerms, len(lexemes))
		}
		if stats.TotalPostings != expectPostings {
			t.Errorf("trial=%d TotalPostings = %d, want %d", trial, stats.TotalPostings, expectPostings)
		}
	}
}

// TestProperty_FSTWriter_AnyOutOfOrderLexemeRejected: take a sorted
// random sequence; inject one out-of-order lexeme at a random position;
// verify the writer rejects at exactly that Add. The error message
// must name the offending pair so production debug output is actionable
// (this was the load-bearing property from commit 46de583).
func TestProperty_FSTWriter_AnyOutOfOrderLexemeRejected(t *testing.T) {
	dir := t.TempDir()
	rng := rand.New(rand.NewPCG(3, 4))

	for trial := range 50 {
		// Make sure we have at least 2 distinct sorted lexemes so a swap
		// is meaningful.
		lex := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
		sort.Strings(lex) // ["alpha","beta","delta","epsilon","gamma"]
		// Pick a position other than 0 where we'll inject the violator.
		pos := rng.IntN(len(lex)-1) + 1

		// The injected lex must be strictly < lex[pos] (out-of-order
		// against immediately-prior lexeme). Use lex[pos-1]'s predecessor
		// — actually easier: use "0" which sorts before any letter.
		violator := "0_" + lex[pos]

		fstPath := filepath.Join(dir, fmt.Sprintf("fst_oo_%d.bin", trial))
		postPath := filepath.Join(dir, fmt.Sprintf("post_oo_%d.bin", trial))
		w, err := newFSTWriter(fstPath, postPath)
		if err != nil {
			t.Fatalf("newFSTWriter: %v", err)
		}

		var addErr error
		var failedAt int
		for i, l := range lex {
			useLex := l
			if i == pos {
				useLex = violator
			}
			if err := w.Add(useLex, uint32(i+1)); err != nil {
				addErr = err
				failedAt = i
				break
			}
		}
		w.Close() // ignore err — we expect the writer in a broken state

		if addErr == nil {
			t.Errorf("trial=%d: expected error injecting %q at pos %d into %v",
				trial, violator, pos, lex)
			continue
		}
		if failedAt != pos {
			t.Errorf("trial=%d: error fired at i=%d, expected at injection pos %d", trial, failedAt, pos)
		}
		// Error message should name the offending lexeme so production
		// stack traces are debuggable.
		if !strings.Contains(addErr.Error(), violator) {
			t.Errorf("trial=%d: error %q should reference the offending lexeme %q", trial, addErr, violator)
		}
	}
}

// TestProperty_FSTWriter_DuplicateLexCidPairCollapses: pushing the same
// (lex, cid) pair multiple times must not inflate TotalPostings. Set
// semantics (not multiset) is the documented contract.
func TestProperty_FSTWriter_DuplicateLexCidPairCollapses(t *testing.T) {
	dir := t.TempDir()
	rng := rand.New(rand.NewPCG(5, 6))

	for trial := range 30 {
		lexemes := genSortedLexemes(rng, 10)
		fstPath := filepath.Join(dir, fmt.Sprintf("fst_dup_%d.bin", trial))
		postPath := filepath.Join(dir, fmt.Sprintf("post_dup_%d.bin", trial))
		w, err := newFSTWriter(fstPath, postPath)
		if err != nil {
			t.Fatalf("newFSTWriter: %v", err)
		}

		// For each unique lex, write the same cid 3 times back-to-back.
		// genSortedLexemes dedupes adjacent dupes so each entry is a
		// fresh group. The set-semantics dedupe rule says only the first
		// Add per (lex, cid) pair counts — so TotalPostings should
		// equal len(lexemes), and UniqueTerms should match.
		for _, lex := range lexemes {
			cid := uint32(7)
			for range 3 {
				if err := w.Add(lex, cid); err != nil {
					t.Fatalf("Add(%q,%d): %v", lex, cid, err)
				}
			}
		}

		stats, err := w.Close()
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
		if stats.TotalPostings != len(lexemes) {
			t.Errorf("trial=%d TotalPostings = %d, want %d (3× duplicates should collapse to 1)",
				trial, stats.TotalPostings, len(lexemes))
		}
		if stats.UniqueTerms != len(lexemes) {
			t.Errorf("trial=%d UniqueTerms = %d, want %d", trial, stats.UniqueTerms, len(lexemes))
		}
	}
}
