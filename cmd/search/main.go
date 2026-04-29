// cmd/search — pgvector cosine search over the chunks table, with an
// optional LLM yes/no verify pass.
//
// Usage:
//   go run ./cmd/search "warm light bedroom"
//   go run ./cmd/search -retrieve "shallow depth of field"
//   go run ./cmd/search -retrieve -verify "April photos with trees"
//   go run ./cmd/search -precise "indoor scenes"
//
// Flags:
//   -dsn DSN         Postgres library DSN (overrides LIBRARY_DSN)
//   -retrieve        Top-500 vector retrieval, cosine ≥ 0.5, no LLM (cmd/web uses this)
//   -precise         Same as -retrieve (kept for parity with the old Python tool)
//   -verify          With -retrieve / -precise: run an LLM yes/no per candidate
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pgvector/pgvector-go"

	"ragotogar/internal/library"
)

const (
	defaultTopK     = 30
	strictTopK      = 500
	cosineThreshold = 0.5

	verifyConcurrency = 8
)

const verifyPrompt = `Determine if a photo is relevant to a search query.

Query: %s

Photo data (camera, settings, date, software, photographer, and visual description):
%s

If the data mentions or shows what the query is about — even as a small,
background, or partial element, or via metadata like camera/lens/date/settings —
answer YES. Only answer NO if the photo is clearly unrelated to the query.

Reply with exactly one word: YES or NO.`

func main() {
	var (
		dsn      = flag.String("dsn", library.DefaultDSN(), "Postgres library DSN (overrides LIBRARY_DSN env var)")
		retrieve = flag.Bool("retrieve", false, "Top-500 vector retrieval, cosine ≥ 0.5, no LLM synthesis")
		precise  = flag.Bool("precise", false, "Strict retrieval (cosine ≥ 0.5); alias for -retrieve")
		verify   = flag.Bool("verify", false, "With -retrieve/-precise: run an LLM yes/no check per candidate, keep only YES")
	)
	// argparse-style suppressed flag for backward compat with old call sites
	// (e.g. cmd/web/search.go passes --mode); the Go flag package doesn't
	// have an equivalent of argparse.SUPPRESS, so we just accept any value.
	mode := flag.String("mode", "", "ignored (kept for backward compatibility with the Python tool's --mode)")
	_ = mode

	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: search [flags] <query>")
		flag.PrintDefaults()
		os.Exit(1)
	}
	query := flag.Arg(0)

	if err := run(*dsn, query, *retrieve, *precise, *verify); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(dsn, query string, retrieve, precise, verify bool) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return fmt.Errorf("connect %s: %w", dsn, err)
	}

	ctx := context.Background()

	// Sanity: bail with a clear message if the indexer hasn't run yet.
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&n); err != nil {
		return fmt.Errorf("count chunks: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("no chunks in library — run cmd/index first")
	}

	var topK int
	var threshold *float64
	if retrieve || precise {
		topK = strictTopK
		t := cosineThreshold
		threshold = &t
	} else {
		topK = defaultTopK
	}

	results, err := vectorSearch(ctx, db, query, topK, threshold)
	if err != nil {
		return err
	}

	if verify && (retrieve || precise) {
		kept, err := verifyFilter(ctx, db, query, results)
		if err != nil {
			return err
		}
		printVerified(query, kept, len(results))
	} else {
		printSources(results)
	}
	return nil
}

type result struct {
	name       string
	similarity float64
}

// vectorSearch runs a single SQL roundtrip: embed the query, then aggregate
// the best chunk score per photo. Threshold (when non-nil) keeps the
// candidate set tight so verify doesn't flood.
func vectorSearch(ctx context.Context, db *sql.DB, query string, topK int, threshold *float64) ([]result, error) {
	embeddings, err := library.EmbedTexts(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	vec := pgvector.NewVector(embeddings[0])

	rows, err := db.QueryContext(ctx, `
		SELECT name, MAX(1 - (embedding <=> $1)) AS similarity
		FROM chunks JOIN photos ON photos.id = chunks.photo_id
		GROUP BY name
		ORDER BY similarity DESC
		LIMIT $2
	`, vec, topK)
	if err != nil {
		return nil, fmt.Errorf("vector query: %w", err)
	}
	defer rows.Close()

	var out []result
	for rows.Next() {
		var r result
		if err := rows.Scan(&r.name, &r.similarity); err != nil {
			return nil, err
		}
		if threshold != nil && r.similarity < *threshold {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// verifyFilter runs the LLM yes/no check on each candidate's BuildDocument
// text in parallel (bounded by verifyConcurrency). Returns the names of
// candidates that came back YES, in the original retrieval order.
func verifyFilter(ctx context.Context, db *sql.DB, query string, candidates []result) ([]string, error) {
	model := library.SearchModel()
	fmt.Fprintf(os.Stderr, "\n--- Verifying %d candidate(s) with LLM ---\n", len(candidates))

	type verdict struct {
		idx int
		ok  bool
		raw string
	}

	verdicts := make([]verdict, len(candidates))
	sem := make(chan struct{}, verifyConcurrency)
	var wg sync.WaitGroup

	for i, c := range candidates {
		i, c := i, c
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			photo, err := library.LoadPhoto(db, c.name)
			if err != nil {
				verdicts[i] = verdict{i, false, "(no photo)"}
				return
			}
			doc := library.BuildDocument(photo)
			if len(doc) > 3000 {
				doc = doc[:3000]
			}
			prompt := fmt.Sprintf(verifyPrompt, query, doc)
			resp, err := library.LLMComplete(ctx, model, prompt)
			if err != nil {
				log.Printf("verify %s: %v", c.name, err)
				verdicts[i] = verdict{i, false, fmt.Sprintf("(error: %v)", err)}
				return
			}
			ok := strings.HasPrefix(strings.ToUpper(strings.TrimSpace(resp)), "Y")
			verdicts[i] = verdict{i, ok, resp}
		}()
	}
	wg.Wait()

	var kept []string
	for i, v := range verdicts {
		marker := "✓"
		if !v.ok {
			marker = "✗"
		}
		display := v.raw
		if len(display) > 80 {
			display = display[:80]
		}
		fmt.Fprintf(os.Stderr, "  %s %s: %s\n", marker, candidates[i].name, display)
		if v.ok {
			kept = append(kept, candidates[i].name)
		}
	}
	return kept, nil
}

func printSources(results []result) {
	if len(results) == 0 {
		return
	}
	fmt.Printf("\n--- Retrieved Sources (%d files) ---\n", len(results))
	for i, r := range results {
		fmt.Printf("  [%d] %s\n", i+1, r.name)
	}
}

func printVerified(query string, kept []string, total int) {
	fmt.Printf("\n--- Verified Sources (%d/%d kept) ---\n", len(kept), total)
	for i, name := range kept {
		fmt.Printf("  [%d] %s\n", i+1, name)
	}
}
