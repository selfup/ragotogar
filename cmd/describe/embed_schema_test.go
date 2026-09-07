package main

import (
	"testing"

	"ragotogar/library"
)

func TestSchemaCreates1024DimensionStoresAndPreservesExistingShape(t *testing.T) {
	t.Setenv("EMBED_MODEL", "text-embedding-qwen3-embedding-0.6b")
	t.Setenv("EMBED_DIM", "")
	db := newTempDB(t)
	dim, err := library.StoredEmbeddingDimensions(t.Context(), db)
	if err != nil || dim != 1024 {
		t.Fatalf("new schema: dim=%d err=%v", dim, err)
	}
	// A later describe invocation must not reset the vector stores just
	// because its shell has a different embedding-model setting.
	t.Setenv("EMBED_MODEL", "text-embedding-qwen3-embedding-4b")
	if err := initSchema(db); err != nil {
		t.Fatal(err)
	}
	dim, err = library.StoredEmbeddingDimensions(t.Context(), db)
	if err != nil || dim != 1024 {
		t.Fatalf("describe changed existing vector shape: dim=%d err=%v", dim, err)
	}
}
