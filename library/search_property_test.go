package library

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"testing"
)

// Property tests for the search math layer: rrfFuse, mergeUnion,
// mergeIntersect, mergeWeighted. Example-based tests in search_test.go
// and search_v2_test.go cover specific shapes; these cover the
// invariants those examples instantiate.

const propertyTrials = 100

// genResultList produces a list of randomly-named Results with random
// similarities in [0, 1]. Names are drawn from a small alphabet so
// multiple lists naturally share documents (which is the interesting
// case for RRF and merge — overlap is when the math actually matters).
func genResultList(rng *rand.Rand, maxLen int) []Result {
	n := rng.IntN(maxLen + 1) // 0..maxLen inclusive
	out := make([]Result, n)
	for i := range out {
		out[i] = Result{
			Name:       fmt.Sprintf("doc_%d", rng.IntN(2*maxLen)), // small alphabet → overlap likely
			Similarity: rng.Float64(),
		}
	}
	// Sort by similarity DESC so the list looks like real retrieval output.
	sort.Slice(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	// Dedupe by name keeping the highest similarity. Real retrievers
	// don't emit dupes within one store.
	seen := make(map[string]bool, len(out))
	kept := out[:0]
	for _, r := range out {
		if seen[r.Name] {
			continue
		}
		seen[r.Name] = true
		kept = append(kept, r)
	}
	return kept
}

// TestProperty_RRF_OutputContainsExactlyInputUnion: every name in the
// output appeared in at least one input list, and every name that
// appeared in some input list shows up in the output (modulo topK).
func TestProperty_RRF_OutputContainsExactlyInputUnion(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := range propertyTrials {
		lists := [][]Result{
			genResultList(rng, 10),
			genResultList(rng, 10),
		}
		fused := rrfFuse(lists, RRFK, 0) // topK=0: no truncation

		inputUnion := map[string]bool{}
		for _, list := range lists {
			for _, r := range list {
				inputUnion[r.Name] = true
			}
		}
		outputSet := map[string]bool{}
		for _, r := range fused {
			outputSet[r.Name] = true
		}

		// Every output is in input.
		for name := range outputSet {
			if !inputUnion[name] {
				t.Errorf("trial=%d: output contains %q not in any input list", trial, name)
			}
		}
		// Every input is in output (topK=0).
		for name := range inputUnion {
			if !outputSet[name] {
				t.Errorf("trial=%d: input %q missing from output (topK=0)", trial, name)
			}
		}
	}
}

// TestProperty_RRF_ScoresMonotonicDescending: rrfFuse's output is sorted
// by fused score DESC. The output Result.Similarity is the underlying
// cosine, not the RRF score, so we can't validate that directly — but
// we can verify the relative ranking matches the RRF math by recomputing
// scores on the output names and checking the order.
func TestProperty_RRF_ScoresMonotonicDescending(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for trial := range propertyTrials {
		lists := [][]Result{
			genResultList(rng, 8),
			genResultList(rng, 8),
		}
		fused := rrfFuse(lists, RRFK, 0)
		if len(fused) < 2 {
			continue
		}
		// Compute RRF scores from the inputs for every name in the output.
		score := map[string]float64{}
		for _, list := range lists {
			for rank, r := range list {
				score[r.Name] += 1.0 / (RRFK + float64(rank+1))
			}
		}
		for i := 1; i < len(fused); i++ {
			prev := score[fused[i-1].Name]
			curr := score[fused[i].Name]
			if prev < curr {
				t.Errorf("trial=%d: fused[%d] score %v < fused[%d] score %v (not sorted DESC)",
					trial, i-1, prev, i, curr)
			}
		}
	}
}

// TestProperty_RRF_CommonItemBeatsSingletonAtSameRank: a name present in
// BOTH lists at rank R should outrank a name present in only ONE list at
// rank R. The whole point of RRF is to reward agreement across arms.
func TestProperty_RRF_CommonItemBeatsSingletonAtSameRank(t *testing.T) {
	for rank := range 10 {
		shared := []Result{
			{Name: "shared"},
			// rank-many filler items to push 'shared' to rank `rank`
		}
		// Place 'shared' at the same rank in both lists; place 'lonely' at
		// the same rank in only one list. Pad with unique fillers above.
		listA := append(make([]Result, 0, rank+1), shared...)
		listB := append(make([]Result, 0, rank+1), shared...)
		// Pad both lists with unique fillers (different on each side) up
		// to length rank+1 so 'shared' lands at position rank.
		listA = padBefore(listA, "fa_", rank)
		listB = padBefore(listB, "fb_", rank)
		// 'lonely' lands at the same rank as 'shared' but only in list A.
		listA = appendAt(listA, "lonely", rank+1)

		fused := rrfFuse([][]Result{listA, listB}, RRFK, 0)
		sharedIdx, lonelyIdx := -1, -1
		for i, r := range fused {
			if r.Name == "shared" {
				sharedIdx = i
			}
			if r.Name == "lonely" {
				lonelyIdx = i
			}
		}
		if sharedIdx < 0 || lonelyIdx < 0 {
			t.Fatalf("rank=%d: shared or lonely missing from fused (sharedIdx=%d lonelyIdx=%d)", rank, sharedIdx, lonelyIdx)
		}
		if sharedIdx > lonelyIdx {
			t.Errorf("rank=%d: shared ranked %d, lonely ranked %d — shared should outrank lonely (it appears in both lists)",
				rank, sharedIdx, lonelyIdx)
		}
	}
}

func padBefore(list []Result, prefix string, n int) []Result {
	// Prepend n unique filler items so the existing entries shift to rank n.
	if n == 0 {
		return list
	}
	padded := make([]Result, 0, len(list)+n)
	for i := range n {
		padded = append(padded, Result{Name: fmt.Sprintf("%s%d", prefix, i)})
	}
	padded = append(padded, list...)
	return padded
}

func appendAt(list []Result, name string, idx int) []Result {
	// Make sure 'name' lands at output rank idx — assuming list has idx
	// items already.
	for len(list) < idx {
		list = append(list, Result{Name: fmt.Sprintf("pad_%d", len(list))})
	}
	return append(list, Result{Name: name})
}

// TestProperty_RRF_TopKTruncates: when topK > 0, len(output) ≤ topK.
func TestProperty_RRF_TopKTruncates(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	for trial := range propertyTrials {
		lists := [][]Result{
			genResultList(rng, 20),
			genResultList(rng, 20),
		}
		topK := rng.IntN(15) + 1 // 1..15
		fused := rrfFuse(lists, RRFK, topK)
		if len(fused) > topK {
			t.Errorf("trial=%d: len(fused)=%d > topK=%d", trial, len(fused), topK)
		}
	}
}

// genStoreResults produces a map keyed on a random subset of the three
// known store names, each with a random list of Results. Real callers
// always pass exactly the three store keys ("descriptions", "metadata",
// "queries") but the merge functions don't enforce that — they merge
// whatever map they're handed.
func genStoreResults(rng *rand.Rand) map[string][]Result {
	stores := []string{"descriptions", "metadata", "queries"}
	out := map[string][]Result{}
	for _, s := range stores {
		// 50% chance each store is enabled. At least one store guaranteed
		// non-empty in callers, but the merge functions handle the empty
		// case too — let's exercise it.
		if rng.IntN(2) == 0 {
			continue
		}
		out[s] = genResultList(rng, 10)
	}
	return out
}

// TestProperty_MergeUnion_OutputIsExactlyTheUnion: every name from any
// input store appears once in the output; nothing extra.
func TestProperty_MergeUnion_OutputIsExactlyTheUnion(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	for trial := range propertyTrials {
		stores := genStoreResults(rng)
		out := mergeUnion(stores)

		expected := map[string]bool{}
		for _, list := range stores {
			for _, r := range list {
				expected[r.Name] = true
			}
		}
		gotSet := map[string]bool{}
		for _, r := range out {
			if gotSet[r.Name] {
				t.Errorf("trial=%d: %q appears twice in output", trial, r.Name)
			}
			gotSet[r.Name] = true
		}
		if len(gotSet) != len(expected) {
			t.Errorf("trial=%d: output size %d, want %d", trial, len(gotSet), len(expected))
		}
		for name := range expected {
			if !gotSet[name] {
				t.Errorf("trial=%d: %q in input but not in union output", trial, name)
			}
		}
	}
}

// TestProperty_MergeUnion_SimilarityIsMaxAcrossStores: a name appearing
// in multiple stores carries the highest similarity it had in any store.
func TestProperty_MergeUnion_SimilarityIsMaxAcrossStores(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10))
	for trial := range propertyTrials {
		stores := genStoreResults(rng)
		out := mergeUnion(stores)

		for _, r := range out {
			var maxSeen float64
			for _, list := range stores {
				for _, lr := range list {
					if lr.Name == r.Name && lr.Similarity > maxSeen {
						maxSeen = lr.Similarity
					}
				}
			}
			if r.Similarity != maxSeen {
				t.Errorf("trial=%d: %q similarity = %v, want max-across-stores = %v",
					trial, r.Name, r.Similarity, maxSeen)
			}
		}
	}
}

// TestProperty_MergeUnion_SortedDescending: output ordered by similarity.
func TestProperty_MergeUnion_SortedDescending(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12))
	for trial := range propertyTrials {
		out := mergeUnion(genStoreResults(rng))
		for i := 1; i < len(out); i++ {
			if out[i-1].Similarity < out[i].Similarity {
				t.Errorf("trial=%d: out[%d].Similarity=%v < out[%d].Similarity=%v",
					trial, i-1, out[i-1].Similarity, i, out[i].Similarity)
			}
		}
	}
}

// TestProperty_MergeIntersect_OutputIsExactlyTheIntersection: output
// contains a name iff every non-empty input store has that name.
func TestProperty_MergeIntersect_OutputIsExactlyTheIntersection(t *testing.T) {
	rng := rand.New(rand.NewPCG(13, 14))
	for trial := range propertyTrials {
		stores := genStoreResults(rng)
		// Empty input → empty output. The function preserves "len(stores)
		// == 0" as a special case — exercise it via empty stores.
		out := mergeIntersect(stores)

		// Build the expected intersection by hand.
		expected := map[string]bool{}
		first := true
		for _, list := range stores {
			cur := map[string]bool{}
			for _, r := range list {
				cur[r.Name] = true
			}
			if first {
				expected = cur
				first = false
				continue
			}
			next := map[string]bool{}
			for k := range expected {
				if cur[k] {
					next[k] = true
				}
			}
			expected = next
		}
		if first {
			// no stores → no expectation
			if len(out) != 0 {
				t.Errorf("trial=%d: no stores, got non-empty output %v", trial, out)
			}
			continue
		}
		got := map[string]bool{}
		for _, r := range out {
			got[r.Name] = true
		}
		if len(got) != len(expected) {
			t.Errorf("trial=%d: output size %d, want intersection size %d (expected=%v got=%v)",
				trial, len(got), len(expected), expected, got)
		}
		for name := range expected {
			if !got[name] {
				t.Errorf("trial=%d: intersection should contain %q", trial, name)
			}
		}
	}
}

// TestProperty_MergeIntersect_SimilarityIsMean: for items in the
// intersection, output similarity == mean of per-store similarities.
func TestProperty_MergeIntersect_SimilarityIsMean(t *testing.T) {
	rng := rand.New(rand.NewPCG(15, 16))
	for trial := range propertyTrials {
		stores := genStoreResults(rng)
		out := mergeIntersect(stores)
		nStores := len(stores)
		if nStores == 0 {
			continue
		}

		for _, r := range out {
			var sum float64
			var present int
			for _, list := range stores {
				for _, lr := range list {
					if lr.Name == r.Name {
						sum += lr.Similarity
						present++
						break // mergeIntersect dedupes inside each store list
					}
				}
			}
			want := sum / float64(nStores)
			const eps = 1e-9
			if d := r.Similarity - want; d > eps || d < -eps {
				t.Errorf("trial=%d: %q similarity = %v, want mean(%v / %d) = %v",
					trial, r.Name, r.Similarity, sum, nStores, want)
			}
		}
	}
}

// TestProperty_MergeWeighted_ZeroWeightContributesZero: a store with
// weight=0 still has its items counted as "appeared in this store", but
// contributes 0 to the score. Items appearing only in zero-weight stores
// land with similarity=0.
func TestProperty_MergeWeighted_ZeroWeightContributesZero(t *testing.T) {
	stores := map[string][]Result{
		"descriptions": {{Name: "a", Similarity: 0.9}, {Name: "b", Similarity: 0.5}},
		"metadata":     {{Name: "a", Similarity: 0.4}}, // a also here
	}
	opts := SearchOptionsV2{
		WeightDescriptions: 1.0,
		WeightMetadata:     0.0, // zero weight
		WeightQueries:      0.0,
	}
	out := mergeWeighted(stores, opts)

	// 'a' score = 0.9*1 + 0.4*0 = 0.9. 'b' score = 0.5*1 = 0.5.
	scores := map[string]float64{}
	for _, r := range out {
		scores[r.Name] = r.Similarity
	}
	if got, want := scores["a"], 0.9; got != want {
		t.Errorf("a similarity = %v, want %v", got, want)
	}
	if got, want := scores["b"], 0.5; got != want {
		t.Errorf("b similarity = %v, want %v", got, want)
	}
}

// TestProperty_MergeWeighted_ScoreIsSumOfWeightedSimilarities: for every
// output item, similarity = sum across stores of (per-store sim ×
// per-store weight). Compares hand-computed expected score to actual.
func TestProperty_MergeWeighted_ScoreIsSumOfWeightedSimilarities(t *testing.T) {
	rng := rand.New(rand.NewPCG(17, 18))
	for trial := range propertyTrials {
		stores := genStoreResults(rng)
		opts := SearchOptionsV2{
			WeightDescriptions: rng.Float64() * 3,
			WeightMetadata:     rng.Float64() * 3,
			WeightQueries:      rng.Float64() * 3,
		}
		weights := map[string]float64{
			"descriptions": opts.WeightDescriptions,
			"metadata":     opts.WeightMetadata,
			"queries":      opts.WeightQueries,
		}

		out := mergeWeighted(stores, opts)
		for _, r := range out {
			var expected float64
			for storeName, list := range stores {
				for _, lr := range list {
					if lr.Name == r.Name {
						expected += lr.Similarity * weights[storeName]
						break
					}
				}
			}
			const eps = 1e-9
			if d := r.Similarity - expected; d > eps || d < -eps {
				t.Errorf("trial=%d: %q similarity = %v, want weighted-sum %v",
					trial, r.Name, r.Similarity, expected)
			}
		}
	}
}
