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
	Results  []result
	Elapsed  time.Duration
	Stats    *library.VerifyStats // nil when verify didn't run
	Rewrite  *rewriteView         // nil when no rewrite happened (mode wasn't auto, or input bypassed)
	Classify *classifyStatsView   // nil when the classifier filter didn't run (toggle off)
}

func search(db *sql.DB, query, mode string, cosine, ftsRel float64, save, classify, saveClassify bool) searchResult {
	start := time.Now()
	ctx := context.Background()
	searcher := library.NewSearcher(db)

	// Auto modes run the LLM rewrite first. The boolean output replaces the
	// original query for both retrieval arms; the result is surfaced in the
	// UI so the user can see what actually ran. save=true lets the cache
	// kick in (read existing, write new); save=false skips cache entirely
	// so the user can iterate to a good rewrite without sticky bad output.
	effectiveQuery := query
	var rwView *rewriteView
	if modeUsesRewrite(mode) {
		rw, err := library.RewriteQuery(ctx, db, query, library.ClassifyModel(), save)
		if err != nil {
			// Advisory — log and fall back to raw query. Search continues.
			log.Printf("rewrite %q (save=%v): %v", query, save, err)
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

	cosineCopy := cosine
	opts := library.SearchOptions{
		// TopK left at 0 — cmd/web wants every match above the cosine
		// cutoff, not an arbitrary truncation. The cosine threshold
		// (and FTSRelativeThreshold for the FTS arm) is the bound.
		Threshold:       &cosineCopy,
		FTSThresholdRel: ftsRel,
		// VectorQuery is the query with websearch NOT operators stripped —
		// the FTS arm sees the original string (boolean form intact) but
		// the embedder gets just the positive residual so `-monochrome`
		// doesn't bias the embedding toward monochrome.
		VectorQuery: library.StripNegation(effectiveQuery),
	}

	fmt.Fprintf(os.Stderr, "search: q=%q mode=%s cosine=%.2f fts=%.2f\n", effectiveQuery, mode, cosine, ftsRel)

	var (
		candidates []library.Result
		err        error
	)
	switch mode {
	case "fts-vector", "fts-vector-verify":
		candidates, err = searcher.SearchHybrid(ctx, effectiveQuery, opts)
	default:
		candidates, err = searcher.Search(ctx, effectiveQuery, opts)
	}
	if err != nil {
		log.Printf("search %q (mode=%s): %v", effectiveQuery, mode, err)
		return searchResult{Elapsed: time.Since(start), Rewrite: rwView}
	}

	// Classifier filter — runs BEFORE verify so the prose verify (if it runs)
	// only sees survivors. Cheap structured-input LLM call vs. per-candidate
	// prose check; doing classifier first reduces the verify-pass workload.
	// Uses the ORIGINAL NL query so the LLM judges intent, not boolean form.
	var classifyView *classifyStatsView
	if classify && len(candidates) > 0 {
		fmt.Fprintf(os.Stderr, "\n--- Classifier filter on %d candidate(s) ---\n", len(candidates))
		filtered, cstats, err := library.FilterByClassification(ctx, db, query, candidates, library.ClassifyModel(), saveClassify)
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
		// they actually typed.
		fmt.Fprintf(os.Stderr, "\n--- Verifying %d candidate(s) with LLM ---\n", len(candidates))
		verdicts, s, err := searcher.VerifyFilter(ctx, query, candidates)
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
