package main

import (
	"fmt"
	"sort"
)

// LaneHit is one (compact_id, similarity) pair surfacing from a
// per-lane scan after MAX-collapse and threshold filtering.
type LaneHit struct {
	CompactID  uint32
	Similarity float64
}

// ScanLane runs a flat int8 cosine scan over the named lane's
// vectors, MAX-collapses similarity per compact_id via the rowmap
// sidecar, applies threshold, and returns survivors sorted desc.
//
// Cosine ≈ dot(query_int8, row_int8) / 127² because both vectors are
// L2-normalized before quantization. Quantization-induced cosine
// error is ~1-3% (documented in EDGE.md).
//
// Multi-row lanes (descriptions, queries) collapse per cmd/web's
// MAX-similarity rule — best chunk / best phrasing wins. Single-row
// lanes (metadata at v12) collapse trivially since each compact_id
// appears once.
func (a *Artifacts) ScanLane(laneName string, queryInt8 []byte, threshold float64) ([]LaneHit, error) {
	lane, ok := a.Lanes[laneName]
	if !ok {
		return nil, fmt.Errorf("unknown lane %q", laneName)
	}
	dim := a.Manifest.Dim
	if len(queryInt8) != dim {
		return nil, fmt.Errorf("query dim=%d, lane dim=%d", len(queryInt8), dim)
	}
	if lane.Rows == 0 {
		return nil, nil
	}

	perCID := make(map[uint32]float64, lane.Rows)
	const inv = 1.0 / (127.0 * 127.0)

	for r := range lane.Rows {
		row := lane.Vectors[r*dim : (r+1)*dim]
		dot := int32(0)
		for i := range dim {
			dot += int32(int8(row[i])) * int32(int8(queryInt8[i]))
		}
		sim := float64(dot) * inv
		cid := lane.RowMap[r]
		if cur, seen := perCID[cid]; !seen || sim > cur {
			perCID[cid] = sim
		}
	}

	out := make([]LaneHit, 0, len(perCID))
	for cid, sim := range perCID {
		if sim >= threshold {
			out = append(out, LaneHit{CompactID: cid, Similarity: sim})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	return out, nil
}
