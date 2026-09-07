package library

import (
	"context"
	"database/sql"
	"fmt"
)

// StoredEmbeddingDimensions reads the column types, including empty stores.
// All three stores must use the same fixed halfvec dimension.
func StoredEmbeddingDimensions(ctx context.Context, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.relname, t.typname, a.atttypmod
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_type t ON t.oid = a.atttypid
		WHERE a.attrelid IN ('photo_descriptions'::regclass,
		                    'photo_metadata'::regclass, 'photo_queries'::regclass)
		  AND a.attname = 'embedding' AND NOT a.attisdropped`)
	if err != nil {
		return 0, fmt.Errorf("read vector schema: %w", err)
	}
	defer rows.Close()
	dim, count := 0, 0
	for rows.Next() {
		var table, kind string
		var n int
		if err := rows.Scan(&table, &kind, &n); err != nil {
			return 0, err
		}
		if kind != "halfvec" || n < 1 || n > 4000 {
			return 0, fmt.Errorf("%s.embedding must be fixed-dimension halfvec (1–4000), got %s with dimension %d", table, kind, n)
		}
		if dim != 0 && n != dim {
			return 0, fmt.Errorf("vector stores have inconsistent dimensions: %s has %d, another store has %d", table, n, dim)
		}
		dim = n
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if count != 3 {
		return 0, fmt.Errorf("expected 3 vector embedding columns, found %d; run scripts/photo_describe.sh -init-only", count)
	}
	return dim, nil
}
