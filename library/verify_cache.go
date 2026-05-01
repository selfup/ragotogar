package library

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

// VerifyStats summarizes the cache outcome of a single VerifyFilter call. Used
// by callers to render telemetry — cmd/web shows it in the search footer,
// cmd/search prints it to stderr after the per-photo verdicts.
type VerifyStats struct {
	Total  int
	Cached int
	LLM    int
}

// HitRate returns the fraction of candidates served from cache, in [0, 1].
// Returns 0 when Total is 0 so the caller can format unconditionally.
func (s VerifyStats) HitRate() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Cached) / float64(s.Total)
}

// CanonicalQuery normalizes a query string for cache lookup. v1 is intentionally
// cheap: lowercase + trim. Semantic dedupe (e.g. embedding-similar queries
// hitting the same cache row) is overkill until there's evidence users retype
// queries with cosmetic-only variation.
func CanonicalQuery(q string) string {
	return strings.ToLower(strings.TrimSpace(q))
}

// lookupVerifyCache returns photo_id → cached verdict for rows where the
// cached verdict is fresher than the photo's last describe — stale rows
// (where the photo was re-described after the verdict was written) are
// silently filtered out at lookup time. Photos missing from the result map
// are cache misses.
func lookupVerifyCache(ctx context.Context, db *sql.DB, query string, photoIDs []string, model string) (map[string]bool, error) {
	if len(photoIDs) == 0 {
		return map[string]bool{}, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT vc.photo_id, vc.verdict
		FROM verify_cache vc
		JOIN inference i ON i.photo_id = vc.photo_id
		WHERE vc.query = $1
		  AND vc.verify_model = $2
		  AND vc.photo_id = ANY($3)
		  AND vc.verified_at > i.described_at
	`, query, model, pq.Array(photoIDs))
	if err != nil {
		return nil, fmt.Errorf("verify_cache lookup: %w", err)
	}
	defer rows.Close()

	out := make(map[string]bool, len(photoIDs))
	for rows.Next() {
		var (
			id      string
			verdict bool
		)
		if err := rows.Scan(&id, &verdict); err != nil {
			return nil, err
		}
		out[id] = verdict
	}
	return out, rows.Err()
}

// writeVerifyCache UPSERTs a single verdict. ON CONFLICT keeps concurrent
// writers (the 8-way verify pool) from colliding on the (query, photo_id,
// model) PK; the latest write wins, which is fine because all writers in
// flight are reading the same photo state and producing equivalent verdicts.
func writeVerifyCache(ctx context.Context, db *sql.DB, query, photoID, model string, verdict bool) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO verify_cache (query, photo_id, verify_model, verdict, verified_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (query, photo_id, verify_model) DO UPDATE SET
			verdict     = EXCLUDED.verdict,
			verified_at = now()
	`, query, photoID, model, verdict)
	if err != nil {
		return fmt.Errorf("verify_cache upsert: %w", err)
	}
	return nil
}
