package library

import (
	"context"
	"database/sql"
	"encoding/json"
)

// DescribeResult is one saved-photo snapshot in cmd/describe's optional JSONL
// report. A photo can appear first as described, then as classified or failed.
// IndexReady marks a successful classification in this run, never an old row.
type DescribeResult struct {
	Name           string          `json:"name"`
	Path           string          `json:"path"`
	Status         string          `json:"status"`
	Description    json.RawMessage `json:"description"`
	Classification json.RawMessage `json:"classification"`
	Error          string          `json:"error,omitempty"`
	IndexReady     bool            `json:"index_ready"`
}

func LoadDescribeResult(ctx context.Context, db *sql.DB, name string) (DescribeResult, error) {
	r := DescribeResult{Name: name}
	err := db.QueryRowContext(ctx, `
		SELECT p.file_path, COALESCE(to_jsonb(d) - 'photo_id' - 'fts', 'null'::jsonb),
		       CASE WHEN c.classified_at >= i.described_at
		            THEN to_jsonb(c) - 'photo_id' ELSE 'null'::jsonb END
		FROM photos p
		LEFT JOIN descriptions d ON d.photo_id = p.id
		LEFT JOIN inference i ON i.photo_id = p.id
		LEFT JOIN classified c ON c.photo_id = p.id
		WHERE p.name = $1`, name).Scan(&r.Path, &r.Description, &r.Classification)
	return r, err
}
