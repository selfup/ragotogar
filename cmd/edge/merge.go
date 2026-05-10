package main

import "sort"

// MergeStrategy mirrors library.MergeStrategy verbatim — same string
// values, same semantics. Reimplemented on uint32 keys so the edge
// runtime stays decoupled from library's name-keyed Result type.
type MergeStrategy string

const (
	MergeUnion     MergeStrategy = "union"
	MergeIntersect MergeStrategy = "intersect"
	MergeWeighted  MergeStrategy = "weighted"
)

// MergeOptions are the tunable knobs for MergeStores. Only the
// per-store weights matter under MergeWeighted; other strategies
// ignore them.
type MergeOptions struct {
	Strategy           MergeStrategy
	WeightDescriptions float64
	WeightMetadata     float64
	WeightQueries      float64
}

// MergeStores combines per-lane []LaneHit lists into a single ranked
// list per the chosen strategy. Pure-math equivalent of
// library.mergeStores — the .Similarity values across strategies
// aren't comparable (max-similarity / mean-similarity / weighted-sum)
// so callers should treat the rank as the authoritative output.
func MergeStores(perLane map[string][]LaneHit, opts MergeOptions) []LaneHit {
	switch opts.Strategy {
	case MergeIntersect:
		return mergeIntersect(perLane)
	case MergeWeighted:
		return mergeWeighted(perLane, opts)
	case MergeUnion, "":
		return mergeUnion(perLane)
	default:
		// Unknown strategy → fall back to union for safety. Same as
		// library.mergeStores.
		return mergeUnion(perLane)
	}
}

// mergeUnion: every compact_id present in any lane wins; similarity =
// MAX across lanes the id appeared in.
func mergeUnion(perLane map[string][]LaneHit) []LaneHit {
	best := map[uint32]float64{}
	for _, hits := range perLane {
		for _, h := range hits {
			if cur, seen := best[h.CompactID]; !seen || h.Similarity > cur {
				best[h.CompactID] = h.Similarity
			}
		}
	}
	out := make([]LaneHit, 0, len(best))
	for cid, sim := range best {
		out = append(out, LaneHit{CompactID: cid, Similarity: sim})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	return out
}

// mergeIntersect: keep only compact_ids present in every lane;
// similarity = mean across lanes (compromise between MIN and MAX).
func mergeIntersect(perLane map[string][]LaneHit) []LaneHit {
	if len(perLane) == 0 {
		return nil
	}
	presence := map[uint32]int{}
	accum := map[uint32]float64{}
	for _, hits := range perLane {
		seen := map[uint32]bool{}
		for _, h := range hits {
			if seen[h.CompactID] {
				continue
			}
			seen[h.CompactID] = true
			presence[h.CompactID]++
			accum[h.CompactID] += h.Similarity
		}
	}
	required := len(perLane)
	out := make([]LaneHit, 0)
	for cid, count := range presence {
		if count == required {
			out = append(out, LaneHit{CompactID: cid, Similarity: accum[cid] / float64(required)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	return out
}

// mergeWeighted: per-lane similarity × weight summed per compact_id.
// Photos appearing in multiple lanes naturally rise; weights bias
// toward (e.g.) the queries lane.
func mergeWeighted(perLane map[string][]LaneHit, opts MergeOptions) []LaneHit {
	weights := map[string]float64{
		"descriptions": opts.WeightDescriptions,
		"metadata":     opts.WeightMetadata,
		"queries":      opts.WeightQueries,
	}
	scores := map[uint32]float64{}
	for laneName, hits := range perLane {
		w := weights[laneName]
		seen := map[uint32]bool{}
		for _, h := range hits {
			if seen[h.CompactID] {
				continue
			}
			seen[h.CompactID] = true
			scores[h.CompactID] += h.Similarity * w
		}
	}
	out := make([]LaneHit, 0, len(scores))
	for cid, score := range scores {
		out = append(out, LaneHit{CompactID: cid, Similarity: score})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	return out
}
