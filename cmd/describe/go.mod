module describe

go 1.26.1

// Local-replace lets cmd/describe reach into internal/library/ for the
// classifier (so the inline -classify flag can call library.ClassifyOne
// directly instead of duplicating prompt/parse/validate logic).
replace ragotogar => ../..

require (
	github.com/jackc/pgx/v5 v5.9.2
	ragotogar v0.0.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/lib/pq v1.10.9 // indirect
	github.com/pgvector/pgvector-go v0.3.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)
