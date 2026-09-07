package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/pgvector/pgvector-go"

	"ragotogar/library"
)

type documentStore struct {
	name      string
	documents func(*library.Photo) []string
	write     func(context.Context, *sql.DB, string, []string, [][]float32) (int, error)
}

var (
	descriptionsStore = documentStore{
		name: "descriptions",
		documents: func(p *library.Photo) []string {
			return library.Chunk(library.BuildDescriptionDocument(p))
		},
		write: writeDescriptions,
	}
	metadataStore = documentStore{
		name: "metadata",
		documents: func(p *library.Photo) []string {
			text := library.BuildMetadataDocument(p)
			if strings.TrimSpace(text) == "" {
				return nil
			}
			return []string{text}
		},
		write: writeMetadata,
	}
	queriesStore = documentStore{
		name:      "queries",
		documents: library.BuildQueryDocuments,
		write:     writeQueries,
	}
)

// All inputs for a photo/store are embedded before replacing its rows.
// Each replacement stays in its own transaction, regardless of HTTP batching.
func writeDescriptions(ctx context.Context, db *sql.DB, photoID string, chunks []string, embeddings [][]float32) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM photo_descriptions WHERE photo_id = $1 AND schema_version = $2",
		photoID, library.V2SchemaVersion,
	); err != nil {
		return 0, fmt.Errorf("delete existing description rows: %w", err)
	}
	for i, text := range chunks {
		vec := pgvector.NewHalfVector(embeddings[i])
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO photo_descriptions
			    (photo_id, schema_version, chunk_index, chunk_text, embedding)
			 VALUES ($1, $2, $3, $4, $5)`,
			photoID, library.V2SchemaVersion, i, text, vec,
		); err != nil {
			return 0, fmt.Errorf("insert description chunk %d: %w", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(chunks), nil
}

func writeMetadata(ctx context.Context, db *sql.DB, photoID string, texts []string, embeddings [][]float32) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM photo_metadata WHERE photo_id = $1 AND schema_version = $2",
		photoID, library.V2SchemaVersion,
	); err != nil {
		return 0, fmt.Errorf("delete existing metadata row: %w", err)
	}
	vec := pgvector.NewHalfVector(embeddings[0])
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO photo_metadata
		    (photo_id, schema_version, metadata_text, embedding)
		 VALUES ($1, $2, $3, $4)`,
		photoID, library.V2SchemaVersion, texts[0], vec,
	); err != nil {
		return 0, fmt.Errorf("insert metadata row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return 1, nil
}

func writeQueries(ctx context.Context, db *sql.DB, photoID string, queries []string, embeddings [][]float32) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM photo_queries WHERE photo_id = $1 AND schema_version = $2",
		photoID, library.V2SchemaVersion,
	); err != nil {
		return 0, fmt.Errorf("delete existing query rows: %w", err)
	}
	for i, text := range queries {
		vec := pgvector.NewHalfVector(embeddings[i])
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO photo_queries
			    (photo_id, schema_version, query_index, query_text, embedding)
			 VALUES ($1, $2, $3, $4, $5)`,
			photoID, library.V2SchemaVersion, i, text, vec,
		); err != nil {
			return 0, fmt.Errorf("insert query row %d: %w", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(queries), nil
}
