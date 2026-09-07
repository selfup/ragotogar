// cmd/index — embed each photo's text into the v12 three-store vector
// schema (photo_descriptions / photo_metadata / photo_queries).
//
// Each store is populated independently:
//   - photo_descriptions: BuildDescriptionDocument → chunked → halfvec(dim)
//   - photo_metadata:     BuildMetadataDocument   → 1 row → halfvec(dim)
//   - photo_queries:      BuildQueryDocuments     → N rows → halfvec(dim)
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
//	go run ./cmd/index -batch-size 10
//	go run ./cmd/index -workers 16
//	go run ./cmd/index -reindex=descriptions
//	go run ./cmd/index -reindex=descriptions,queries
//	go run ./cmd/index -dsn postgres:///other_db
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"ragotogar/library"
)

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
	for p := range strings.SplitSeq(raw, ",") {
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
		reindexFlag = flag.String("reindex", "", "comma-separated stores to re-populate: descriptions, metadata, queries. Listing all three also authorizes clearing/resizing vector stores when model identity or dimensions change (including unrecorded legacy models). Default empty (incremental skip-if-exists).")
		workers     = flag.Int("workers", 1, "parallel batch workers. Default 1 for local LM Studio; bump to 8–16 against cloud embed endpoints.")
		batchSize   = flag.Int("batch-size", defaultBatchSize, "maximum documents per embedding request; each chunk or query phrasing is a separate input")
	)
	flag.Parse()

	rs, err := parseReindex(*reindexFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}

	if err := run(*dsn, rs, *workers, *batchSize); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(dsn string, reindex reindexSet, workers, batchSize int) error {
	if batchSize < 1 {
		return fmt.Errorf("-batch-size must be at least 1")
	}
	if workers < 1 {
		workers = 1
	}
	dim, err := library.EmbeddingDimensions()
	if err != nil {
		return err
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
	indexLock, err := lockIndex(ctx, db)
	if err != nil {
		return err
	}
	defer indexLock.Rollback()
	if err := prepareVectorStores(ctx, db, dim, reindex); err != nil {
		return err
	}

	// A missing query-store row is work only when source phrasings exist.
	// Older photos can legitimately have no generated queries; repeatedly
	// queueing them after every restart inflates progress with zero-write jobs.
	// Malformed non-empty JSON remains eligible so LoadPhoto reports it.
	rows, err := db.Query(`
		SELECT p.name, COALESCE(qg.queries NOT IN ('[]'::jsonb, 'null'::jsonb), false)
		FROM photos p
		LEFT JOIN query_generations qg ON qg.photo_id = p.id
		ORDER BY p.name`)
	if err != nil {
		return fmt.Errorf("list photos: %w", err)
	}
	var allNames []string
	querySources := map[string]bool{}
	for rows.Next() {
		var n string
		var hasQueries bool
		if err := rows.Scan(&n, &hasQueries); err != nil {
			rows.Close()
			return err
		}
		allNames = append(allNames, n)
		querySources[n] = hasQueries
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("list photos: %w", err)
	}
	rows.Close()
	if len(allNames) == 0 {
		fmt.Printf("No photos in %s. Run cmd/describe first.\n", library.MaskDSN(dsn))
		return nil
	}

	// Pre-compute per-store skip sets. Each store's skip set is the set of
	// photos that already have rows at library.V2SchemaVersion. -reindex=<store>
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

	// todo = photos that need at least one store populated. A photo without
	// query phrasings doesn't need a photo_queries row. New phrasings are
	// picked up on the next run because availability is read fresh each time.
	var todo []string
	for _, n := range allNames {
		needDesc := !descExisting[n]
		needMeta := !metaExisting[n]
		needQ := querySources[n] && !queriesExisting[n]
		if needDesc || needMeta || needQ {
			todo = append(todo, n)
		}
	}
	skipped := len(allNames) - len(todo)

	fmt.Printf("Found %d photo(s) in %s (skipping %d with no pending embeddings)\n", len(allNames), library.MaskDSN(dsn), skipped)
	fmt.Printf("Embed: %s @ %s\n", library.EmbedModel(), library.EmbedEndpoint())
	fmt.Printf("Dimensions: %d\n", dim)
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
	fmt.Printf("Workers: %d\n", workers)
	fmt.Printf("Batch size: %d documents per embedding request (max)\n\n", batchSize)

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var (
		photosDone                      atomic.Int64
		descRows, metaRows, queriesRows atomic.Int64
		descSkip, metaSkip, queriesSkip atomic.Int64
		descFail, metaFail, queriesFail atomic.Int64
		loadFail                        atomic.Int64
	)
	var fatalOnce sync.Once
	var fatalErr error

	stores := []struct {
		store                 documentStore
		existing              map[string]bool
		rows, skipped, failed *atomic.Int64
	}{
		{descriptionsStore, descExisting, &descRows, &descSkip, &descFail},
		{metadataStore, metaExisting, &metaRows, &metaSkip, &metaFail},
		{queriesStore, queriesExisting, &queriesRows, &queriesSkip, &queriesFail},
	}

	start := time.Now()
queue:
	for offset := 0; offset < len(todo); offset += batchSize {
		// Bound both in-flight HTTP requests and loaded photos. One worker
		// processes a window of photos, batching each store across the window.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break queue
		}
		wg.Add(1)
		go func(names []string) {
			defer wg.Done()
			defer func() { <-sem }()
			var photos []*library.Photo
			for _, name := range names {
				if ctx.Err() != nil {
					return
				}
				photo, err := library.LoadPhoto(db, name)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  [load-error] %s: %v\n", name, err)
					loadFail.Add(1)
					continue
				}
				photos = append(photos, photo)
			}

			for _, s := range stores {
				if ctx.Err() != nil {
					return
				}
				var pending []*library.Photo
				for _, photo := range photos {
					if s.existing[photo.Name] {
						s.skipped.Add(1)
					} else {
						pending = append(pending, photo)
					}
				}
				results, err := indexStoreBatch(ctx, db, s.store, pending, batchSize)
				if err != nil {
					// Permanent endpoint failures stop other workers immediately.
					if fatalEmbeddingError(err) {
						fatalOnce.Do(func() {
							fatalErr = fmt.Errorf("%s: %w (aborting — fix the embed endpoint and rerun)", s.store.name, err)
							cancel()
						})
					}
					return // otherwise another worker canceled this request
				}
				for i, result := range results {
					if result.err != nil {
						fmt.Fprintf(os.Stderr, "  [%s-error] %s: %v\n", s.store.name, pending[i].Name, result.err)
						s.failed.Add(1)
					} else if result.added == 0 {
						s.skipped.Add(1)
					} else {
						s.rows.Add(int64(result.added))
					}
				}
			}

			for _, photo := range photos {
				ord := photosDone.Add(1)
				if ord%10 == 0 || ord == int64(len(todo)) {
					fmt.Printf("  [%d/%d] %s — rows added this run: desc=%d meta=%d q=%d\n",
						ord, len(todo), photo.Name,
						descRows.Load(), metaRows.Load(), queriesRows.Load(),
					)
				}
			}
		}(todo[offset:min(offset+batchSize, len(todo))])
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
// row in the named v2 store at library.V2SchemaVersion. Returns an empty map (so
// every photo's "exists" check returns false → populate) when reindex is
// true.
func loadExistingV2(db *sql.DB, table string, reindex bool) (map[string]bool, error) {
	if reindex {
		return map[string]bool{}, nil
	}
	// table is a fixed-string switch on caller side — no SQL injection risk
	// (the three valid values are pinned).
	q := fmt.Sprintf("SELECT DISTINCT photo_id FROM %s WHERE schema_version = $1", table)
	rows, err := db.Query(q, library.V2SchemaVersion)
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
