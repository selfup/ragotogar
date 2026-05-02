package library

import "testing"

func TestRRFFuseSharedDocsRiseToTop(t *testing.T) {
	// Doc A: rank 1 in vector, rank 1 in FTS — should be #1 fused
	// Doc B: rank 1 in vector only
	// Doc C: rank 1 in FTS only
	// Doc D: rank 2 in both — second by RRF
	vec := []Result{{Name: "A"}, {Name: "B"}, {Name: "D"}}
	fts := []Result{{Name: "A"}, {Name: "C"}, {Name: "D"}}

	out := rrfFuse([][]Result{vec, fts}, 60, 10)

	if len(out) != 4 {
		t.Fatalf("expected 4 unique docs, got %d", len(out))
	}
	if out[0].Name != "A" {
		t.Errorf("expected A first (rank-1 in both lists), got %s", out[0].Name)
	}
	if out[1].Name != "D" {
		t.Errorf("expected D second (rank-3 in both lists), got %s", out[1].Name)
	}
	// B and C share the third/fourth slot — both have one rank-2 contribution
	last := []string{out[2].Name, out[3].Name}
	if !(last[0] == "B" && last[1] == "C") && !(last[0] == "C" && last[1] == "B") {
		t.Errorf("expected B and C in slots 3-4, got %v", last)
	}
}

func TestRRFFuseSimilarityFromVectorArmPreserved(t *testing.T) {
	// Vector arm has a similarity score; FTS arm doesn't. Fused result
	// should keep the vector similarity.
	vec := []Result{{Name: "A", Similarity: 0.87}}
	fts := []Result{{Name: "A", Similarity: 0}}
	out := rrfFuse([][]Result{vec, fts}, 60, 10)
	if len(out) != 1 || out[0].Name != "A" {
		t.Fatalf("expected single doc A, got %v", out)
	}
	if out[0].Similarity != 0.87 {
		t.Errorf("similarity = %v, want 0.87 (preserved from vector arm)", out[0].Similarity)
	}
}

func TestRRFFuseTopKCap(t *testing.T) {
	vec := []Result{{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}, {Name: "E"}}
	fts := []Result{{Name: "F"}, {Name: "G"}}
	out := rrfFuse([][]Result{vec, fts}, 60, 3)
	if len(out) != 3 {
		t.Errorf("topK=3 should cap output at 3, got %d", len(out))
	}
}

func TestRRFFuseTopKZeroIsUnbounded(t *testing.T) {
	// topK=0 is the "unbounded" sentinel — every unique doc across all
	// input lists should reach the output. SearchHybrid relies on this
	// when the caller leaves opts.TopK at 0 to mean "every match above
	// the cosine cutoff."
	vec := []Result{{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}, {Name: "E"}}
	fts := []Result{{Name: "F"}, {Name: "G"}, {Name: "H"}}
	out := rrfFuse([][]Result{vec, fts}, 60, 0)
	if len(out) != 8 {
		t.Errorf("topK=0 should return all 8 unique docs, got %d", len(out))
	}
}

func TestRRFFuseSingleListPassthrough(t *testing.T) {
	// One empty list shouldn't break the fusion or affect ranks.
	vec := []Result{{Name: "A"}, {Name: "B"}}
	out := rrfFuse([][]Result{vec, nil}, 60, 10)
	if len(out) != 2 || out[0].Name != "A" || out[1].Name != "B" {
		t.Errorf("expected A,B in order, got %v", out)
	}
}

func TestRRFFuseEmptyEverything(t *testing.T) {
	out := rrfFuse([][]Result{nil, nil}, 60, 10)
	if len(out) != 0 {
		t.Errorf("empty input should yield empty result, got %v", out)
	}
}
