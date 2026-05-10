package main

import (
	"testing"
)

// buildSyntheticArtifacts builds a tiny in-memory Artifacts with one
// vector lane and a manifest sufficient for ScanLane to operate on.
// Vectors and rowmap are passed in directly; no mmap.
func buildSyntheticArtifacts(laneName string, dim int, rows int, vectors []byte, rowmap []uint32) *Artifacts {
	return &Artifacts{
		Manifest: &Manifest{Dim: dim},
		Lanes: map[string]*VectorLane{
			laneName: {
				Name:    laneName,
				Vectors: vectors,
				RowMap:  rowmap,
				Rows:    rows,
			},
		},
	}
}

// b returns the byte form of an int8 — keeps the synthetic vector
// declarations readable.
func b(v int8) byte { return byte(v) }

func TestScanLane_BasicDotAndMaxCollapse(t *testing.T) {
	// dim=4, 3 rows. Rows 0 and 2 share compact_id=7 (multi-row case);
	// row 1 has compact_id=3.
	dim := 4
	vectors := []byte{
		b(100), b(0), b(0), b(0), // row 0 → cid 7, dot with q={127,0,0,0} = 100*127 = 12700
		b(0), b(127), b(0), b(0), // row 1 → cid 3, dot = 0
		b(127), b(0), b(0), b(0), // row 2 → cid 7, dot = 127*127 = 16129
	}
	rowmap := []uint32{7, 3, 7}
	a := buildSyntheticArtifacts("d", dim, 3, vectors, rowmap)

	q := []byte{b(127), b(0), b(0), b(0)}
	hits, err := a.ScanLane("d", q, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	// MAX-collapse: cid=7 should have similarity = 16129/127² = 1.0 (best row wins).
	// cid=3 should have similarity = 0.
	if hits[0].CompactID != 7 {
		t.Errorf("rank 0 cid = %d, want 7 (MAX over multi-row)", hits[0].CompactID)
	}
	want7 := 16129.0 / (127.0 * 127.0)
	if hits[0].Similarity != want7 {
		t.Errorf("cid=7 similarity = %v, want %v", hits[0].Similarity, want7)
	}
	if hits[1].CompactID != 3 || hits[1].Similarity != 0 {
		t.Errorf("rank 1 = (%d, %v), want (3, 0)", hits[1].CompactID, hits[1].Similarity)
	}
}

func TestScanLane_ThresholdDropsBelow(t *testing.T) {
	dim := 4
	vectors := []byte{
		b(127), b(0), b(0), b(0), // row 0 → cid 0, dot=16129, cosine=1.0
		b(64), b(0), b(0), b(0),  // row 1 → cid 1, dot=8128, cosine ≈ 0.504
	}
	rowmap := []uint32{0, 1}
	a := buildSyntheticArtifacts("d", dim, 2, vectors, rowmap)

	q := []byte{b(127), b(0), b(0), b(0)}
	hits, err := a.ScanLane("d", q, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1 after threshold", len(hits))
	}
	if hits[0].CompactID != 0 {
		t.Errorf("survivor cid = %d, want 0", hits[0].CompactID)
	}
}

func TestScanLane_UnknownLane(t *testing.T) {
	a := buildSyntheticArtifacts("d", 4, 0, nil, nil)
	q := []byte{0, 0, 0, 0}
	if _, err := a.ScanLane("nope", q, 0); err == nil {
		t.Fatal("expected error for unknown lane")
	}
}

func TestScanLane_DimMismatch(t *testing.T) {
	a := buildSyntheticArtifacts("d", 4, 0, nil, nil)
	q := []byte{0, 0, 0} // wrong dim
	if _, err := a.ScanLane("d", q, 0); err == nil {
		t.Fatal("expected error for dim mismatch")
	}
}

func TestScanLane_EmptyLane(t *testing.T) {
	a := buildSyntheticArtifacts("d", 4, 0, nil, nil)
	q := []byte{0, 0, 0, 0}
	hits, err := a.ScanLane("d", q, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected 0 hits on empty lane, got %d", len(hits))
	}
}
