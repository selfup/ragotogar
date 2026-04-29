package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	"ragotogar/internal/library"
)

// search runs the vector retrieval (and optional verify) pipeline against
// the library directly via library.Searcher — no exec, no shell, no go run
// per request. Mode "naive-verify" composes the verify pass; everything
// else is plain top-500 retrieval at cosine ≥ 0.5.
//
// "graph" and "hybrid" pills are kept in the UI for backward compatibility
// with bookmarks/links from the LightRAG era; they map to the same vector
// path here since pgvector has no graph-aware modes.
func search(db *sql.DB, query, mode string) []result {
	ctx := context.Background()
	searcher := library.NewSearcher(db)

	threshold := library.CosineThreshold
	opts := library.SearchOptions{
		TopK:      library.StrictTopK,
		Threshold: &threshold,
	}

	fmt.Fprintf(os.Stderr, "search: q=%q mode=%s\n", query, mode)

	candidates, err := searcher.Search(ctx, query, opts)
	if err != nil {
		log.Printf("search %q (mode=%s): %v", query, mode, err)
		return nil
	}

	var names []string
	if mode == "naive-verify" && len(candidates) > 0 {
		fmt.Fprintf(os.Stderr, "\n--- Verifying %d candidate(s) with LLM ---\n", len(candidates))
		verdicts, err := searcher.VerifyFilter(ctx, query, candidates)
		if err != nil {
			log.Printf("verify %q: %v", query, err)
			return nil
		}
		library.LogVerdicts(os.Stderr, verdicts)
		names = library.KeptNames(verdicts)
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
	return results
}
