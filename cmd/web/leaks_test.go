package main

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain gates cmd/web tests on no-goroutine-leaks. cmd/web's
// HTTP handlers fan out goroutines (verify pool, edge HTTP client
// timeouts, etc.); any of those leaving a goroutine in flight past
// test completion would surface here.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).backgroundHealthCheck"),
		goleak.IgnoreTopFunction("github.com/jackc/puddle/v2.(*Pool[...]).backgroundHealthCheck"),
	)
}
