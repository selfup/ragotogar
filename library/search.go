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
const (
	DefaultTopK       = 30
	StrictTopK        = 500
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
func (s *Searcher) Search(ctx context.Context, query string, opts SearchOptions) ([]Result, error) {
	embeddings, err := EmbedTexts(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	vec := pgvector.NewHalfVector(embeddings[0])

	rows, err := s.db.QueryContext(ctx, `
		SELECT name, MAX(1 - (embedding <=> $1)) AS similarity
		FROM chunks JOIN photos ON photos.id = chunks.photo_id
		GROUP BY name
		ORDER BY similarity DESC
		LIMIT $2
	`, vec, opts.TopK)
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

// searchFTS runs a Postgres full-text search against the descriptions.fts
// generated tsvector column. Returns only results above the adaptive
// FTSRelativeThreshold so the long tail of incidental token matches
// doesn't pollute the RRF fusion.
//
// Why adaptive: a query like "red truck" against this corpus produces real
// matches at ts_rank ≈ 0.33 alongside noise at ≈ 0.0 (descriptions where
// "red" and "truck" appear in unrelated sentences). A single-token query
// like "X100VI" gives every match a uniform low ts_rank ≈ 0.08. A flat
// cutoff over-prunes the latter; the relative cutoff handles both.
func (s *Searcher) searchFTS(ctx context.Context, query string, topK int, relThreshold float64) ([]Result, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.name, ts_rank(d.fts, plainto_tsquery('english', $1)) AS rank
		FROM descriptions d
		JOIN photos p ON p.id = d.photo_id
		WHERE d.fts @@ plainto_tsquery('english', $1)
		ORDER BY rank DESC
		LIMIT $2
	`, query, topK)
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
		// FTS doesn't honor opts.Threshold (different score space) but
		// does honor opts.TopK as the cap on per-lane retrieval before
		// fusion. opts.FTSThresholdRel = 0 means "use the package
		// default" so callers can leave it unset.
		topK := opts.TopK
		if topK <= 0 {
			topK = StrictTopK
		}
		relThreshold := opts.FTSThresholdRel
		if relThreshold <= 0 {
			relThreshold = FTSRelativeThreshold
		}
		r, err := s.searchFTS(ctx, query, topK, relThreshold)
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

	final := opts.TopK
	if final <= 0 {
		final = DefaultTopK
	}
	return rrfFuse([][]Result{vec.results, fts.results}, RRFK, final), nil
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
// the ✓/✗ stderr stream so callers can show progress).
type Verdict struct {
	Result Result
	YES    bool
	Raw    string // LLM response, truncated by the caller for display
}

// VerifyFilter runs the LLM yes/no check on each candidate's BuildDocument
// text. Concurrency is bounded by VerifyConcurrency. Verdicts come back in
// the original retrieval order so callers can display them in rank order.
func (s *Searcher) VerifyFilter(ctx context.Context, query string, candidates []Result) ([]Verdict, error) {
	model := SearchModel()
	verdicts := make([]Verdict, len(candidates))

	sem := make(chan struct{}, VerifyConcurrency)
	var wg sync.WaitGroup
	for i, c := range candidates {
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
		}()
	}
	wg.Wait()
	return verdicts, nil
}

// LogVerdicts streams the per-photo ✓/✗ verdict line to w (typically
// os.Stderr). Pulled out so callers can opt in or out of the progress
// chatter — cmd/search prints it; cmd/web suppresses it for HTTP responses.
func LogVerdicts(w *os.File, verdicts []Verdict) {
	for _, v := range verdicts {
		marker := "✓"
		if !v.YES {
			marker = "✗"
		}
		display := v.Raw
		if len(display) > 80 {
			display = display[:80]
		}
		fmt.Fprintf(w, "  %s %s: %s\n", marker, v.Result.Name, display)
	}
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
