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
//   go run ./cmd/index -dsn postgres:///other_db
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pgvector/pgvector-go"

	"ragotogar/library"
)

func main() {
	var (
		dsn     = flag.String("dsn", library.DefaultDSN(), "Postgres library DSN (overrides LIBRARY_DSN env var)")
		reindex = flag.Bool("reindex", false, "TRUNCATE chunks before re-embedding all photos")
	)
	flag.Parse()

	if err := run(*dsn, *reindex); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(dsn string, reindex bool) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return fmt.Errorf("connect %s: %w", dsn, err)
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
		fmt.Printf("No photos in %s. Run cmd/describe first.\n", dsn)
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

	fmt.Printf("Found %d photo(s) in %s (skipping %d already indexed)\n", len(allNames), dsn, skipped)
	fmt.Printf("Embed: %s @ %s\n\n", library.EmbedModel(), library.EmbedEndpoint())

	totalChunks := 0
	for i, name := range todo {
		n, err := indexOne(ctx, db, name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [error] %s: %v\n", name, err)
			continue
		}
		totalChunks += n
		if (i+1)%10 == 0 || i+1 == len(todo) {
			fmt.Printf("  [%d/%d] %s (%d chunks; %d total)\n", i+1, len(todo), name, n, totalChunks)
		}
	}
	fmt.Printf("\nDone. Indexed %d photo(s), %d chunk(s).\n", len(todo), totalChunks)
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
		vec := pgvector.NewVector(embeddings[i])
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
