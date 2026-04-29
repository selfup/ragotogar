// cmd/search — terminal CLI around library.Searcher.
//
// Usage:
//   go run ./cmd/search "warm light bedroom"
//   go run ./cmd/search -retrieve "shallow depth of field"
//   go run ./cmd/search -retrieve -verify "April photos with trees"
//   go run ./cmd/search -precise "indoor scenes"
//
// cmd/web no longer shells out to this binary — it imports library.Searcher
// directly. This CLI is kept for terminal exploration.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"

	"ragotogar/internal/library"
)

func main() {
	var (
		dsn      = flag.String("dsn", library.DefaultDSN(), "Postgres library DSN (overrides LIBRARY_DSN env var)")
		retrieve = flag.Bool("retrieve", false, "Top-500 vector retrieval, cosine ≥ 0.5, no LLM synthesis")
		precise  = flag.Bool("precise", false, "Strict retrieval (cosine ≥ 0.5); alias for -retrieve")
		verify   = flag.Bool("verify", false, "With -retrieve/-precise: run an LLM yes/no check per candidate, keep only YES")
	)
	// argparse-style suppressed flag for backward compat with old call sites.
	mode := flag.String("mode", "", "ignored (kept for compatibility with the old Python tool's --mode)")
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

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&n); err != nil {
		return fmt.Errorf("count chunks: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("no chunks in library — run cmd/index first")
	}

	opts := library.SearchOptions{TopK: library.DefaultTopK}
	if retrieve || precise {
		opts.TopK = library.StrictTopK
		t := library.CosineThreshold
		opts.Threshold = &t
	}

	ctx := context.Background()
	searcher := library.NewSearcher(db)
	results, err := searcher.Search(ctx, query, opts)
	if err != nil {
		return err
	}

	if verify && (retrieve || precise) {
		fmt.Fprintf(os.Stderr, "\n--- Verifying %d candidate(s) with LLM ---\n", len(results))
		verdicts, err := searcher.VerifyFilter(ctx, query, results)
		if err != nil {
			return err
		}
		library.LogVerdicts(os.Stderr, verdicts)
		kept := library.KeptNames(verdicts)
		fmt.Printf("\n--- Verified Sources (%d/%d kept) ---\n", len(kept), len(results))
		for i, name := range kept {
			fmt.Printf("  [%d] %s\n", i+1, name)
		}
		return nil
	}

	if len(results) == 0 {
		return nil
	}
	fmt.Printf("\n--- Retrieved Sources (%d files) ---\n", len(results))
	for i, r := range results {
		fmt.Printf("  [%d] %s\n", i+1, r.Name)
	}
	return nil
}
