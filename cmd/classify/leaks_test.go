package main

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain gates cmd/classify tests on no-goroutine-leaks. cmd/classify
// runs a parallel worker pool in run(); even the listTodo-only tests
// indirectly exercise pgx connection plumbing.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).backgroundHealthCheck"),
		goleak.IgnoreTopFunction("github.com/jackc/puddle/v2.(*Pool[...]).backgroundHealthCheck"),
	)
}
