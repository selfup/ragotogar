package main

import "sort"

// RRFK is the standard Reciprocal Rank Fusion constant. 60 is the
// canonical value from the original Cormack et al. paper and matches
// library.RRFK so cross-arm scores are directly comparable in parity
// validation.
const RRFK = 60.0

// RRFFuse combines two or more ranked arms into one fused ranking via
//
//	score(doc) = sum over arms of 1 / (k + rank_in_arm(doc))
//
// where rank is 1-indexed (top result = rank 1). Docs appearing in
// multiple arms accumulate; docs in only one arm still get a
// contribution but with smaller score.
//
// Stable tie-break by ascending compact_id keeps output deterministic.
func RRFFuse(arms ...[]LaneHit) []LaneHit {
	scores := map[uint32]float64{}
	for _, arm := range arms {
		for rank, hit := range arm {
			scores[hit.CompactID] += 1.0 / (RRFK + float64(rank+1))
		}
	}
	out := make([]LaneHit, 0, len(scores))
	for cid, s := range scores {
		out = append(out, LaneHit{CompactID: cid, Similarity: s})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Similarity != out[j].Similarity {
			return out[i].Similarity > out[j].Similarity
		}
		return out[i].CompactID < out[j].CompactID
	})
	return out
}
