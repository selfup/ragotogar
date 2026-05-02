package library

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/pgvector/pgvector-go"
)

// Search defaults — exported so cmd/search and cmd/web both pull from the
// same source of truth instead of redefining magic numbers.
//
// TopK = 0 is the "unbounded" sentinel: Search / searchFTS skip the LIMIT
// clause entirely and SearchHybrid skips the post-fusion cap. The cosine
// threshold (and, for FTS, FTSRelativeThreshold) becomes the only bound on
// result size. cmd/web and cmd/search's -retrieve/-precise/-hybrid paths
// rely on this — they want every match above the cutoff, not an arbitrary
// truncation.
const (
	DefaultTopK       = 30
	CosineThreshold   = 0.5 // applied in retrieve / precise modes
	VerifyConcurrency = 8

	// Reciprocal Rank Fusion constant. The standard value from the original
	// RRF paper (Cormack et al. 2009) — 60 dampens the contribution of
	// any single list's top hit so neither vector nor FTS dominates.
	RRFK = 60.0

	// FTSRelativeThreshold is the floor for FTS results expressed as a
	// fraction of the strongest match's ts_rank. ts_rank values are
	// query-shape dependent (a single rare token can score ~0.07 while a
	// dense phrase can score ~0.5), so a fixed cutoff over- or
	// under-prunes depending on query. Keeping only results at ≥30% of the
	// max-in-set drops the long tail of incidental token matches without
	// over-filtering single-token queries where every match is naturally
	// the same low rank.
	FTSRelativeThreshold = 0.3
)

// Result is a single ranked photo match. Similarity is in [0, 1] cosine space
// (1 = identical, 0 = orthogonal); higher is better.
type Result struct {
	Name       string
	Similarity float64
}

// SearchOptions controls Search and SearchHybrid behavior.
type SearchOptions struct {
	TopK             int
	Threshold        *float64 // vector cosine cutoff. nil = return everything up to TopK; non-nil = post-filter on similarity
	FTSThresholdRel  float64  // FTS adaptive threshold ratio for SearchHybrid. 0 = use FTSRelativeThreshold (the package default)
}

// Searcher wraps a *sql.DB for the vector retrieval + verify pipeline. It's
// cheap to construct and safe for concurrent use (database/sql handles its
// own pooling). cmd/web instantiates one at startup; cmd/search makes one
// per CLI invocation.
type Searcher struct {
	db *sql.DB
}

func NewSearcher(db *sql.DB) *Searcher {
	return &Searcher{db: db}
}

// Search embeds the query, runs a single SQL roundtrip against the chunks
// table, and returns the per-photo best-chunk similarity in descending order.
// Filters by Threshold when non-nil. Caller decides whether to feed the
// results into VerifyFilter.
//
// opts.TopK <= 0 means unbounded — the LIMIT clause is dropped and the only
// cap on the result set is the optional Threshold. Otherwise LIMIT $2 is
// appended. The branch keeps the SQL static (no string interpolation of
// untrusted values) while still letting "give me everything above 0.5" be
// expressible.
func (s *Searcher) Search(ctx context.Context, query string, opts SearchOptions) ([]Result, error) {
	embeddings, err := EmbedTexts(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	vec := pgvector.NewHalfVector(embeddings[0])

	const baseSQL = `
		SELECT name, MAX(1 - (embedding <=> $1)) AS similarity
		FROM chunks JOIN photos ON photos.id = chunks.photo_id
		GROUP BY name
		ORDER BY similarity DESC`
	var rows *sql.Rows
	if opts.TopK > 0 {
		rows, err = s.db.QueryContext(ctx, baseSQL+" LIMIT $2", vec, opts.TopK)
	} else {
		rows, err = s.db.QueryContext(ctx, baseSQL, vec)
	}
	if err != nil {
		return nil, fmt.Errorf("vector query: %w", err)
	}
	defer rows.Close()

	var out []Result
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.Name, &r.Similarity); err != nil {
			return nil, err
		}
		if opts.Threshold != nil && r.Similarity < *opts.Threshold {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// searchFTS runs a Postgres full-text search across both descriptions.fts
// (LLM prose) and exif.fts (camera/lens/year/software/artist metadata).
// The two tsvectors are concatenated at query time (`d.fts || e.fts`) so a
// multi-word query like "X100VI bedroom" — where one token lives in metadata
// and the other in prose — matches via plainto_tsquery's implicit AND.
//
// Why concat instead of `WHERE d.fts @@ q OR e.fts @@ q`: the OR form only
// matches when a single column carries all query tokens. plainto_tsquery
// AND's its terms, so a cross-column query collapses to zero hits under OR.
// Concat fuses the two columns into one effective tsvector, restoring the
// expected behavior. Trade-off: the per-table GIN indexes can't help the
// concatenated expression, so this degrades to a sequential scan over
// (descriptions ⨝ exif). At ~30K photos that's still <100 ms; if it ever
// becomes the dominant cost, materialize the union onto photos.fts.
//
// Returns only results above the adaptive FTSRelativeThreshold so the long
// tail of incidental token matches doesn't pollute the RRF fusion. Adaptive
// because a query like "red truck" produces real matches at ts_rank ≈ 0.33
// alongside noise at ≈ 0.0; a single-token query like "X100VI" gives every
// match a uniform low ts_rank ≈ 0.08. A flat cutoff over-prunes the latter;
// the relative cutoff handles both.
func (s *Searcher) searchFTS(ctx context.Context, query string, topK int, relThreshold float64) ([]Result, error) {
	const baseSQL = `
		SELECT p.name,
		       ts_rank(
		           COALESCE(d.fts, ''::tsvector) || COALESCE(e.fts, ''::tsvector),
		           plainto_tsquery('english', $1)
		       ) AS rank
		FROM photos p
		LEFT JOIN descriptions d ON p.id = d.photo_id
		LEFT JOIN exif e         ON p.id = e.photo_id
		WHERE (COALESCE(d.fts, ''::tsvector) || COALESCE(e.fts, ''::tsvector))
		      @@ plainto_tsquery('english', $1)
		ORDER BY rank DESC`
	var (
		rows *sql.Rows
		err  error
	)
	if topK > 0 {
		rows, err = s.db.QueryContext(ctx, baseSQL+" LIMIT $2", query, topK)
	} else {
		rows, err = s.db.QueryContext(ctx, baseSQL, query)
	}
	if err != nil {
		return nil, fmt.Errorf("fts query: %w", err)
	}
	defer rows.Close()

	var raw []Result
	for rows.Next() {
		var r Result
		// ts_rank stashed in Similarity so the threshold filter below can
		// see the score. RRF only uses rank position, not the value.
		if err := rows.Scan(&r.Name, &r.Similarity); err != nil {
			return nil, err
		}
		raw = append(raw, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return raw, nil
	}

	maxRank := raw[0].Similarity // rows sorted DESC, first row is the max
	threshold := relThreshold * maxRank
	kept := raw[:0]
	for _, r := range raw {
		if r.Similarity >= threshold {
			kept = append(kept, r)
		}
	}
	return kept, nil
}

// SearchHybrid runs vector retrieval + Postgres full-text search in
// parallel, then fuses the two ranked lists via Reciprocal Rank Fusion.
// FTS catches literal-text queries the vector misses (model names,
// place names, person names typed into the description); vector catches
// the semantic shape ("warm light bedroom") that FTS can't reason about.
//
// The vector arm uses Search with the same TopK + Threshold the caller
// would pass to a pure vector search. The FTS arm pulls up to TopK
// matches above zero ts_rank. RRF combines, dedupes, returns up to
// opts.TopK results ordered by fused score.
func (s *Searcher) SearchHybrid(ctx context.Context, query string, opts SearchOptions) ([]Result, error) {
	type lane struct {
		results []Result
		err     error
	}
	vecCh := make(chan lane, 1)
	ftsCh := make(chan lane, 1)

	go func() {
		r, err := s.Search(ctx, query, opts)
		vecCh <- lane{r, err}
	}()
	go func() {
		// FTS honors opts.TopK as a per-lane cap; 0 propagates as
		// "unbounded" the same way Search treats it. opts.FTSThresholdRel
		// = 0 means "use the package default" so callers can leave it
		// unset.
		relThreshold := opts.FTSThresholdRel
		if relThreshold <= 0 {
			relThreshold = FTSRelativeThreshold
		}
		r, err := s.searchFTS(ctx, query, opts.TopK, relThreshold)
		ftsCh <- lane{r, err}
	}()

	vec := <-vecCh
	fts := <-ftsCh
	if vec.err != nil {
		return nil, vec.err
	}
	if fts.err != nil {
		return nil, fts.err
	}

	// opts.TopK <= 0 → no post-fusion cap; rrfFuse already handles that
	// via its own topK<=0 branch.
	return rrfFuse([][]Result{vec.results, fts.results}, RRFK, opts.TopK), nil
}

// rrfFuse implements Reciprocal Rank Fusion (Cormack et al. 2009).
// Each input list is treated as a ranking; a document's fused score is
// the sum over lists of `1 / (k + rank_in_list)` (rank is 1-indexed).
// Documents that appear in both lists naturally rise to the top.
//
// Similarity for the fused output preserves the highest similarity seen
// across lists — typically the cosine similarity from the vector arm
// for shared docs, or 0 (the unset default for FTS-only docs).
func rrfFuse(lists [][]Result, k float64, topK int) []Result {
	scores := make(map[string]float64)
	sims := make(map[string]float64)
	for _, list := range lists {
		for rank, r := range list {
			scores[r.Name] += 1.0 / (k + float64(rank+1))
			if r.Similarity > sims[r.Name] {
				sims[r.Name] = r.Similarity
			}
		}
	}
	type scored struct {
		name  string
		score float64
	}
	ranked := make([]scored, 0, len(scores))
	for name, score := range scores {
		ranked = append(ranked, scored{name, score})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if topK > 0 && len(ranked) > topK {
		ranked = ranked[:topK]
	}
	out := make([]Result, len(ranked))
	for i, r := range ranked {
		out[i] = Result{Name: r.name, Similarity: sims[r.name]}
	}
	return out
}

// VerifyPrompt is the LLM yes/no template applied to each candidate's
// BuildDocument text. Recall-biased on purpose — precision comes from the
// cosine threshold upstream of verify, so the verify pass mostly weeds out
// the "vector matched 'red' somewhere in the description" false positives.
const VerifyPrompt = `Determine if a photo is relevant to a search query.

Query: %s

Photo data (camera, settings, date, software, photographer, and visual description):
%s

If the data mentions or shows what the query is about — even as a small,
background, or partial element, or via metadata like camera/lens/date/settings —
answer YES. Only answer NO if the photo is clearly unrelated to the query.

Reply with exactly one word: YES or NO.`

// Verdict carries the LLM's per-photo answer plus the raw response (used for
// the ✓/✗ stderr stream so callers can show progress). FromCache flags the
// rows that came back from verify_cache without an LLM call — used by the
// per-row ✓/✗ stream to mark cached rows and by VerifyStats aggregation.
type Verdict struct {
	Result    Result
	YES       bool
	Raw       string // LLM response, truncated by the caller for display
	FromCache bool
}

// VerifyFilter runs the LLM yes/no check on each candidate's BuildDocument
// text, consulting verify_cache before each LLM call. Concurrency is bounded
// by VerifyConcurrency. Verdicts come back in the original retrieval order
// so callers can display them in rank order. The returned VerifyStats counts
// cache hits vs LLM calls — caller surfaces the hit rate to UI / logs.
//
// Cache lookup is one batch query keyed on (canonical_query, photo_ids,
// verify_model). Rows where the photo has been re-described after the verdict
// was written are silently skipped (see verify_cache.go) so a re-describe
// transparently invalidates stale cache entries without explicit teardown.
//
// Errors (LoadPhoto failure, LLM failure) produce verdicts with YES=false but
// are NOT cached — only successful LLM responses become persistent rows.
func (s *Searcher) VerifyFilter(ctx context.Context, query string, candidates []Result) ([]Verdict, VerifyStats, error) {
	model := SearchModel()
	canonical := CanonicalQuery(query)
	verdicts := make([]Verdict, len(candidates))
	stats := VerifyStats{Total: len(candidates)}

	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.Name
	}
	cached, err := lookupVerifyCache(ctx, s.db, canonical, ids, model)
	if err != nil {
		// Cache lookup failure is non-fatal — fall through to LLM-only
		// behavior. Logging is the caller's job.
		cached = map[string]bool{}
	}

	sem := make(chan struct{}, VerifyConcurrency)
	var (
		wg sync.WaitGroup
		mu sync.Mutex // guards stats.Cached / stats.LLM
	)
	for i, c := range candidates {
		if v, hit := cached[c.Name]; hit {
			mark := "yes"
			if !v {
				mark = "no"
			}
			verdicts[i] = Verdict{Result: c, YES: v, Raw: "(cached " + mark + ")", FromCache: true}
			stats.Cached++
			continue
		}

		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			photo, err := LoadPhoto(s.db, c.Name)
			if err != nil {
				verdicts[i] = Verdict{Result: c, YES: false, Raw: "(no photo)"}
				return
			}
			doc := BuildDocument(photo)
			if len(doc) > 3000 {
				doc = doc[:3000]
			}
			prompt := fmt.Sprintf(VerifyPrompt, query, doc)
			resp, err := LLMComplete(ctx, model, prompt)
			if err != nil {
				verdicts[i] = Verdict{Result: c, YES: false, Raw: fmt.Sprintf("(error: %v)", err)}
				return
			}
			ok := strings.HasPrefix(strings.ToUpper(strings.TrimSpace(resp)), "Y")
			verdicts[i] = Verdict{Result: c, YES: ok, Raw: resp}
			mu.Lock()
			stats.LLM++
			mu.Unlock()

			// Best-effort cache write — failure here doesn't poison the
			// verdict, just means the next identical query will re-run
			// the LLM.
			if err := writeVerifyCache(ctx, s.db, canonical, c.Name, model, ok); err != nil {
				fmt.Fprintf(os.Stderr, "verify_cache write %q/%s: %v\n", canonical, c.Name, err)
			}
		}()
	}
	wg.Wait()
	return verdicts, stats, nil
}

// LogVerdicts streams the per-photo ✓/✗ verdict line to w (typically
// os.Stderr). Pulled out so callers can opt in or out of the progress
// chatter — cmd/search prints it; cmd/web suppresses it for HTTP responses.
// Cached verdicts get a [c] tag so the user can eyeball cache hit density
// without parsing the trailing stats line.
func LogVerdicts(w *os.File, verdicts []Verdict) {
	for _, v := range verdicts {
		marker := "✓"
		if !v.YES {
			marker = "✗"
		}
		tag := ""
		if v.FromCache {
			tag = " [c]"
		}
		display := v.Raw
		if len(display) > 80 {
			display = display[:80]
		}
		fmt.Fprintf(w, "  %s%s %s: %s\n", marker, tag, v.Result.Name, display)
	}
}

// LogVerifyStats prints the one-line cache summary to w. Format matches the
// telemetry the cmd/web template renders, so the CLI and UI stay legible the
// same way.
func LogVerifyStats(w *os.File, stats VerifyStats) {
	if stats.Total == 0 {
		return
	}
	fmt.Fprintf(w, "verify: %d candidates · %d cached · %d LLM · %.0f%% hit\n",
		stats.Total, stats.Cached, stats.LLM, stats.HitRate()*100)
}

// KeptNames extracts just the YES verdicts' names in retrieval order. Most
// callers (cmd/web for the grid, cmd/search for the printed list) want
// exactly this slice.
func KeptNames(verdicts []Verdict) []string {
	var out []string
	for _, v := range verdicts {
		if v.YES {
			out = append(out, v.Result.Name)
		}
	}
	return out
}
