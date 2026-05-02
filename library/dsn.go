package library

import (
	"os"
	"strings"
)

// DefaultDSN is the canonical Postgres connection string for the library.
// All Go binaries (cmd/describe, cmd/web, cmd/index, cmd/search) honor
// LIBRARY_DSN as an override.
func DefaultDSN() string {
	if v := os.Getenv("LIBRARY_DSN"); v != "" {
		return v
	}
	return "postgres:///ragotogar"
}

// MaskDSN truncates dsn at the second colon (inclusive), removing the
// password and everything after it from URL-form DSNs. Used at every
// log/print site that surfaces a DSN so a hosted-DB connection string
// can never reveal credentials in stdout, stderr, or full_run.log.
//
// Examples:
//
//	postgresql://neondb_owner:secret@ep.neon.tech/neondb → postgresql://neondb_owner:
//	postgres:///ragotogar                                 → postgres:///ragotogar    (only one colon, unchanged)
//	postgres://alice@host/db                              → postgres://alice@host/db (only one colon, unchanged)
//
// The rule is deliberately blunt — it sometimes strips port and dbname
// in addition to the password, but those aren't secrets and the
// resulting log line is still enough to identify which library was hit.
// Keyword-form DSNs (host=… user=… password=…) have no scheme colon and
// pass through unchanged; this project's keyword form is only used in
// tests where the password isn't a real secret.
func MaskDSN(dsn string) string {
	first := strings.IndexByte(dsn, ':')
	if first < 0 {
		return dsn
	}
	rest := dsn[first+1:]
	second := strings.IndexByte(rest, ':')
	if second < 0 {
		return dsn
	}
	return dsn[:first+1+second+1]
}
