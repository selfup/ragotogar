package main

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain gates cmd/edge tests on no-goroutine-leaks. cmd/edge's
// search handler runs the lexical + vector lanes sequentially today
// (per the perf note in the c87d738 commit message) but the artifact
// loader may hold mmap-backing goroutines, and the pgx liveness
// ping at startup keeps a driver background routine alive.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).backgroundHealthCheck"),
		goleak.IgnoreTopFunction("github.com/jackc/puddle/v2.(*Pool[...]).backgroundHealthCheck"),
	)
}
