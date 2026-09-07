package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// EmbeddingConfigSchema belongs to the derived vector stores. Both describe
// startup and index startup may create it; only the indexer records identity.
// Existing vectors are never assigned a guessed model during schema creation.
const EmbeddingConfigSchema = `CREATE TABLE IF NOT EXISTS embedding_config (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    model TEXT NOT NULL CHECK (model <> ''),
    dimensions INTEGER NOT NULL CHECK (dimensions BETWEEN 1 AND 4000),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`

type EmbeddingConfig struct {
	Model      string
	Dimensions int
}

// EmbeddingConfigLockKey is shared by vector readers and model-switch
// transactions. The independent indexer-run lock uses key (7261676, 1).
const EmbeddingConfigLockKey = 2

// LockEmbeddingConfig holds model identity stable while a search or artifact
// build reads vectors. Rollback the returned transaction when finished. Normal
// incremental writes need no exclusive configuration lock and can continue.
func LockEmbeddingConfig(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared(7261676, $1)`, EmbeddingConfigLockKey); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("lock embedding configuration: %w", err)
	}
	return tx, nil
}

// StoredEmbeddingConfig returns nil for libraries whose vector provenance
// has not been recorded yet, including libraries predating this table.
func StoredEmbeddingConfig(ctx context.Context, db *sql.DB) (*EmbeddingConfig, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('embedding_config') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("read embedding configuration: %w", err)
	}
	if !exists {
		return nil, nil
	}
	var config EmbeddingConfig
	err := db.QueryRowContext(ctx, `SELECT model, dimensions FROM embedding_config WHERE singleton`).Scan(&config.Model, &config.Dimensions)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read embedding configuration: %w", err)
	}
	return &config, nil
}

// CheckEmbeddingConfig prevents query vectors and sealed artifacts from
// being associated with a different model, even when dimensions match.
func CheckEmbeddingConfig(ctx context.Context, db *sql.DB, model string, dim int) error {
	config, err := StoredEmbeddingConfig(ctx, db)
	if err != nil {
		return err
	}
	if config == nil {
		return fmt.Errorf("vector stores have no recorded embedding model; run scripts/index.sh -reindex=descriptions,metadata,queries with the intended EMBED_MODEL")
	}
	if config.Model != model || config.Dimensions != dim {
		return fmt.Errorf("vector stores use model %q (%d dimensions), requested %q (%d); use the recorded model/settings or run scripts/index.sh -reindex=descriptions,metadata,queries to switch", config.Model, config.Dimensions, model, dim)
	}
	actual, err := StoredEmbeddingDimensions(ctx, db)
	if err != nil {
		return err
	}
	if actual != dim {
		return fmt.Errorf("recorded embedding dimension %d differs from vector columns (%d); run scripts/index.sh -reindex=descriptions,metadata,queries", dim, actual)
	}
	return nil
}
