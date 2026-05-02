// cmd/classify — map description prose to typed enum fields.
//
// Reads each photo's description from Postgres, sends it to a small text
// LLM with a classifier prompt, validates the JSON response against
// allowed values, and UPSERTs into the classified table. Default is
// incremental (skip photos that already have a classified row);
// -reclassify rebuilds all.
//
// Most of the actual classifier logic lives in internal/library so
// cmd/describe can call it inline (see -classify flag on cmd/describe).
//
// Usage:
//   go run ./cmd/classify
//   go run ./cmd/classify -reclassify
//   go run ./cmd/classify -dsn postgres:///other_db
//   CLASSIFY_MODEL=mistralai/devstral-small-2-2512 go run ./cmd/classify
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"ragotogar/library"
)

func main() {
	var (
		dsn        = flag.String("dsn", library.DefaultDSN(), "Postgres library DSN (overrides LIBRARY_DSN env var)")
		reclassify = flag.Bool("reclassify", false, "TRUNCATE classified before re-classifying all photos")
		workers    = flag.Int("workers", 8, "parallel classifier workers")
	)
	flag.Parse()

	if err := run(*dsn, *reclassify, *workers); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(dsn string, reclassify bool, workers int) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return fmt.Errorf("connect %s: %w", library.MaskDSN(dsn), err)
	}

	ctx := context.Background()

	if reclassify {
		if _, err := db.Exec("TRUNCATE classified"); err != nil {
			return fmt.Errorf("truncate classified: %w", err)
		}
		log.Printf("truncated classified table")
	}

	todo, err := listTodo(db, reclassify)
	if err != nil {
		return err
	}
	if len(todo) == 0 {
		fmt.Printf("Nothing to classify in %s.\n", library.MaskDSN(dsn))
		return nil
	}

	model := library.ClassifyModel()
	fmt.Printf("Classifying %d photo(s) in %s\n", len(todo), library.MaskDSN(dsn))
	fmt.Printf("Model: %s @ %s\n", model, library.TextEndpoint())
	fmt.Printf("Workers: %d\n\n", workers)

	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var ok, failed atomic.Int64

	start := time.Now()
	for _, name := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func(n string) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := library.ClassifyOne(ctx, db, n, model); err != nil {
				fmt.Fprintf(os.Stderr, "  [error] %s: %v\n", n, err)
				failed.Add(1)
				return
			}
			ord := ok.Add(1)
			fmt.Printf("  [%d/%d done] %s\n", ord, len(todo), n)
		}(name)
	}
	wg.Wait()
	fmt.Printf("\nDone. Classified: %d, Errors: %d, Elapsed: %s\n",
		ok.Load(), failed.Load(), time.Since(start).Round(time.Second))
	return nil
}

// listTodo returns the photo names that still need classification. When
// reclassify is true, this is every photo with a description; otherwise
// only photos lacking a classified row.
func listTodo(db *sql.DB, reclassify bool) ([]string, error) {
	q := `
		SELECT d.photo_id
		FROM descriptions d
		LEFT JOIN classified c ON c.photo_id = d.photo_id
		WHERE d.full_description IS NOT NULL`
	if !reclassify {
		q += " AND c.photo_id IS NULL"
	}
	q += " ORDER BY d.photo_id"
	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("list todo: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}
