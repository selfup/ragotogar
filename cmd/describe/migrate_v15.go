package main

import (
	"database/sql"
	"fmt"
	"os"

	"ragotogar/library"
)

// migrateV15 repairs combined responses stored before query isolation. Updating
// full_description recomputes its generated FTS column. Embeddings and verifier
// verdicts derived from the old prose must be discarded; the regular index run
// then repopulates just the missing description rows. Metadata and query stores
// remain valid. No vision or embedding calls occur during this migration.
func migrateV15(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT photo_id, full_description FROM descriptions
		WHERE full_description ILIKE '%queries%'`)
	if err != nil {
		return err
	}
	type repair struct{ id, raw, prose string }
	var repairs []repair
	for rows.Next() {
		var r repair
		if err := rows.Scan(&r.id, &r.raw); err != nil {
			rows.Close()
			return err
		}
		r.prose = library.StripGeneratedQueries(r.raw)
		if r.prose != r.raw {
			repairs = append(repairs, r)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, r := range repairs {
		// Preserve the original response before changing the searchable prose.
		// Existing raw responses win, including on an interrupted/retried upgrade.
		if _, err := tx.Exec(`
			INSERT INTO inference (photo_id, raw_response) VALUES ($1, $2)
			ON CONFLICT (photo_id) DO UPDATE SET
				raw_response = COALESCE(inference.raw_response, EXCLUDED.raw_response)
		`, r.id, r.raw); err != nil {
			return fmt.Errorf("preserve response %s: %w", r.id, err)
		}
		if _, err := tx.Exec(`UPDATE descriptions SET full_description = $2 WHERE photo_id = $1`,
			r.id, nullIfEmpty(r.prose)); err != nil {
			return fmt.Errorf("clean description %s: %w", r.id, err)
		}
		if _, err := tx.Exec(`DELETE FROM photo_descriptions WHERE photo_id = $1`, r.id); err != nil {
			return fmt.Errorf("invalidate embeddings %s: %w", r.id, err)
		}
		if _, err := tx.Exec(`DELETE FROM verify_cache WHERE photo_id = $1`, r.id); err != nil {
			return fmt.Errorf("invalidate verification %s: %w", r.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if len(repairs) > 0 {
		fmt.Fprintf(os.Stderr, "v15: isolated generated queries in %d description(s); run scripts/index.sh to restore their description embeddings, then rebuild any edge artifacts.\n", len(repairs))
	}
	return nil
}
