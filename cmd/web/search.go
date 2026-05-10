package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	stdsort "sort"
	"time"

	"github.com/lib/pq"

	"ragotogar/library"
)

// searchParams bundles every URL knob the HTTP handler reads. Pulled out
// as a struct so the search() signature doesn't grow into 13+ positional
// args every time a new toggle ships.
type searchParams struct {
	query           string
	mode            string
	cosine          float64
	ftsRel          float64
	save            bool
	classify        bool
	saveClassify    bool
	useDescriptions bool
	useMetadata     bool
	useQueries      bool
	merge           library.MergeStrategy
	weightDesc      float64
	weightMeta      float64
	weightQueries   float64

	// backend swaps the retrieval source. "" or "pg" → in-process
	// SearchV2/SearchHybridV2 (default). "edge" → HTTP call to
	// cmd/edge's /search. The auto-rewrite, classifier filter, and
	// verify steps run in cmd/web regardless of backend, so all six
	// modes compose with both.
	backend string
	// edgeURL is the cmd/edge base URL (no trailing slash).
	// Effective only when backend == "edge". Empty string when -edge-url
	// flag wasn't passed at startup.
	edgeURL string
}

// search runs the appropriate retrieval pipeline for the given mode and
// returns matching photo names. All non-FTS modes use the v12 SearchV2
// path (per-store toggles + merge strategy); FTS+vector modes use
// SearchHybridV2 (same FTS arm as v1; vector lane is the merged v12 stores).
//
//   naive             : SearchV2 (per-store toggles, merged per strategy)
//   naive-verify      : SearchV2 + VerifyFilterV2 (verifier text mirrors toggles)
//   fts-vector        : SearchHybridV2 (RRF over v2 vector lane + FTS)
//   fts-vector-verify : SearchHybridV2 + VerifyFilterV2
//   auto / auto-verify: rewrite + fts-vector / fts-vector-verify
//
// cosine, ftsRel and per-store options are user-tunable via the UI form.
// searchResult bundles everything cmd/web's HTTP handler renders for a single
// query — the matching photos, the wall-clock duration, and (when the verify
// pass ran) the verify_cache stats so the template can show the hit rate.
type searchResult struct {
	Results  []result
	Elapsed  time.Duration
	Stats    *library.VerifyStats // nil when verify didn't run
	Rewrite  *rewriteView         // nil when no rewrite happened (mode wasn't auto, or input bypassed)
	Classify *classifyStatsView   // nil when the classifier filter didn't run (toggle off)
	Err      string               // non-empty when retrieval errored — UI surfaces this; e.g. cmd/edge's 400 on phrase queries.
}

func search(db *sql.DB, p searchParams) searchResult {
	start := time.Now()
	ctx := context.Background()
	searcher := library.NewSearcher(db)

	mode := p.mode
	query := p.query

	// Auto modes run the LLM rewrite first. The boolean output replaces the
	// original query for both retrieval arms; the result is surfaced in the
	// UI so the user can see what actually ran. save=true lets the cache
	// kick in (read existing, write new); save=false skips cache entirely
	// so the user can iterate to a good rewrite without sticky bad output.
	effectiveQuery := query
	var rwView *rewriteView
	if modeUsesRewrite(mode) {
		rw, err := library.RewriteQuery(ctx, db, query, library.ClassifyModel(), p.save)
		if err != nil {
			// Advisory — log and fall back to raw query. Search continues.
			log.Printf("rewrite %q (save=%v): %v", query, p.save, err)
		}
		if rw.Rewritten != "" && rw.Rewritten != query {
			effectiveQuery = rw.Rewritten
			rwView = &rewriteView{
				Rewritten: rw.Rewritten,
				Cached:    rw.Cached,
				Elapsed:   formatLatency(rw.Elapsed),
			}
		}
		// Auto modes always retrieve through the FTS+vector path — the
		// rewrite produces FTS-shaped output and vector-only would waste
		// the operators.
		mode = modeAfterRewrite(mode)
	}

	cosineCopy := p.cosine
	opts := library.SearchOptionsV2{
		// TopK left at 0 — cmd/web wants every match above the cosine
		// cutoff, not an arbitrary truncation. The cosine threshold
		// (and FTSRelativeThreshold for the FTS arm) is the bound.
		Threshold:          &cosineCopy,
		FTSThresholdRel:    p.ftsRel,
		VectorQuery:        library.StripNegation(effectiveQuery),
		UseDescriptions:    p.useDescriptions,
		UseMetadata:        p.useMetadata,
		UseQueries:         p.useQueries,
		MergeStrategy:      p.merge,
		WeightDescriptions: p.weightDesc,
		WeightMetadata:     p.weightMeta,
		WeightQueries:      p.weightQueries,
	}

	fmt.Fprintf(os.Stderr, "search: q=%q mode=%s cosine=%.2f fts=%.2f stores=[d=%v m=%v q=%v] merge=%s\n",
		effectiveQuery, mode, p.cosine, p.ftsRel,
		p.useDescriptions, p.useMetadata, p.useQueries, p.merge,
	)

	var (
		candidates []library.Result
		err        error
	)
	useEdge := p.backend == "edge" && p.edgeURL != ""
	switch {
	case useEdge:
		// Edge backend handles both vector and FTS+vector via its own
		// arm toggles; mode controls whether the FST arm participates.
		// retrieveFromEdge sends the same per-store + merge + cosine
		// params the in-process path uses.
		candidates, err = retrieveFromEdge(ctx, p.edgeURL, mode, effectiveQuery, opts)
	case mode == "fts-vector" || mode == "fts-vector-verify":
		candidates, err = searcher.SearchHybridV2(ctx, effectiveQuery, opts)
	default:
		candidates, err = searcher.SearchV2(ctx, effectiveQuery, opts)
	}
	if err != nil {
		log.Printf("search %q (mode=%s, backend=%s): %v", effectiveQuery, mode, p.backend, err)
		return searchResult{Elapsed: time.Since(start), Rewrite: rwView, Err: err.Error()}
	}

	// Classifier filter — runs BEFORE verify so the prose verify (if it runs)
	// only sees survivors. Cheap structured-input LLM call vs. per-candidate
	// prose check; doing classifier first reduces the verify-pass workload.
	// Uses the ORIGINAL NL query so the LLM judges intent, not boolean form.
	var classifyView *classifyStatsView
	if p.classify && len(candidates) > 0 {
		fmt.Fprintf(os.Stderr, "\n--- Classifier filter on %d candidate(s) ---\n", len(candidates))
		filtered, cstats, err := library.FilterByClassification(ctx, db, query, candidates, library.ClassifyModel(), p.saveClassify)
		if err != nil {
			// Advisory — log and proceed with unfiltered candidates.
			log.Printf("classify filter %q: %v", query, err)
		} else {
			candidates = filtered
		}
		fmt.Fprintf(os.Stderr, "classify filter: %d candidates · %d dropped · %d cached · %d LLM (%v)\n",
			cstats.Total, cstats.Dropped, cstats.Cached, cstats.LLM, cstats.Elapsed)
		classifyView = &classifyStatsView{
			Total:   cstats.Total,
			Dropped: cstats.Dropped,
			Cached:  cstats.Cached,
			LLM:     cstats.LLM,
			Elapsed: formatLatency(cstats.Elapsed),
		}
	}

	verify := mode == "naive-verify" || mode == "fts-vector-verify"

	var (
		names []string
		stats *library.VerifyStats
	)
	if verify && len(candidates) > 0 {
		// Verify uses the ORIGINAL natural-language query, not the boolean
		// rewrite. The rewrite is a retrieval mechanism; the source of
		// truth for "does this photo match what the user wanted" is what
		// they actually typed. Verifier text composition mirrors the
		// search-time toggles (queries always excluded — locked decision).
		fmt.Fprintf(os.Stderr, "\n--- Verifying %d candidate(s) with LLM ---\n", len(candidates))
		verdicts, s, err := searcher.VerifyFilterV2(ctx, query, candidates, opts)
		if err != nil {
			log.Printf("verify %q: %v", query, err)
			return searchResult{Elapsed: time.Since(start), Rewrite: rwView, Classify: classifyView}
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
	return searchResult{Results: results, Elapsed: time.Since(start), Stats: stats, Rewrite: rwView, Classify: classifyView}
}

// applySort reorders results by exif.date_taken when sort is "date-desc" or
// "date-asc". "relevance" leaves the slice as-is — retrieval order (cosine /
// RRF / verify) is the input. Photos with NULL date_taken sort to the end in
// both directions.
//
// One batched SELECT pulls dates for every name in the result set. Errors
// degrade to "relevance" rather than dropping results, since sort is a
// presentation concern and the matches themselves are still valid.
func applySort(db *sql.DB, results []result, sort string) []result {
	if sort == "relevance" || len(results) <= 1 {
		return results
	}
	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Name
	}
	dates, err := fetchDates(db, names)
	if err != nil {
		log.Printf("sort %s: fetch dates: %v — falling back to relevance", sort, err)
		return results
	}
	return sortByDate(results, dates, sort)
}

// fetchDates returns name → exif.date_taken (ISO 8601) for each name with a
// non-NULL date. Names absent from the map have no date and sort to the end.
func fetchDates(db *sql.DB, names []string) (map[string]string, error) {
	rows, err := db.Query(`
		SELECT p.name, e.date_taken
		FROM photos p
		LEFT JOIN exif e ON e.photo_id = p.id
		WHERE p.name = ANY($1)
	`, pq.Array(names))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string, len(names))
	for rows.Next() {
		var (
			name string
			d    sql.NullString
		)
		if err := rows.Scan(&name, &d); err != nil {
			return nil, err
		}
		if d.Valid && d.String != "" {
			out[name] = d.String
		}
	}
	return out, rows.Err()
}

// sortByDate is the pure reorder step — no DB. Stable so retrieval order
// breaks ties between identical dates (and between two dateless results).
// date_taken is ISO 8601 ("2024-04-15T10:23:14"), so lexical compare matches
// chronological compare. Anything other than "date-desc" / "date-asc" is a
// no-op (returns a copy in retrieval order).
func sortByDate(results []result, dates map[string]string, sort string) []result {
	out := make([]result, len(results))
	copy(out, results)
	if sort != "date-desc" && sort != "date-asc" {
		return out
	}
	stdsort.SliceStable(out, func(i, j int) bool {
		di, hasI := dates[out[i].Name]
		dj, hasJ := dates[out[j].Name]
		// NULL dates always end up at the tail.
		if !hasI && !hasJ {
			return false
		}
		if !hasI {
			return false
		}
		if !hasJ {
			return true
		}
		if sort == "date-asc" {
			return di < dj
		}
		return di > dj
	})
	return out
}
