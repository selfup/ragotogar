package library

import (
	"context"
	"database/sql"
	"fmt"
	"os"
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
)

// Result is a single ranked photo match. Similarity is in [0, 1] cosine space
// (1 = identical, 0 = orthogonal); higher is better.
type Result struct {
	Name       string
	Similarity float64
}

// SearchOptions controls Search behavior.
type SearchOptions struct {
	TopK      int
	Threshold *float64 // nil = return everything up to TopK; non-nil = post-filter on similarity
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
	vec := pgvector.NewVector(embeddings[0])

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
