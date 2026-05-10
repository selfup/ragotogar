package main

import (
	"fmt"
	"strings"
)

// ContainsPhrase reports whether q contains any quoted phrase, positive
// or negative. v1 blocks both at HTTP 400 — the edge FST has no
// position info, so phrase adjacency can't be reproduced faithfully
// (decision #2c in EDGE.md). Honest 400 beats silent over- or
// under-matching.
func ContainsPhrase(q string) bool {
	return strings.Contains(q, `"`)
}

// FSTNegationDrop computes the set of compact_ids to drop based on
// the negation portion of the query. negation is the output of
// library.ExtractNegation — tokens with leading `-` preserved
// (e.g. `-monochrome -grayscale`).
//
// A compact_id is in the drop set if it appears in ANY negation
// token's posting list (union — same as cmd/web's
// websearch_to_tsquery NOT operator over descriptions.fts ‖ exif.fts).
//
// Phrase negations should have been blocked at HTTP 400 by
// ContainsPhrase before this function runs — but if they slip
// through, this function still does the right thing by tokenizing the
// inner content and dropping each bare token individually. Defense in
// depth.
func (a *Artifacts) FSTNegationDrop(negation string) (map[uint32]bool, error) {
	if negation == "" {
		return nil, nil
	}
	drop := map[uint32]bool{}
	for raw := range strings.FieldsSeq(negation) {
		raw = strings.TrimLeft(raw, "-\"")
		raw = strings.TrimRight(raw, "\"")
		for _, tok := range tokenizeQuery(raw) {
			offset, exists, err := a.FST.Get([]byte(tok))
			if err != nil {
				return nil, fmt.Errorf("FST lookup for negation token %q: %w", tok, err)
			}
			if !exists {
				continue
			}
			ids, err := a.DecodePosting(offset)
			if err != nil {
				return nil, fmt.Errorf("decode posting for negation token %q: %w", tok, err)
			}
			for _, cid := range ids {
				drop[cid] = true
			}
		}
	}
	return drop, nil
}

// FilterByDropSet returns hits with compact_ids in dropSet removed.
// Allocates a new slice; the input is not mutated.
func FilterByDropSet(hits []LaneHit, dropSet map[uint32]bool) []LaneHit {
	if len(dropSet) == 0 {
		return hits
	}
	out := make([]LaneHit, 0, len(hits))
	for _, h := range hits {
		if !dropSet[h.CompactID] {
			out = append(out, h)
		}
	}
	return out
}
