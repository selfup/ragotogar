package main

import (
	"math"
	"reflect"
	"testing"
)

// approxEqualHits compares two []LaneHit slices with a small float
// tolerance on Similarity. FP-summing means values like (0.6+0.8+0.4)/3
// don't land at exactly 0.6 — eq tolerance avoids brittle tests
// without losing real-error detection.
func approxEqualHits(a, b []LaneHit) bool {
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

func TestMergeUnion_MaxAcrossLanes(t *testing.T) {
	per := map[string][]LaneHit{
		"descriptions": {{CompactID: 1, Similarity: 0.5}, {CompactID: 2, Similarity: 0.3}},
		"metadata":     {{CompactID: 1, Similarity: 0.7}, {CompactID: 3, Similarity: 0.6}},
	}
	got := MergeStores(per, MergeOptions{Strategy: MergeUnion})
	// Expected: cid=1 (0.7, max wins), cid=3 (0.6), cid=2 (0.3)
	want := []LaneHit{
		{CompactID: 1, Similarity: 0.7},
		{CompactID: 3, Similarity: 0.6},
		{CompactID: 2, Similarity: 0.3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeIntersect_OnlyCommon(t *testing.T) {
	per := map[string][]LaneHit{
		"descriptions": {{CompactID: 1, Similarity: 0.6}, {CompactID: 2, Similarity: 0.5}},
		"metadata":     {{CompactID: 1, Similarity: 0.8}, {CompactID: 3, Similarity: 0.4}},
		"queries":      {{CompactID: 1, Similarity: 0.4}, {CompactID: 2, Similarity: 0.9}},
	}
	got := MergeStores(per, MergeOptions{Strategy: MergeIntersect})
	// Only cid=1 is in all three lanes. Mean = (0.6+0.8+0.4)/3 = 0.6
	want := []LaneHit{{CompactID: 1, Similarity: 0.6}}
	if !approxEqualHits(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeIntersect_EmptyWhenNoOverlap(t *testing.T) {
	per := map[string][]LaneHit{
		"descriptions": {{CompactID: 1, Similarity: 0.5}},
		"metadata":     {{CompactID: 2, Similarity: 0.5}},
	}
	got := MergeStores(per, MergeOptions{Strategy: MergeIntersect})
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestMergeWeighted_SumWithBias(t *testing.T) {
	per := map[string][]LaneHit{
		"descriptions": {{CompactID: 1, Similarity: 0.5}},
		"metadata":     {{CompactID: 1, Similarity: 0.5}},
		"queries":      {{CompactID: 1, Similarity: 0.5}, {CompactID: 2, Similarity: 0.9}},
	}
	got := MergeStores(per, MergeOptions{
		Strategy:           MergeWeighted,
		WeightDescriptions: 1.0,
		WeightMetadata:     1.0,
		WeightQueries:      3.0, // queries lane biased
	})
	// cid=1: 0.5*1 + 0.5*1 + 0.5*3 = 2.5
	// cid=2: 0.9*3 = 2.7
	// Queries-bias makes the queries-only hit (cid=2) outrank the all-lanes hit (cid=1).
	want := []LaneHit{
		{CompactID: 2, Similarity: 2.7},
		{CompactID: 1, Similarity: 2.5},
	}
	if !approxEqualHits(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeWeighted_ZeroWeightZeroContribution(t *testing.T) {
	per := map[string][]LaneHit{
		"descriptions": {{CompactID: 1, Similarity: 0.9}},
		"metadata":     {{CompactID: 1, Similarity: 0.9}}, // weight=0 → contributes 0
	}
	got := MergeStores(per, MergeOptions{
		Strategy:           MergeWeighted,
		WeightDescriptions: 1.0,
		WeightMetadata:     0.0,
	})
	want := []LaneHit{{CompactID: 1, Similarity: 0.9}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeStores_UnknownStrategyFallsBackToUnion(t *testing.T) {
	per := map[string][]LaneHit{
		"descriptions": {{CompactID: 1, Similarity: 0.5}},
	}
	got := MergeStores(per, MergeOptions{Strategy: MergeStrategy("garbage")})
	want := []LaneHit{{CompactID: 1, Similarity: 0.5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeStores_EmptyMap(t *testing.T) {
	got := MergeStores(map[string][]LaneHit{}, MergeOptions{Strategy: MergeUnion})
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

// Within a single lane, duplicate compact_ids (which shouldn't happen
// post-MAX-collapse, but might in a hand-constructed test) collapse via
// max in union and via per-lane "seen" set in intersect/weighted —
// only counted once toward presence/sum.
func TestMergeStores_DupWithinLane(t *testing.T) {
	per := map[string][]LaneHit{
		"descriptions": {
			{CompactID: 1, Similarity: 0.4},
			{CompactID: 1, Similarity: 0.7}, // dup; should win in union
		},
	}
	got := MergeStores(per, MergeOptions{Strategy: MergeUnion})
	want := []LaneHit{{CompactID: 1, Similarity: 0.7}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
