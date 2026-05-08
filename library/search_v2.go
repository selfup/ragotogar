package library

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/pgvector/pgvector-go"
)

// MergeStrategy controls how SearchV2 combines results from the three
// per-store ANN queries (photo_descriptions, photo_metadata, photo_queries).
// See merge* helpers below for the per-strategy math.
type MergeStrategy string

const (
	// MergeUnion: photo IDs from any enabled store are candidates,
	// deduplicated. A photo's similarity is the max across the stores it
	// appeared in. Default — broadest recall.
	MergeUnion MergeStrategy = "union"

	// MergeIntersect: only photos appearing in results from all enabled
	// stores survive. Their similarity is the per-store average. Strictest
	// precision; useful when you want every signal to corroborate.
	MergeIntersect MergeStrategy = "intersect"

	// MergeWeighted: each store contributes (similarity * weight) to a
	// per-photo sum. Photos appearing in more stores naturally rise to the
	// top. Use to bias retrieval toward (e.g.) the queries store while
	// still letting descriptions and metadata participate.
	MergeWeighted MergeStrategy = "weighted"
)

// SearchOptionsV2 controls SearchV2 / SearchHybridV2 / VerifyFilterV2.
// Per-store toggles + merge strategy + per-store weights. Verifier text
// composition is derived from the same UseDescriptions / UseMetadata
// toggles — see ARCHITECTURE.md Pillar 0.
//
// All bool fields default false on a zero-value struct, which would
// silently disable every store. Use DefaultSearchOptionsV2 instead.
type SearchOptionsV2 struct {
	TopK      int
	Threshold *float64 // per-store cosine cutoff. nil = no per-store filter; non-nil = drop rows with similarity < *Threshold before merge.
	// VectorQuery is the positive-residual embed input (negation tokens
	// removed by StripNegation). FTS arm in SearchHybridV2 still sees the
	// original query.
	VectorQuery string

	UseDescriptions bool
	UseMetadata     bool
	UseQueries      bool

	MergeStrategy MergeStrategy

	// Weights for MergeWeighted. Other strategies ignore them. Zero/negative
	// weights effectively exclude a store from the weighted sum.
	WeightDescriptions float64
	WeightMetadata     float64
	WeightQueries      float64

	// FTSThresholdRel is the FTS adaptive threshold for SearchHybridV2's
	// FTS arm. 0 = use FTSRelativeThreshold (the package default).
	FTSThresholdRel float64
}

// DefaultSearchOptionsV2 returns a fully-enabled options struct: all three
// stores on, union merge, equal weights, default TopK. Callers tweak from
// there.
func DefaultSearchOptionsV2() SearchOptionsV2 {
	return SearchOptionsV2{
		TopK:               DefaultTopK,
		UseDescriptions:    true,
		UseMetadata:        true,
		UseQueries:         true,
		MergeStrategy:      MergeUnion,
		WeightDescriptions: 1.0,
		WeightMetadata:     1.0,
		WeightQueries:      1.0,
	}
}

// SearchV2 retrieves from the three v12 stores in parallel and merges
// per the configured strategy. Returns []Result ranked by merged score.
// Negation post-filter (matching v1 behavior) drops candidates whose
// description / exif text matches any of the websearch NOT operators in
// the original query.
//
// Empty options (all UseX = false) returns nil with no error — a no-op
// search lets the caller surface the configuration mistake without
// crashing.
func (s *Searcher) SearchV2(ctx context.Context, query string, opts SearchOptionsV2) ([]Result, error) {
	if !opts.UseDescriptions && !opts.UseMetadata && !opts.UseQueries {
		return nil, nil
	}

	embedInput := opts.VectorQuery
	if embedInput == "" {
		embedInput = query
	}
	negation := ExtractNegation(query)

	embeddings, err := EmbedTexts(ctx, []string{embedInput})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	vec := pgvector.NewHalfVector(embeddings[0])

	// Each enabled store runs in its own goroutine. Parallelism here is
	// cheap on the DB side (one ANN query per store, three at most) and
	// matters mostly when the embed endpoint shares HTTP plumbing with
	// the rest of the request path.
	type lane struct {
		store   string
		results []Result
		err     error
	}
	laneCh := make(chan lane, 3)
	enabled := 0
	if opts.UseDescriptions {
		enabled++
		go func() {
			r, err := searchStoreDescriptions(ctx, s.db, vec, opts.TopK, opts.Threshold)
			laneCh <- lane{store: "descriptions", results: r, err: err}
		}()
	}
	if opts.UseMetadata {
		enabled++
		go func() {
			r, err := searchStoreMetadata(ctx, s.db, vec, opts.TopK, opts.Threshold)
			laneCh <- lane{store: "metadata", results: r, err: err}
		}()
	}
	if opts.UseQueries {
		enabled++
		go func() {
			r, err := searchStoreQueries(ctx, s.db, vec, opts.TopK, opts.Threshold)
			laneCh <- lane{store: "queries", results: r, err: err}
		}()
	}

	storeResults := map[string][]Result{}
	for i := 0; i < enabled; i++ {
		l := <-laneCh
		if l.err != nil {
			return nil, fmt.Errorf("%s store: %w", l.store, l.err)
		}
		storeResults[l.store] = l.results
	}

	merged := mergeStores(storeResults, opts)

	if negation != "" && len(merged) > 0 {
		merged, err = s.filterByNegation(ctx, merged, negation)
		if err != nil {
			return nil, fmt.Errorf("negation filter: %w", err)
		}
	}

	if opts.TopK > 0 && len(merged) > opts.TopK {
		merged = merged[:opts.TopK]
	}

	return merged, nil
}

// SearchHybridV2 combines SearchV2's three-store vector lane with the
// existing FTS arm via Reciprocal Rank Fusion. FTS is unchanged from v1
// (descriptions.fts ‖ exif.fts), since Steps 1–4 left the FTS surface
// alone per locked decision #9.
func (s *Searcher) SearchHybridV2(ctx context.Context, query string, opts SearchOptionsV2) ([]Result, error) {
	type lane struct {
		results []Result
		err     error
	}
	vecCh := make(chan lane, 1)
	ftsCh := make(chan lane, 1)

	go func() {
		r, err := s.SearchV2(ctx, query, opts)
		vecCh <- lane{r, err}
	}()
	go func() {
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

	return rrfFuse([][]Result{vec.results, fts.results}, RRFK, opts.TopK), nil
}

// VerifyFilterV2 runs the LLM yes/no check using a per-photo text
// composed from the v12 builders, gated by the search-time toggles:
//   - UseDescriptions on  → BuildDescriptionDocument is included
//   - UseMetadata on      → BuildMetadataDocument is included (joined with
//     "\n\n" if descriptions also on)
//   - UseQueries          → ignored (queries are excluded from verify per
//     the locked decision: verifier never sees its own training-target text)
//
// If both UseDescriptions and UseMetadata are off, verifier sees an empty
// document — the LLM will mostly say "NO" given empty context, which is
// the right outcome (you can't verify what you didn't retrieve).
func (s *Searcher) VerifyFilterV2(ctx context.Context, query string, candidates []Result, opts SearchOptionsV2) ([]Verdict, VerifyStats, error) {
	builder := func(p *Photo) string { return buildVerifyTextV2(p, opts) }
	return s.runVerify(ctx, query, candidates, builder)
}

// buildVerifyTextV2 assembles the per-candidate verifier prompt body from
// the v12 store builders. See VerifyFilterV2 for the toggle semantics.
func buildVerifyTextV2(p *Photo, opts SearchOptionsV2) string {
	var parts []string
	if opts.UseDescriptions {
		if d := BuildDescriptionDocument(p); d != "" {
			parts = append(parts, d)
		}
	}
	if opts.UseMetadata {
		if m := BuildMetadataDocument(p); m != "" {
			parts = append(parts, m)
		}
	}
	return strings.Join(parts, "\n\n")
}

// searchStoreDescriptions runs the photo_descriptions ANN query. Multiple
// chunks per photo collapse to one row via MAX(similarity).
func searchStoreDescriptions(ctx context.Context, db *sql.DB, vec pgvector.HalfVector, topK int, threshold *float64) ([]Result, error) {
	const baseSQL = `
		SELECT p.name, MAX(1 - (pd.embedding <=> $1)) AS similarity
		FROM photo_descriptions pd
		JOIN photos p ON p.id = pd.photo_id
		WHERE pd.schema_version = $2
		GROUP BY p.name
		ORDER BY similarity DESC`
	return runStoreQuery(ctx, db, baseSQL, vec, topK, threshold)
}

// searchStoreMetadata runs the photo_metadata ANN query. One row per
// photo per schema_version, so no GROUP BY needed.
func searchStoreMetadata(ctx context.Context, db *sql.DB, vec pgvector.HalfVector, topK int, threshold *float64) ([]Result, error) {
	const baseSQL = `
		SELECT p.name, 1 - (pm.embedding <=> $1) AS similarity
		FROM photo_metadata pm
		JOIN photos p ON p.id = pm.photo_id
		WHERE pm.schema_version = $2
		ORDER BY similarity DESC`
	return runStoreQuery(ctx, db, baseSQL, vec, topK, threshold)
}

// searchStoreQueries runs the photo_queries ANN query. Multiple
// query phrasings per photo collapse to one row via MAX(similarity) — the
// best-matching phrasing wins, same shape as the descriptions store.
func searchStoreQueries(ctx context.Context, db *sql.DB, vec pgvector.HalfVector, topK int, threshold *float64) ([]Result, error) {
	const baseSQL = `
		SELECT p.name, MAX(1 - (pq.embedding <=> $1)) AS similarity
		FROM photo_queries pq
		JOIN photos p ON p.id = pq.photo_id
		WHERE pq.schema_version = $2
		GROUP BY p.name
		ORDER BY similarity DESC`
	return runStoreQuery(ctx, db, baseSQL, vec, topK, threshold)
}

// runStoreQuery is the shared executor for the three per-store ANN
// queries. baseSQL must use $1 for the query vector and $2 for
// schema_version; LIMIT is appended when topK > 0. Threshold is applied
// in Go (not SQL) so the same SQL works whether or not the caller wants
// a cutoff.
func runStoreQuery(ctx context.Context, db *sql.DB, baseSQL string, vec pgvector.HalfVector, topK int, threshold *float64) ([]Result, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if topK > 0 {
		rows, err = db.QueryContext(ctx, baseSQL+" LIMIT $3", vec, v2SchemaVersion, topK)
	} else {
		rows, err = db.QueryContext(ctx, baseSQL, vec, v2SchemaVersion)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Result
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.Name, &r.Similarity); err != nil {
			return nil, err
		}
		if threshold != nil && r.Similarity < *threshold {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// v2SchemaVersion mirrors cmd/index's const so the search side reads
// rows the indexer wrote. Bumping in lockstep is intentional; declared
// twice rather than exporting from cmd/index because cmd/index can't be
// imported by library (cmd/* would create a cycle).
const v2SchemaVersion = 2

// mergeStores combines per-store result lists into a single ranked list
// per the chosen strategy. Each strategy has different score semantics
// (max-similarity / avg-similarity / weighted-sum) so the resulting
// .Similarity values aren't directly comparable across strategies — the
// rank is what matters.
func mergeStores(storeResults map[string][]Result, opts SearchOptionsV2) []Result {
	switch opts.MergeStrategy {
	case MergeIntersect:
		return mergeIntersect(storeResults)
	case MergeWeighted:
		return mergeWeighted(storeResults, opts)
	case MergeUnion, "":
		return mergeUnion(storeResults)
	default:
		// Unknown strategy — fall back to union for safety, same as how
		// most option-pickers behave under malformed input.
		return mergeUnion(storeResults)
	}
}

// mergeUnion: take every photo from any enabled store. A photo's
// similarity is the max across the stores it appeared in. Sorted by
// similarity DESC.
func mergeUnion(storeResults map[string][]Result) []Result {
	bestSim := map[string]float64{}
	for _, list := range storeResults {
		for _, r := range list {
			if r.Similarity > bestSim[r.Name] {
				bestSim[r.Name] = r.Similarity
			}
		}
	}
	out := make([]Result, 0, len(bestSim))
	for name, sim := range bestSim {
		out = append(out, Result{Name: name, Similarity: sim})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	return out
}

// mergeIntersect: keep only photos that appear in every enabled store's
// results. Score is the per-store mean similarity (compromise between
// MIN — too conservative — and MAX — defeats the point of intersection).
func mergeIntersect(storeResults map[string][]Result) []Result {
	if len(storeResults) == 0 {
		return nil
	}
	// Per-store presence + accumulated similarity.
	presence := map[string]int{}
	accum := map[string]float64{}
	for _, list := range storeResults {
		seen := map[string]bool{}
		for _, r := range list {
			if seen[r.Name] {
				continue
			}
			seen[r.Name] = true
			presence[r.Name]++
			accum[r.Name] += r.Similarity
		}
	}
	required := len(storeResults)
	out := make([]Result, 0)
	for name, count := range presence {
		if count == required {
			out = append(out, Result{Name: name, Similarity: accum[name] / float64(required)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	return out
}

// mergeWeighted: photo's score = sum over stores it appeared in of
// (similarity * weight). Photos appearing in multiple stores naturally
// rise; weights let callers bias toward queries (or descriptions, etc).
// Stores with weight ≤ 0 contribute nothing to the sum, but their
// presence in storeResults still counts toward "appeared in" semantics —
// callers who want to silence a store should disable it via UseX = false
// rather than zero its weight.
func mergeWeighted(storeResults map[string][]Result, opts SearchOptionsV2) []Result {
	weight := map[string]float64{
		"descriptions": opts.WeightDescriptions,
		"metadata":     opts.WeightMetadata,
		"queries":      opts.WeightQueries,
	}
	scores := map[string]float64{}
	for store, list := range storeResults {
		w := weight[store]
		seen := map[string]bool{}
		for _, r := range list {
			if seen[r.Name] {
				continue
			}
			seen[r.Name] = true
			scores[r.Name] += r.Similarity * w
		}
	}
	out := make([]Result, 0, len(scores))
	for name, score := range scores {
		out = append(out, Result{Name: name, Similarity: score})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	return out
}
