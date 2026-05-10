package main

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain gates cmd/describe tests on no-goroutine-leaks. The
// describer's PREVIEW_WORKERS + inference workers spawn goroutines
// in production; even the current tests (which exercise the DB
// schema and parser surface) trip pgx background workers.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).backgroundHealthCheck"),
		goleak.IgnoreTopFunction("github.com/jackc/puddle/v2.(*Pool[...]).backgroundHealthCheck"),
	)
}
