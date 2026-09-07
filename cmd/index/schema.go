package main

import (
	"context"
	"database/sql"
	"fmt"

	"ragotogar/library"
)

// prepareVectorStores records model identity and requires a full reindex to
// change it, including same-dimension changes and legacy vectors of unknown
// origin. The caller holds the index lock for the entire run.
func prepareVectorStores(ctx context.Context, db *sql.DB, dim int, reindex reindexSet) error {
	if _, err := db.ExecContext(ctx, library.EmbeddingConfigSchema); err != nil {
		return fmt.Errorf("create embedding configuration: %w", err)
	}
	current, err := library.StoredEmbeddingDimensions(ctx, db)
	if err != nil {
		return err
	}
	config, err := library.StoredEmbeddingConfig(ctx, db)
	if err != nil {
		return err
	}
	model := library.EmbedModel()
	if current == dim && config != nil && config.Model == model && config.Dimensions == dim {
		return nil
	}
	var populated bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM photo_descriptions)
		OR EXISTS (SELECT 1 FROM photo_metadata) OR EXISTS (SELECT 1 FROM photo_queries)`).Scan(&populated); err != nil {
		return err
	}
	reset := current != dim || config != nil || populated
	if reset {
		if !reindex.descriptions || !reindex.metadata || !reindex.queries {
			previous := "unrecorded"
			if config != nil {
				previous = config.Model
			}
			return fmt.Errorf("vector stores use model %q with %d dimensions; EMBED_MODEL=%q expects %d; run -reindex=descriptions,metadata,queries to establish or change the embedding model", previous, current, model, dim)
		}
		if _, err := library.EmbedTexts(ctx, []string{"embedding configuration check"}); err != nil {
			return fmt.Errorf("embedding preflight (existing vector stores unchanged): %w", err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(7261676, $1)`, library.EmbeddingConfigLockKey); err != nil {
		return fmt.Errorf("lock embedding configuration: %w", err)
	}
	if reset {
		if _, err := tx.ExecContext(ctx, `TRUNCATE photo_descriptions, photo_metadata, photo_queries`); err != nil {
			return fmt.Errorf("clear old embedding rows: %w", err)
		}
	}
	// dim was validated by EmbeddingDimensions before this function is called;
	// table names are fixed literals, never user input.
	for _, table := range []string{"photo_descriptions", "photo_metadata", "photo_queries"} {
		if current == dim {
			break
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN embedding TYPE halfvec(%d)`, table, dim)); err != nil {
			return fmt.Errorf("resize %s (migration rolled back): %w", table, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO embedding_config (singleton, model, dimensions)
		VALUES (true, $1, $2) ON CONFLICT (singleton) DO UPDATE SET
		model = EXCLUDED.model, dimensions = EXCLUDED.dimensions, updated_at = now()`, model, dim); err != nil {
		return fmt.Errorf("record embedding model: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if reset {
		fmt.Printf("Reconfigured vector stores: model=%s, %d → %d dimensions; old embeddings cleared for full reindex.\n", model, current, dim)
	}
	return nil
}

// A transaction-scoped advisory lock reserves a connection until workers have
// stopped. It prevents another indexer from switching model identity while
// this process is still writing vectors. Rollback releases it on every exit.
func lockIndex(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return nil, err
	}
	var acquired bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(7261676, 1)`).Scan(&acquired); err != nil {
		tx.Rollback()
		return nil, err
	}
	if !acquired {
		tx.Rollback()
		return nil, fmt.Errorf("another indexer is running for this database; stop it before starting a new run")
	}
	return tx, nil
}
