package main

import (
	"sort"
	"strings"
	"unicode"

	"github.com/blevesearch/snowballstem"
	"github.com/blevesearch/snowballstem/english"
)

// tokenizeQuery splits q on non-alphanumeric runes, lowercases, and
// applies the Snowball English (Porter2) stemmer to each token.
// Mirrors pg's to_tsvector('english') closely enough for FST parity
// with the cmd/web FTS arm — pg uses the same Porter2 stemmer.
//
// Remaining parity gap (acceptable for v1):
//   - English stopwords (the, and, of, …) are passed through to FST
//     lookup. They aren't in the FST since pg stripped them at index
//     time, so the lookup returns empty — wasted but harmless.
//   - Identifier-shaped tokens like "x100vi" / "2024" pass through
//     stemming unchanged because Snowball's English rules don't
//     trigger on alphanumeric mixed strings or pure digits.
func tokenizeQuery(q string) []string {
	q = strings.ToLower(q)
	env := snowballstem.NewEnv("")
	stem := func(word string) string {
		env.SetCurrent(word)
		english.Stem(env)
		return env.Current()
	}

	var tokens []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, stem(current.String()))
			current.Reset()
		}
	}
	for _, r := range q {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return tokens
}

// ScanFST runs the FST retrieval arm: for each token in the positive
// query, look up the term in the FST, decode its posting list, and
// accumulate per-compact_id coverage (number of query tokens that hit
// this doc). Returns []LaneHit with Similarity = coverage count,
// sorted desc.
//
// Coverage as a rank function approximates pg's ts_rank for short
// queries — the dominant signal is "does this doc match more or fewer
// of the query tokens." More refined ranking (idf weighting via
// posting-list length) is a v2 candidate; coverage is sufficient for
// v1 and avoids storing additional per-term metadata in the artifact.
//
// Stable tie-break by ascending compact_id keeps output deterministic
// across runs — important for downstream RRF fusion to be reproducible.
func (a *Artifacts) ScanFST(query string) ([]LaneHit, error) {
	tokens := tokenizeQuery(query)
	if len(tokens) == 0 {
		return nil, nil
	}
	coverage := map[uint32]int{}
	for _, tok := range tokens {
		offset, exists, err := a.FST.Get([]byte(tok))
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		ids, err := a.DecodePosting(offset)
		if err != nil {
			return nil, err
		}
		for _, cid := range ids {
			coverage[cid]++
		}
	}
	out := make([]LaneHit, 0, len(coverage))
	for cid, count := range coverage {
		out = append(out, LaneHit{CompactID: cid, Similarity: float64(count)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Similarity != out[j].Similarity {
			return out[i].Similarity > out[j].Similarity
		}
		return out[i].CompactID < out[j].CompactID
	})
	return out, nil
}
