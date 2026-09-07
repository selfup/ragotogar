package main

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain gates the batch workers, cancellation paths, and per-store
// transaction tests on no goroutine leaks. It also catches pgx connection
// background goroutines or HTTP roundtrippers that don't wind down.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).backgroundHealthCheck"),
		goleak.IgnoreTopFunction("github.com/jackc/puddle/v2.(*Pool[...]).backgroundHealthCheck"),
	)
}
