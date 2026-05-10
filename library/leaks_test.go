package library

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs every library_test under goleak.VerifyTestMain, which
// re-runs every Test* in the package and then asserts no goroutines
// outlive the process. SearchV2 / SearchHybridV2 / VerifyFilter all
// spawn goroutines; a missed waitgroup.Done or a closed-channel send
// path that orphans a goroutine would show up here.
//
// IgnoreTopFunction whitelists the pgx connection-pool background
// goroutine that the pgx/v5 stdlib driver runs for the lifetime of
// any sql.DB — that's a library-managed background worker, not a
// leak in our code. If a future pgx version renames the function,
// the whitelist falls out and goleak surfaces real leaks behind it.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).backgroundHealthCheck"),
		goleak.IgnoreTopFunction("github.com/jackc/puddle/v2.(*Pool[...]).backgroundHealthCheck"),
	)
}
