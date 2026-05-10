package main

import (
	"reflect"
	"testing"
)

func TestContainsPhrase(t *testing.T) {
	cases := []struct {
		q    string
		want bool
	}{
		{"red truck", false},
		{"\"red truck\"", true},
		{"red -monochrome", false},
		{"-\"black and white\"", true},
		{"red \"phrase\" -trailing", true},
		{"", false},
	}
	for _, c := range cases {
		got := ContainsPhrase(c.q)
		if got != c.want {
			t.Errorf("ContainsPhrase(%q) = %v, want %v", c.q, got, c.want)
		}
	}
}

// FST keys are stems (the same Porter2 form pg's to_tsvector produces
// at index time, which the runtime tokenizer reproduces). "monochrome"
// stems to "monochrom", "grayscale" to "grayscal".
func TestFSTNegationDrop_SingleToken(t *testing.T) {
	a := buildSyntheticFSTArtifacts(t, map[string][]uint32{
		"monochrom": {1, 5, 10},
		"red":       {2, 5, 7}, // unrelated
	})
	drop, err := a.FSTNegationDrop("-monochrome")
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint32]bool{1: true, 5: true, 10: true}
	if !reflect.DeepEqual(drop, want) {
		t.Errorf("got %v, want %v", drop, want)
	}
}

func TestFSTNegationDrop_MultipleTokensUnion(t *testing.T) {
	a := buildSyntheticFSTArtifacts(t, map[string][]uint32{
		"monochrom": {1, 5, 10},
		"grayscal":  {3, 5, 12},
	})
	drop, err := a.FSTNegationDrop("-monochrome -grayscale")
	if err != nil {
		t.Fatal(err)
	}
	// Union: 1, 3, 5, 10, 12.
	want := map[uint32]bool{1: true, 3: true, 5: true, 10: true, 12: true}
	if !reflect.DeepEqual(drop, want) {
		t.Errorf("got %v, want %v", drop, want)
	}
}

// Defense in depth: if a phrase negation slips past the HTTP 400 phrase
// block, the inner tokens still produce a (broader-than-cmd/web) drop
// set rather than crashing or doing nothing.
func TestFSTNegationDrop_PhraseLikeFallback(t *testing.T) {
	a := buildSyntheticFSTArtifacts(t, map[string][]uint32{
		"black": {1, 7},
		"white": {2, 7},
	})
	drop, err := a.FSTNegationDrop(`-"black and white"`)
	if err != nil {
		t.Fatal(err)
	}
	// Tokens: black, and, white. "and" is unknown — only black + white drop.
	// Drop set: 1, 2, 7.
	want := map[uint32]bool{1: true, 2: true, 7: true}
	if !reflect.DeepEqual(drop, want) {
		t.Errorf("got %v, want %v", drop, want)
	}
}

func TestFSTNegationDrop_TokenNotInFST(t *testing.T) {
	a := buildSyntheticFSTArtifacts(t, map[string][]uint32{"red": {1}})
	drop, err := a.FSTNegationDrop("-ghost")
	if err != nil {
		t.Fatal(err)
	}
	if len(drop) != 0 {
		t.Errorf("expected empty drop for unknown token, got %v", drop)
	}
}

func TestFSTNegationDrop_EmptyNegation(t *testing.T) {
	a := buildSyntheticFSTArtifacts(t, map[string][]uint32{"red": {1}})
	drop, err := a.FSTNegationDrop("")
	if err != nil {
		t.Fatal(err)
	}
	if drop != nil {
		t.Errorf("expected nil drop for empty negation, got %v", drop)
	}
}

func TestFilterByDropSet_DropsCorrectly(t *testing.T) {
	hits := []LaneHit{
		{CompactID: 1, Similarity: 0.9},
		{CompactID: 2, Similarity: 0.8},
		{CompactID: 3, Similarity: 0.7},
		{CompactID: 4, Similarity: 0.6},
	}
	drop := map[uint32]bool{2: true, 4: true}
	got := FilterByDropSet(hits, drop)
	want := []LaneHit{
		{CompactID: 1, Similarity: 0.9},
		{CompactID: 3, Similarity: 0.7},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFilterByDropSet_EmptyDropSetPassthrough(t *testing.T) {
	hits := []LaneHit{
		{CompactID: 1, Similarity: 0.9},
		{CompactID: 2, Similarity: 0.8},
	}
	got := FilterByDropSet(hits, nil)
	if !reflect.DeepEqual(got, hits) {
		t.Errorf("got %v, want %v", got, hits)
	}
	got2 := FilterByDropSet(hits, map[uint32]bool{})
	if !reflect.DeepEqual(got2, hits) {
		t.Errorf("got %v, want %v", got2, hits)
	}
}
