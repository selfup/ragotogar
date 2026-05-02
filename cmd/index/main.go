// cmd/index — embed each photo's description into the chunks table.
//
// Reads photos / exif / descriptions from Postgres, chunks the
// library.BuildDocument output, embeds each chunk via LM Studio, and
// INSERTs into the chunks table. Default is incremental (skip photos that
// already have any chunks); --reindex truncates first.
//
// Usage:
//   go run ./cmd/index
//   go run ./cmd/index -reindex
//   go run ./cmd/index -workers 16            # cloud embed endpoint
//   go run ./cmd/index -dsn postgres:///other_db
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pgvector/pgvector-go"

	"ragotogar/library"
)

func main() {
	var (
		dsn     = flag.String("dsn", library.DefaultDSN(), "Postgres library DSN (overrides LIBRARY_DSN env var)")
		reindex = flag.Bool("reindex", false, "TRUNCATE chunks before re-embedding all photos")
		workers = flag.Int("workers", 1, "parallel embed workers. Default 1 (local LM Studio serializes on GPU anyway). Bump to 8–16 against cloud embed endpoints.")
	)
	flag.Parse()

	if err := run(*dsn, *reindex, *workers); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(dsn string, reindex bool, workers int) error {
	if workers < 1 {
		workers = 1
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	// Each worker holds a transaction (DELETE + INSERT per photo) so the
	// pool needs at least workers connections; +4 covers list/exists queries
	// and a little slack.
	db.SetMaxOpenConns(workers + 4)
	if err := db.Ping(); err != nil {
		return fmt.Errorf("connect %s: %w", library.MaskDSN(dsn), err)
	}

	ctx := context.Background()

	if reindex {
		if _, err := db.Exec("TRUNCATE chunks"); err != nil {
			return fmt.Errorf("truncate chunks: %w", err)
		}
		log.Printf("truncated chunks table")
	}

	rows, err := db.Query("SELECT name FROM photos ORDER BY name")
	if err != nil {
		return fmt.Errorf("list photos: %w", err)
	}
	var allNames []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		allNames = append(allNames, n)
	}
	rows.Close()
	if len(allNames) == 0 {
		fmt.Printf("No photos in %s. Run cmd/describe first.\n", library.MaskDSN(dsn))
		return nil
	}

	existing := make(map[string]bool)
	if !reindex {
		exRows, err := db.Query("SELECT DISTINCT photo_id FROM chunks")
		if err != nil {
			return fmt.Errorf("list existing chunks: %w", err)
		}
		for exRows.Next() {
			var id string
			if err := exRows.Scan(&id); err != nil {
				exRows.Close()
				return err
			}
			existing[id] = true
		}
		exRows.Close()
	}

	var todo []string
	for _, n := range allNames {
		if !existing[n] {
			todo = append(todo, n)
		}
	}
	skipped := len(allNames) - len(todo)

	fmt.Printf("Found %d photo(s) in %s (skipping %d already indexed)\n", len(allNames), library.MaskDSN(dsn), skipped)
	fmt.Printf("Embed: %s @ %s\n", library.EmbedModel(), library.EmbedEndpoint())
	fmt.Printf("Workers: %d\n\n", workers)

	// Fan-out pattern mirrors cmd/classify. Default workers=1 preserves
	// local LM Studio behavior (concurrent embed requests serialize on GPU
	// anyway); cloud endpoints earn the speedup at higher counts.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var indexed, failed, totalChunks atomic.Int64
	var fatalOnce sync.Once
	var fatalErr error

	start := time.Now()
	for _, name := range todo {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(n string) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			chunks, err := indexOne(ctx, db, n)
			if err != nil {
				// Non-retryable means every subsequent photo will hit the
				// same wall (wrong model name, no model loaded, bad auth).
				// First fatal cancels the context so in-flight workers
				// drain quickly instead of all hitting the same error.
				if errors.Is(err, library.ErrNonRetryable) {
					fatalOnce.Do(func() {
						fatalErr = fmt.Errorf("%s: %w (aborting — fix the embed endpoint and rerun)", n, err)
						cancel()
					})
					return
				}
				fmt.Fprintf(os.Stderr, "  [error] %s: %v\n", n, err)
				failed.Add(1)
				return
			}
			ord := indexed.Add(1)
			total := totalChunks.Add(int64(chunks))
			if ord%10 == 0 || ord == int64(len(todo)) {
				fmt.Printf("  [%d/%d] %s (%d chunks; %d total)\n", ord, len(todo), n, chunks, total)
			}
		}(name)
	}
	wg.Wait()

	if fatalErr != nil {
		return fatalErr
	}
	fmt.Printf("\nDone. Indexed %d photo(s), %d chunk(s), Errors: %d, Elapsed: %s\n",
		indexed.Load(), totalChunks.Load(), failed.Load(), time.Since(start).Round(time.Second))
	return nil
}

// indexOne chunks + embeds a single photo and replaces its chunk rows in
// one transaction. Idempotent — DELETE then INSERT so rerunning replaces
// stale chunks cleanly.
func indexOne(ctx context.Context, db *sql.DB, name string) (int, error) {
	photo, err := library.LoadPhoto(db, name)
	if err != nil {
		return 0, err
	}
	doc := library.BuildDocument(photo)
	chunks := library.Chunk(doc)
	if len(chunks) == 0 {
		return 0, nil
	}
	embeddings, err := library.EmbedTexts(ctx, chunks)
	if err != nil {
		return 0, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM chunks WHERE photo_id = $1", name); err != nil {
		return 0, fmt.Errorf("delete existing chunks: %w", err)
	}
	for i, text := range chunks {
		vec := pgvector.NewHalfVector(embeddings[i])
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO chunks (photo_id, idx, text, embedding) VALUES ($1, $2, $3, $4)",
			name, i, text, vec,
		); err != nil {
			return 0, fmt.Errorf("insert chunk %d: %w", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(chunks), nil
}
