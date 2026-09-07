package main

import "testing"

func TestEmbedderDriftChecksDimensionAsWellAsModel(t *testing.T) {
	model := "text-embedding-qwen3-embedding-0.6b"
	t.Setenv("EMBED_MODEL", model)
	t.Setenv("EMBED_DIM", "")
	mf := &Manifest{Dim: 1024, Lanes: map[string]LaneEntry{
		"descriptions": {EmbedderVersion: model},
	}}
	if err := checkEmbedderDrift(mf); err != nil {
		t.Fatalf("matching 0.6B artifacts rejected: %v", err)
	}
	mf.Dim = 2560
	if err := checkEmbedderDrift(mf); err == nil {
		t.Fatal("must reject incompatible vector dimensions even when model IDs match")
	}
}
