package main

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/blevesearch/vellum"
)

func TestTokenizeQuery(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		// "red" / "truck" don't stem to anything different
		{"red truck", []string{"red", "truck"}},
		{"  spaces  around  ", []string{"space", "around"}}, // "spaces" → "space"
		{"hyphen-word", []string{"hyphen", "word"}},
		{"comma,separated.values", []string{"comma", "separ", "valu"}}, // "separated" → "separ", "values" → "valu"
		{"X100VI 2024", []string{"x100vi", "2024"}},                    // identifiers unchanged
		{"", nil},
		{"!!!", nil},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := tokenizeQuery(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// Stemming parity probe: the Porter2 stemmer Snowball ships should
// produce the same lexemes pg's to_tsvector('english') stores in the
// FST. Lock the expectations for the words observed in the live
// corpus probe so a future Snowball version bump or replacement that
// breaks parity gets caught here.
func TestTokenizeQuery_StemmingMatchesPgEnglish(t *testing.T) {
	cases := map[string]string{
		"airplane":      "airplan",
		"airplanes":     "airplan",
		"propeller":     "propel",
		"propellers":    "propel",
		"engine":        "engin",
		"engines":       "engin",
		"single":        "singl",
		"trucks":        "truck",
		"truck":         "truck",
		"running":       "run",
		"flying":        "fli", // Porter2 lowercases + applies suffix rules
		"x100vi":        "x100vi",
		"nikon":         "nikon",
		"2024":          "2024",
	}
	for word, want := range cases {
		got := tokenizeQuery(word)
		if len(got) != 1 || got[0] != want {
			t.Errorf("tokenizeQuery(%q) = %v, want [%q]", word, got, want)
		}
	}
}

// buildSyntheticFSTArtifacts produces an Artifacts with a real vellum
// FST + postings.bin built from a terms map. Same encoding as the
// build's fstWriter (varint count + varint deltas), so this exercises
// both ends of the on-disk contract end-to-end.
func buildSyntheticFSTArtifacts(t *testing.T, terms map[string][]uint32) *Artifacts {
	t.Helper()

	// Sort terms lex; vellum requires it.
	keys := make([]string, 0, len(terms))
	for k := range terms {
		keys = append(keys, k)
	}
	// Sort: lexeme ASC
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}

	var fstBuf bytes.Buffer
	builder, err := vellum.New(&fstBuf, nil)
	if err != nil {
		t.Fatal(err)
	}

	var postBuf bytes.Buffer
	scratch := make([]byte, binary.MaxVarintLen64)

	for _, k := range keys {
		ids := terms[k]
		offset := uint64(postBuf.Len())

		n := binary.PutUvarint(scratch, uint64(len(ids)))
		postBuf.Write(scratch[:n])
		var prev uint32
		for _, cid := range ids {
			delta := cid - prev
			n := binary.PutUvarint(scratch, uint64(delta))
			postBuf.Write(scratch[:n])
			prev = cid
		}
		if err := builder.Insert([]byte(k), offset); err != nil {
			t.Fatal(err)
		}
	}
	if err := builder.Close(); err != nil {
		t.Fatal(err)
	}

	fst, err := vellum.Load(fstBuf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return &Artifacts{
		FST:      fst,
		Postings: postBuf.Bytes(),
	}
}

func TestScanFST_CoverageRank(t *testing.T) {
	// Three docs (compact ids 0, 1, 2):
	//   doc 0 contains: red truck
	//   doc 1 contains: red car
	//   doc 2 contains: blue truck
	// Query "red truck" → doc 0 covers both (rank 1), docs 1 and 2 cover one each.
	a := buildSyntheticFSTArtifacts(t, map[string][]uint32{
		"red":   {0, 1},
		"truck": {0, 2},
		"car":   {1},
		"blue":  {2},
	})

	hits, err := a.ScanFST("red truck")
	if err != nil {
		t.Fatal(err)
	}

	// Expected order: cid=0 (count 2), then cid=1 (count 1), cid=2 (count 1).
	// Tie-break ascending cid, so 1 before 2.
	want := []LaneHit{
		{CompactID: 0, Similarity: 2},
		{CompactID: 1, Similarity: 1},
		{CompactID: 2, Similarity: 1},
	}
	if !reflect.DeepEqual(hits, want) {
		t.Errorf("got %v, want %v", hits, want)
	}
}

func TestScanFST_TokenNotInFST(t *testing.T) {
	a := buildSyntheticFSTArtifacts(t, map[string][]uint32{
		"truck": {0},
	})
	hits, err := a.ScanFST("ghost")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("expected empty for unknown term, got %v", hits)
	}
}

func TestScanFST_EmptyQuery(t *testing.T) {
	a := buildSyntheticFSTArtifacts(t, map[string][]uint32{"x": {0}})
	hits, err := a.ScanFST("")
	if err != nil {
		t.Fatal(err)
	}
	if hits != nil {
		t.Errorf("expected nil for empty query, got %v", hits)
	}
}

func TestScanFST_CaseInsensitive(t *testing.T) {
	a := buildSyntheticFSTArtifacts(t, map[string][]uint32{"red": {0}})
	hits, err := a.ScanFST("RED")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].CompactID != 0 {
		t.Errorf("uppercase token should hit lowercase index, got %v", hits)
	}
}
