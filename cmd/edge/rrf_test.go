package main

import (
	"math"
	"testing"
)

func approxEqualRRF(a, b []LaneHit) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].CompactID != b[i].CompactID {
			return false
		}
		if math.Abs(a[i].Similarity-b[i].Similarity) > 1e-9 {
			return false
		}
	}
	return true
}

func TestRRFFuse_BasicMath(t *testing.T) {
	// Single arm with 3 results: ranks 1, 2, 3.
	// Score: 1/(60+1), 1/(60+2), 1/(60+3).
	arm := []LaneHit{
		{CompactID: 10, Similarity: 0.9},
		{CompactID: 20, Similarity: 0.8},
		{CompactID: 30, Similarity: 0.7},
	}
	got := RRFFuse(arm)
	want := []LaneHit{
		{CompactID: 10, Similarity: 1.0 / 61.0},
		{CompactID: 20, Similarity: 1.0 / 62.0},
		{CompactID: 30, Similarity: 1.0 / 63.0},
	}
	if !approxEqualRRF(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRRFFuse_TwoArmsCommonItemRises(t *testing.T) {
	// Item appearing in both arms outranks items in only one.
	armA := []LaneHit{
		{CompactID: 1, Similarity: 0.9}, // rank 1 in A
		{CompactID: 2, Similarity: 0.5}, // rank 2 in A
	}
	armB := []LaneHit{
		{CompactID: 3, Similarity: 0.9}, // rank 1 in B
		{CompactID: 1, Similarity: 0.5}, // rank 2 in B — same item, also in A
	}
	got := RRFFuse(armA, armB)
	// cid=1: 1/61 + 1/62 ≈ 0.03253
	// cid=3: 1/61 ≈ 0.01639
	// cid=2: 1/62 ≈ 0.01613
	if len(got) != 3 {
		t.Fatalf("got %d hits, want 3", len(got))
	}
	if got[0].CompactID != 1 {
		t.Errorf("rank 1 cid = %d, want 1 (appeared in both arms)", got[0].CompactID)
	}
	if got[1].CompactID != 3 {
		t.Errorf("rank 2 cid = %d, want 3", got[1].CompactID)
	}
	if got[2].CompactID != 2 {
		t.Errorf("rank 3 cid = %d, want 2", got[2].CompactID)
	}
}

func TestRRFFuse_TieBreakAscendingCID(t *testing.T) {
	// Two arms each with one result at the same rank — same RRF score.
	// Tie-break by ascending compact_id.
	armA := []LaneHit{{CompactID: 5, Similarity: 0.9}}
	armB := []LaneHit{{CompactID: 3, Similarity: 0.9}}
	got := RRFFuse(armA, armB)
	if len(got) != 2 {
		t.Fatalf("got %d hits, want 2", len(got))
	}
	if got[0].CompactID != 3 || got[1].CompactID != 5 {
		t.Errorf("got %v, want [3, 5] by ascending cid tie-break", got)
	}
}

func TestRRFFuse_EmptyArms(t *testing.T) {
	got := RRFFuse(nil, nil)
	if len(got) != 0 {
		t.Errorf("expected empty for empty arms, got %v", got)
	}
}

func TestRRFFuse_OneArmEmpty(t *testing.T) {
	armA := []LaneHit{{CompactID: 1, Similarity: 0.9}}
	got := RRFFuse(armA, nil)
	if len(got) != 1 || got[0].CompactID != 1 {
		t.Errorf("got %v, want one hit", got)
	}
}
