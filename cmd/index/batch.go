package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"ragotogar/library"
)

const defaultBatchSize = 10

type storeResult struct {
	added int
	err   error
}

func fatalEmbeddingError(err error) bool {
	return errors.Is(err, library.ErrNonRetryable) || errors.Is(err, library.ErrEmbeddingDimension)
}

// indexStoreBatch packs one store's documents across photos into bounded
// requests. Every input stays separate; descriptions and query phrasings are
// never concatenated. A photo's chunks can span requests, but its rows are
// replaced only after ALL its embeddings succeed. A failed request leaves
// each affected photo/store untouched; missing stores remain resumable.
//
// Results correspond to photos in input order. Only cancellation and permanent
// endpoint failures return a top-level error; other failures are per photo.
// batchSize must be positive (validated by run before connecting).
func indexStoreBatch(ctx context.Context, db *sql.DB, store documentStore, photos []*library.Photo, batchSize int) ([]storeResult, error) {
	results := make([]storeResult, len(photos))
	var texts []string
	var owners []int
	offsets := make([]int, len(photos)+1)
	for i, photo := range photos {
		docs := store.documents(photo)
		texts = append(texts, docs...)
		for range docs {
			owners = append(owners, i)
		}
		offsets[i+1] = len(texts)
	}

	vectors := make([][]float32, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+batchSize, len(texts))
		embeddings, err := library.EmbedTexts(ctx, texts[start:end])
		if fatalEmbeddingError(err) || ctx.Err() != nil {
			if err == nil {
				err = ctx.Err()
			}
			return nil, fmt.Errorf("%s: %w", photos[owners[start]].Name, err)
		}
		if err != nil {
			for _, owner := range owners[start:end] {
				results[owner].err = err
			}
			continue
		}
		copy(vectors[start:end], embeddings)
	}

	for i, photo := range photos {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		start, end := offsets[i], offsets[i+1]
		if results[i].err != nil || start == end {
			continue
		}
		results[i].added, results[i].err = store.write(ctx, db, photo.Name, texts[start:end], vectors[start:end])
	}
	return results, nil
}
