package main

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain gates cmd/index tests on no-goroutine-leaks. cmd/index spawns
// a worker pool in run() and per-store transactions; even though the
// existing tests call indexDescriptions/indexMetadata/indexQueries
// directly (not run()), goleak still surfaces any pgx connection
// background goroutine or HTTP roundtripper that doesn't wind down.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).backgroundHealthCheck"),
		goleak.IgnoreTopFunction("github.com/jackc/puddle/v2.(*Pool[...]).backgroundHealthCheck"),
	)
}
