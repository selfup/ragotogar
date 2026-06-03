// cmd/search — terminal CLI around library.SearcherV2.
//
// v12 default: queries the three vector stores (photo_descriptions,
// photo_metadata, photo_queries) and merges per the chosen strategy.
//
// Usage:
//
//	go run ./cmd/search "warm light bedroom"
//	go run ./cmd/search -retrieve "shallow depth of field"
//	go run ./cmd/search -retrieve -verify "April photos with trees"
//	go run ./cmd/search -merge-strategy=intersect "red truck"
//	go run ./cmd/search -use-queries=false -use-metadata=false "X100VI"
//	go run ./cmd/search -merge-strategy=weighted -weight-queries=2.0 "moody portrait"
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

	"ragotogar/library"
)

func main() {
	var (
		dsn      = flag.String("dsn", library.DefaultDSN(), "Postgres library DSN (overrides LIBRARY_DSN env var)")
		retrieve = flag.Bool("retrieve", false, "Unbounded vector retrieval (every match above the cosine cutoff), no LLM synthesis")
		precise  = flag.Bool("precise", false, "Strict retrieval; alias for -retrieve")
		hybrid   = flag.Bool("hybrid", false, "Vector retrieval + Postgres FTS, fused via Reciprocal Rank Fusion (unbounded above the cosine cutoff)")
		verify   = flag.Bool("verify", false, "With -retrieve/-precise/-hybrid: run an LLM yes/no check per candidate, keep only YES")
		cosine   = flag.Float64("cosine", library.CosineThreshold, "Vector cosine cutoff (0..1). Applied per-store before merge.")
		fts      = flag.Float64("fts", library.FTSRelativeThreshold, "FTS adaptive threshold ratio (0..1). 0 = no FTS filter; 1 = only the top-ranked FTS match.")

		useDescriptions = flag.Bool("use-descriptions", true, "Query the photo_descriptions store")
		useMetadata     = flag.Bool("use-metadata", true, "Query the photo_metadata store")
		useQueries      = flag.Bool("use-queries", true, "Query the photo_queries store")
		mergeStrategy   = flag.String("merge-strategy", "union", "How to combine per-store results: union | intersect | weighted")
		weightDesc      = flag.Float64("weight-descriptions", 1.0, "Score weight for the descriptions store under -merge-strategy=weighted")
		weightMeta      = flag.Float64("weight-metadata", 1.0, "Score weight for the metadata store under -merge-strategy=weighted")
		weightQueries   = flag.Float64("weight-queries", 1.0, "Score weight for the queries store under -merge-strategy=weighted")
	)
	mode := flag.String("mode", "", "ignored (kept for compatibility with the old Python tool's --mode)")
	_ = mode

	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: search [flags] <query>")
		flag.PrintDefaults()
		os.Exit(1)
	}
	query := flag.Arg(0)

	cfg := runConfig{
		dsn:             *dsn,
		query:           query,
		retrieve:        *retrieve,
		precise:         *precise,
		hybrid:          *hybrid,
		verify:          *verify,
		cosine:          *cosine,
		fts:             *fts,
		useDescriptions: *useDescriptions,
		useMetadata:     *useMetadata,
		useQueries:      *useQueries,
		mergeStrategy:   library.MergeStrategy(*mergeStrategy),
		weightDesc:      *weightDesc,
		weightMeta:      *weightMeta,
		weightQueries:   *weightQueries,
	}

	if err := run(context.Background(), cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

type runConfig struct {
	dsn             string
	query           string
	retrieve        bool
	precise         bool
	hybrid          bool
	verify          bool
	cosine          float64
	fts             float64
	useDescriptions bool
	useMetadata     bool
	useQueries      bool
	mergeStrategy   library.MergeStrategy
	weightDesc      float64
	weightMeta      float64
	weightQueries   float64
}

// run executes a search against cfg. It takes a context so callers can bound
// the work: main() passes context.Background() (production keeps the full
// unbounded retry/backoff in library.EmbedTexts), while tests pass a short
// deadline so the real search path executes but fails fast when the embed
// endpoint is unreachable instead of grinding through the whole backoff.
func run(ctx context.Context, cfg runConfig) error {
	db, err := sql.Open("pgx", cfg.dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect %s: %w", library.MaskDSN(cfg.dsn), err)
	}

	if !cfg.useDescriptions && !cfg.useMetadata && !cfg.useQueries {
		return fmt.Errorf("at least one of -use-descriptions / -use-metadata / -use-queries must be true")
	}
	switch cfg.mergeStrategy {
	case library.MergeUnion, library.MergeIntersect, library.MergeWeighted:
		// ok
	default:
		return fmt.Errorf("unknown -merge-strategy %q (valid: union, intersect, weighted)", cfg.mergeStrategy)
	}

	opts := library.SearchOptionsV2{
		TopK:               library.DefaultTopK,
		FTSThresholdRel:    cfg.fts,
		VectorQuery:        library.StripNegation(cfg.query),
		UseDescriptions:    cfg.useDescriptions,
		UseMetadata:        cfg.useMetadata,
		UseQueries:         cfg.useQueries,
		MergeStrategy:      cfg.mergeStrategy,
		WeightDescriptions: cfg.weightDesc,
		WeightMetadata:     cfg.weightMeta,
		WeightQueries:      cfg.weightQueries,
	}
	if cfg.retrieve || cfg.precise || cfg.hybrid {
		opts.TopK = 0 // unbounded — cosine threshold is the only cap
		t := cfg.cosine
		opts.Threshold = &t
	}

	searcher := library.NewSearcher(db)

	var (
		results []library.Result
		err2    error
	)
	if cfg.hybrid {
		results, err2 = searcher.SearchHybridV2(ctx, cfg.query, opts)
	} else {
		results, err2 = searcher.SearchV2(ctx, cfg.query, opts)
	}
	if err2 != nil {
		return err2
	}

	if cfg.verify && (cfg.retrieve || cfg.precise || cfg.hybrid) {
		fmt.Fprintf(os.Stderr, "\n--- Verifying %d candidate(s) with LLM ---\n", len(results))
		verdicts, stats, err := searcher.VerifyFilterV2(ctx, cfg.query, results, opts)
		if err != nil {
			return err
		}
		library.LogVerdicts(os.Stderr, verdicts)
		library.LogVerifyStats(os.Stderr, stats)
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
