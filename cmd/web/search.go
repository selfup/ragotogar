package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"ragotogar/library"
)

// search runs the appropriate retrieval pipeline for the given mode and
// returns matching photo names. All four modes use the same vector cosine
// query under the hood; "hybrid" modes also fold in Postgres FTS via RRF;
// "verify" modes pipe the candidates through the LLM yes/no pass.
//
//   vector            : pure vector retrieval, no LLM
//   naive-verify      : vector retrieval + LLM verify
//   fts-vector        : vector ∪ FTS, fused via Reciprocal Rank Fusion, no LLM
//   fts-vector-verify : RRF fusion + LLM verify
//
// cosine and ftsRel are user-tunable via the UI sliders (URL params
// ?cosine= and ?fts=). Both default to library.CosineThreshold /
// library.FTSRelativeThreshold when the caller passes the package
// defaults.
// searchResult bundles everything cmd/web's HTTP handler renders for a single
// query — the matching photos, the wall-clock duration, and (when the verify
// pass ran) the verify_cache stats so the template can show the hit rate.
type searchResult struct {
	Results []result
	Elapsed time.Duration
	Stats   *library.VerifyStats // nil when verify didn't run
}

func search(db *sql.DB, query, mode string, cosine, ftsRel float64) searchResult {
	start := time.Now()
	ctx := context.Background()
	searcher := library.NewSearcher(db)

	cosineCopy := cosine
	opts := library.SearchOptions{
		TopK:            library.StrictTopK,
		Threshold:       &cosineCopy,
		FTSThresholdRel: ftsRel,
	}

	fmt.Fprintf(os.Stderr, "search: q=%q mode=%s cosine=%.2f fts=%.2f\n", query, mode, cosine, ftsRel)

	var (
		candidates []library.Result
		err        error
	)
	switch mode {
	case "fts-vector", "fts-vector-verify":
		candidates, err = searcher.SearchHybrid(ctx, query, opts)
	default:
		candidates, err = searcher.Search(ctx, query, opts)
	}
	if err != nil {
		log.Printf("search %q (mode=%s): %v", query, mode, err)
		return searchResult{Elapsed: time.Since(start)}
	}

	verify := mode == "naive-verify" || mode == "fts-vector-verify"

	var (
		names []string
		stats *library.VerifyStats
	)
	if verify && len(candidates) > 0 {
		fmt.Fprintf(os.Stderr, "\n--- Verifying %d candidate(s) with LLM ---\n", len(candidates))
		verdicts, s, err := searcher.VerifyFilter(ctx, query, candidates)
		if err != nil {
			log.Printf("verify %q: %v", query, err)
			return searchResult{Elapsed: time.Since(start)}
		}
		library.LogVerdicts(os.Stderr, verdicts)
		library.LogVerifyStats(os.Stderr, s)
		names = library.KeptNames(verdicts)
		stats = &s
	} else {
		names = make([]string, len(candidates))
		for i, c := range candidates {
			names[i] = c.Name
		}
	}

	results := make([]result, 0, len(names))
	for _, name := range names {
		results = append(results, result{Name: name})
	}
	return searchResult{Results: results, Elapsed: time.Since(start), Stats: stats}
}
