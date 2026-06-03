package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"ragotogar/library"
	"ragotogar/library/testdb"
)

// TestRun_RejectsAllStoresDisabled: cmd/search guards against the
// silently-zero-results misconfig where every -use-* flag is false. The
// guard fires after db open/ping so the test needs a working DSN, hence
// testdb.NewWithDSN.
func TestRun_RejectsAllStoresDisabled(t *testing.T) {
	_, dsn := testdb.NewWithDSN(t, "search_val", nil)

	cfg := runConfig{
		dsn:             dsn,
		query:           "warm light",
		useDescriptions: false,
		useMetadata:     false,
		useQueries:      false,
		mergeStrategy:   library.MergeUnion,
	}
	err := run(context.Background(), cfg)
	if err == nil {
		t.Fatal("run() with all stores disabled should error")
	}
	if !strings.Contains(err.Error(), "at least one of") {
		t.Errorf("error message should call out the all-disabled misconfig, got: %v", err)
	}
}

// TestRun_RejectsUnknownMergeStrategy: typo in -merge-strategy should
// fail fast with a helpful message, not run with surprise behavior.
func TestRun_RejectsUnknownMergeStrategy(t *testing.T) {
	_, dsn := testdb.NewWithDSN(t, "search_val", nil)

	cfg := runConfig{
		dsn:             dsn,
		query:           "warm light",
		useDescriptions: true,
		mergeStrategy:   "intresect", // typo: "intersect"
	}
	err := run(context.Background(), cfg)
	if err == nil {
		t.Fatal("run() with bad merge strategy should error")
	}
	if !strings.Contains(err.Error(), "intresect") {
		t.Errorf("error message should quote the invalid value, got: %v", err)
	}
	if !strings.Contains(err.Error(), "union") {
		t.Errorf("error message should list valid options, got: %v", err)
	}
}

// TestRun_AcceptsEachValidMergeStrategy: validation should pass for all
// three documented strategies. The test asserts the error (if any) is
// NOT a merge-strategy validation error — downstream errors (e.g. SQL
// table missing because the test schema is empty) are expected and OK.
func TestRun_AcceptsEachValidMergeStrategy(t *testing.T) {
	_, dsn := testdb.NewWithDSN(t, "search_val", nil)

	for _, strategy := range []library.MergeStrategy{
		library.MergeUnion,
		library.MergeIntersect,
		library.MergeWeighted,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			cfg := runConfig{
				dsn:             dsn,
				query:           "q",
				useDescriptions: true,
				mergeStrategy:   strategy,
				weightDesc:      1.0,
				weightMeta:      1.0,
				weightQueries:   1.0,
			}
			// Deadline-bound the real call so a down embed endpoint fails fast
				// instead of grinding through EmbedTexts' full retry/backoff.
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				err := run(ctx, cfg)
			// We don't require success — without populated vector stores
			// and a reachable embed endpoint, SearchV2 will error
			// downstream. We DO require the error isn't a validation
			// rejection of this strategy.
			if err != nil && strings.Contains(err.Error(), "unknown -merge-strategy") {
				t.Errorf("valid strategy %q rejected as unknown: %v", strategy, err)
			}
		})
	}
}

// TestRun_BadDSNFailsAtPing: a malformed DSN should error at the
// open/ping stage with a connect-failure message, not a downstream
// nil-pointer dereference.
func TestRun_BadDSNFailsAtPing(t *testing.T) {
	cfg := runConfig{
		dsn:             "postgres://nonexistent-host:65535/no_such_db",
		query:           "q",
		useDescriptions: true,
		mergeStrategy:   library.MergeUnion,
	}
	err := run(context.Background(), cfg)
	if err == nil {
		t.Fatal("run() with unreachable DSN should error")
	}
	// The error wraps via "connect %s: %w"; expect the DSN tag.
	if !strings.Contains(err.Error(), "connect") {
		t.Errorf("error should be flagged as a connect failure, got: %v", err)
	}
}
