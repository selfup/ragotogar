// cmd/index — embed each photo's text into the v12 three-store vector
// schema (photo_descriptions / photo_metadata / photo_queries).
//
// Each store is populated independently:
//   - photo_descriptions: BuildDescriptionDocument → chunked → halfvec(2560)
//   - photo_metadata:     BuildMetadataDocument   → 1 row → halfvec(2560)
//   - photo_queries:      BuildQueryDocuments     → N rows → halfvec(2560)
//
// Skip-if-exists is per-store and keyed on (photo_id, schema_version), so
// a prompt change touching only the description prompt invalidates only the
// description store; metadata + queries stay valid. Partial failure
// (descriptions OK, metadata fails) is logged and resumable — the next
// run picks up the missing store without re-doing the successful one.
//
// Usage:
//
//	go run ./cmd/index
//	go run ./cmd/index -workers 16
//	go run ./cmd/index -reindex=descriptions
//	go run ./cmd/index -reindex=descriptions,queries
//	go run ./cmd/index -dsn postgres:///other_db
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pgvector/pgvector-go"

	"ragotogar/library"
)

// v2SchemaVersion stamps every row inserted into the v12 three-store
// tables. Bump per-store (with a separate const) when a prompt or builder
// change makes prior rows stale; until then all three stores share v2.
const v2SchemaVersion = 2

// reindexSet carries the parsed -reindex flag — which stores should
// invalidate their existing rows for a photo before re-populating. Stores
// not listed here use the standard skip-if-exists path.
type reindexSet struct {
	descriptions bool
	metadata     bool
	queries      bool
}

func parseReindex(raw string) (reindexSet, error) {
	if strings.TrimSpace(raw) == "" {
		return reindexSet{}, nil
	}
	var rs reindexSet
	for _, p := range strings.Split(raw, ",") {
		switch strings.TrimSpace(p) {
		case "":
			// trailing comma — ignore.
		case "descriptions":
			rs.descriptions = true
		case "metadata":
			rs.metadata = true
		case "queries":
			rs.queries = true
		default:
			return rs, fmt.Errorf("unknown store %q in -reindex (valid: descriptions, metadata, queries)", p)
		}
	}
	return rs, nil
}

func main() {
	var (
		dsn         = flag.String("dsn", library.DefaultDSN(), "Postgres library DSN (overrides LIBRARY_DSN env var)")
		reindexFlag = flag.String("reindex", "", "comma-separated list of stores to invalidate before re-populating: descriptions, metadata, queries. Default empty (incremental skip-if-exists).")
		workers     = flag.Int("workers", 1, "parallel embed workers. Default 1 (local LM Studio serializes on GPU). Bump to 8–16 against cloud embed endpoints.")
	)
	flag.Parse()

	rs, err := parseReindex(*reindexFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}

	if err := run(*dsn, rs, *workers); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(dsn string, reindex reindexSet, workers int) error {
	if workers < 1 {
		workers = 1
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	// Each worker holds at most one tx at a time per store; with 3 stores
	// the pool needs at least workers*1 connections plus slack for list /
	// exists queries. Bump headroom slightly.
	db.SetMaxOpenConns(workers + 8)
	if err := db.Ping(); err != nil {
		return fmt.Errorf("connect %s: %w", library.MaskDSN(dsn), err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rows, err := db.Query("SELECT name FROM photos ORDER BY name")
	if err != nil {
		return fmt.Errorf("list photos: %w", err)
	}
	var allNames []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		allNames = append(allNames, n)
	}
	rows.Close()
	if len(allNames) == 0 {
		fmt.Printf("No photos in %s. Run cmd/describe first.\n", library.MaskDSN(dsn))
		return nil
	}

	// Pre-compute per-store skip sets. Each store's skip set is the set of
	// photos that already have rows at v2SchemaVersion. -reindex=<store>
	// skips loading the corresponding set (treats it as empty so every
	// photo gets re-populated for that store).
	descExisting, err := loadExistingV2(db, "photo_descriptions", reindex.descriptions)
	if err != nil {
		return fmt.Errorf("load existing descriptions: %w", err)
	}
	metaExisting, err := loadExistingV2(db, "photo_metadata", reindex.metadata)
	if err != nil {
		return fmt.Errorf("load existing metadata: %w", err)
	}
	queriesExisting, err := loadExistingV2(db, "photo_queries", reindex.queries)
	if err != nil {
		return fmt.Errorf("load existing queries: %w", err)
	}

	// todo = photos that need at least one store populated. Photos already
	// complete across all enabled stores are skipped entirely.
	var todo []string
	for _, n := range allNames {
		needDesc := !descExisting[n]
		needMeta := !metaExisting[n]
		needQ := !queriesExisting[n]
		if needDesc || needMeta || needQ {
			todo = append(todo, n)
		}
	}
	skipped := len(allNames) - len(todo)

	fmt.Printf("Found %d photo(s) in %s (skipping %d already complete)\n", len(allNames), library.MaskDSN(dsn), skipped)
	fmt.Printf("Embed: %s @ %s\n", library.EmbedModel(), library.EmbedEndpoint())
	fmt.Printf("Stores: descriptions, metadata, queries\n")
	if reindex.descriptions || reindex.metadata || reindex.queries {
		var rs []string
		if reindex.descriptions {
			rs = append(rs, "descriptions")
		}
		if reindex.metadata {
			rs = append(rs, "metadata")
		}
		if reindex.queries {
			rs = append(rs, "queries")
		}
		fmt.Printf("Reindex: %s\n", strings.Join(rs, ", "))
	}
	fmt.Printf("Workers: %d\n\n", workers)

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var (
		photosDone                          atomic.Int64
		descRows, metaRows, queriesRows     atomic.Int64
		descSkip, metaSkip, queriesSkip     atomic.Int64
		descFail, metaFail, queriesFail     atomic.Int64
		loadFail                            atomic.Int64
	)
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

			photo, err := library.LoadPhoto(db, n)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  [load-error] %s: %v\n", n, err)
				loadFail.Add(1)
				return
			}

			// Per-store work. Each store decides for itself whether to run
			// based on the pre-computed existing set (overridden by reindex).
			// A non-retryable embed error from any store cancels the whole
			// run so we don't burn 16 workers × N photos hitting the same
			// wall (wrong model, bad auth, etc).
			handleStoreErr := func(store string, err error) {
				if errors.Is(err, library.ErrNonRetryable) {
					fatalOnce.Do(func() {
						fatalErr = fmt.Errorf("%s/%s: %w (aborting — fix the embed endpoint and rerun)", n, store, err)
						cancel()
					})
					return
				}
				fmt.Fprintf(os.Stderr, "  [%s-error] %s: %v\n", store, n, err)
			}

			// Descriptions store.
			if !descExisting[n] {
				added, err := indexDescriptions(ctx, db, photo)
				if err != nil {
					handleStoreErr("descriptions", err)
					descFail.Add(1)
				} else {
					descRows.Add(int64(added))
				}
			} else {
				descSkip.Add(1)
			}
			if ctx.Err() != nil {
				return
			}

			// Metadata store.
			if !metaExisting[n] {
				added, err := indexMetadata(ctx, db, photo)
				if err != nil {
					handleStoreErr("metadata", err)
					metaFail.Add(1)
				} else {
					metaRows.Add(int64(added))
				}
			} else {
				metaSkip.Add(1)
			}
			if ctx.Err() != nil {
				return
			}

			// Queries store. Photos without GeneratedQueries (no query_generations
			// row, or parse failure on describe) emit zero rows — that's logged
			// to skip rather than fail since it's expected for older photos
			// that pre-date the v12 prompt change. Re-running cmd/describe
			// will regenerate queries and the next index run will pick them up.
			if !queriesExisting[n] {
				added, err := indexQueries(ctx, db, photo)
				if err != nil {
					handleStoreErr("queries", err)
					queriesFail.Add(1)
				} else if added == 0 {
					queriesSkip.Add(1)
				} else {
					queriesRows.Add(int64(added))
				}
			} else {
				queriesSkip.Add(1)
			}

			ord := photosDone.Add(1)
			if ord%10 == 0 || ord == int64(len(todo)) {
				fmt.Printf("  [%d/%d] %s — desc=%d meta=%d q=%d (totals)\n",
					ord, len(todo), n,
					descRows.Load(), metaRows.Load(), queriesRows.Load(),
				)
			}
		}(name)
	}
	wg.Wait()

	if fatalErr != nil {
		return fatalErr
	}
	fmt.Printf("\nDone. %d photo(s) processed, elapsed %s\n", photosDone.Load(), time.Since(start).Round(time.Second))
	fmt.Printf("  descriptions: %d rows added, %d skipped, %d failed\n", descRows.Load(), descSkip.Load(), descFail.Load())
	fmt.Printf("  metadata:     %d rows added, %d skipped, %d failed\n", metaRows.Load(), metaSkip.Load(), metaFail.Load())
	fmt.Printf("  queries:      %d rows added, %d skipped (incl. zero-query photos), %d failed\n", queriesRows.Load(), queriesSkip.Load(), queriesFail.Load())
	if loadFail.Load() > 0 {
		fmt.Printf("  load errors:  %d (photo skipped entirely)\n", loadFail.Load())
	}
	return nil
}

// loadExistingV2 returns the set of photo IDs that already have at least one
// row in the named v2 store at v2SchemaVersion. Returns an empty map (so
// every photo's "exists" check returns false → populate) when reindex is
// true.
func loadExistingV2(db *sql.DB, table string, reindex bool) (map[string]bool, error) {
	if reindex {
		return map[string]bool{}, nil
	}
	// table is a fixed-string switch on caller side — no SQL injection risk
	// (the three valid values are pinned).
	q := fmt.Sprintf("SELECT DISTINCT photo_id FROM %s WHERE schema_version = $1", table)
	rows, err := db.Query(q, v2SchemaVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// indexDescriptions chunks the description text and writes one row per
// chunk into photo_descriptions. Returns the number of rows inserted.
// Empty text (photo lacks all scene fields) returns 0 with no error.
func indexDescriptions(ctx context.Context, db *sql.DB, photo *library.Photo) (int, error) {
	doc := library.BuildDescriptionDocument(photo)
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
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM photo_descriptions WHERE photo_id = $1 AND schema_version = $2",
		photo.Name, v2SchemaVersion,
	); err != nil {
		return 0, fmt.Errorf("delete existing description rows: %w", err)
	}
	for i, text := range chunks {
		vec := pgvector.NewHalfVector(embeddings[i])
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO photo_descriptions
			    (photo_id, schema_version, chunk_index, chunk_text, embedding)
			 VALUES ($1, $2, $3, $4, $5)`,
			photo.Name, v2SchemaVersion, i, text, vec,
		); err != nil {
			return 0, fmt.Errorf("insert description chunk %d: %w", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(chunks), nil
}

// indexMetadata embeds the EXIF-token text into a single photo_metadata row.
// Empty token text (photo with no EXIF columns populated) returns 0.
func indexMetadata(ctx context.Context, db *sql.DB, photo *library.Photo) (int, error) {
	text := library.BuildMetadataDocument(photo)
	if strings.TrimSpace(text) == "" {
		return 0, nil
	}
	embeddings, err := library.EmbedTexts(ctx, []string{text})
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM photo_metadata WHERE photo_id = $1 AND schema_version = $2",
		photo.Name, v2SchemaVersion,
	); err != nil {
		return 0, fmt.Errorf("delete existing metadata row: %w", err)
	}
	vec := pgvector.NewHalfVector(embeddings[0])
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO photo_metadata
		    (photo_id, schema_version, metadata_text, embedding)
		 VALUES ($1, $2, $3, $4)`,
		photo.Name, v2SchemaVersion, text, vec,
	); err != nil {
		return 0, fmt.Errorf("insert metadata row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return 1, nil
}

// indexQueries writes one photo_queries row per generated phrasing. Photos
// without GeneratedQueries (parse failure, or pre-v12 describe) return 0
// without error — the caller treats this as a benign skip rather than a
// failure since a future cmd/describe -force will regenerate.
func indexQueries(ctx context.Context, db *sql.DB, photo *library.Photo) (int, error) {
	queries := library.BuildQueryDocuments(photo)
	if len(queries) == 0 {
		return 0, nil
	}
	embeddings, err := library.EmbedTexts(ctx, queries)
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM photo_queries WHERE photo_id = $1 AND schema_version = $2",
		photo.Name, v2SchemaVersion,
	); err != nil {
		return 0, fmt.Errorf("delete existing query rows: %w", err)
	}
	for i, text := range queries {
		vec := pgvector.NewHalfVector(embeddings[i])
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO photo_queries
			    (photo_id, schema_version, query_index, query_text, embedding)
			 VALUES ($1, $2, $3, $4, $5)`,
			photo.Name, v2SchemaVersion, i, text, vec,
		); err != nil {
			return 0, fmt.Errorf("insert query row %d: %w", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(queries), nil
}
