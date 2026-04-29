package library

import "os"

// DefaultDSN is the canonical Postgres connection string for the library.
// All Go binaries (cmd/describe, cmd/web, cmd/index, cmd/search) honor
// LIBRARY_DSN as an override.
func DefaultDSN() string {
	if v := os.Getenv("LIBRARY_DSN"); v != "" {
		return v
	}
	return "postgres:///ragotogar"
}
