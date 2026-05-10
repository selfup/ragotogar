// cmd/edge_build builds the edge-search artifact pipeline.
//
// Reads from the v12 three-store pg library and emits seven static
// read-only artifacts to -out: terms.fst + postings.bin (lexical lane),
// vectors.{descriptions,metadata,queries}.bin (vector lanes), payload.bin
// (per-photo caption + tags), and manifest.json. The artifacts are
// consumed by cmd/edge at runtime — pg stays the system of record but is
// not in the search query path.
//
// See EDGE.md for design decisions and the locked artifact shape.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"ragotogar/library"
)

func main() {
	dsn := flag.String("dsn", library.DefaultDSN(), "Postgres library DSN")
	out := flag.String("out", "", "output directory (required)")
	embedModel := flag.String("embed-model", "", "operator-asserted embedder version recorded per lane in manifest (required)")
	flag.Parse()

	if *out == "" {
		log.Fatal("-out is required")
	}
	if *embedModel == "" {
		log.Fatal("-embed-model is required (manifest field, drift-checked at runtime)")
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", *out, err)
	}

	log.Printf("edge_build starting: dsn=%s out=%s embed-model=%s", library.MaskDSN(*dsn), *out, *embedModel)

	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("ping db: %v", err)
	}

	start := time.Now()
	if err := run(db, *out, *embedModel); err != nil {
		log.Fatalf("build failed: %v", err)
	}
	log.Printf("edge_build done in %s", time.Since(start).Round(time.Millisecond))
}

func run(db *sql.DB, outDir, embedModel string) error {
	ids, err := loadIDSpace(db)
	if err != nil {
		return fmt.Errorf("load id space: %w", err)
	}
	log.Printf("id space: %d photos", len(ids.Names))

	fstStats, err := buildFSTAndPostings(db, ids, filepath.Join(outDir, "terms.fst"), filepath.Join(outDir, "postings.bin"))
	if err != nil {
		return fmt.Errorf("build fst: %w", err)
	}
	log.Printf("fst: %d unique lexemes, %d postings, terms.fst=%s postings.bin=%s",
		fstStats.UniqueTerms, fstStats.TotalPostings,
		humanBytes(fstStats.FSTBytes), humanBytes(fstStats.PostingsBytes))

	laneRows, err := buildVectorLanes(db, ids, outDir)
	if err != nil {
		return fmt.Errorf("build vectors: %w", err)
	}
	log.Printf("vectors: descriptions=%d metadata=%d queries=%d",
		laneRows.Descriptions, laneRows.Metadata, laneRows.Queries)

	if err := buildPayload(db, ids, filepath.Join(outDir, "payload.bin")); err != nil {
		return fmt.Errorf("build payload: %w", err)
	}

	if err := writeManifest(db, ids, laneRows, embedModel, filepath.Join(outDir, "manifest.json")); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
