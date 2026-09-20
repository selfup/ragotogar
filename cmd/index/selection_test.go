package main

import (
	"strings"
	"testing"

	"ragotogar/library/testdb"
)

func TestScopedReindexCannotChangeEmbeddingModel(t *testing.T) {
	db, dsn := testdb.NewWithDSN(t, "index_scoped", testdb.SchemaSQL(indexTestSchema))
	seedOldVectorStores(t, db)
	t.Setenv("EMBED_MODEL", "different-model-same-dimension")
	recorder := batchEmbedServer(t, 0, 0)
	if err := runSelected(dsn, reindexAll, 1, 10, []string{"old_photo"}); err == nil || !strings.Contains(err.Error(), "vector stores use model") {
		t.Fatalf("scoped reindex authorized a global model switch: %v", err)
	}
	assertOldVectorStores(t, db)
	if len(recorder.batches()) != 0 {
		t.Fatal("mismatched scoped run reached embedding endpoint")
	}
}

func TestEmptySelectionDoesNoWork(t *testing.T) {
	if err := runSelected("invalid DSN", reindexAll, 1, 10, []string{}); err != nil {
		t.Fatalf("empty selection accessed database: %v", err)
	}
}

func TestScopedRunReportsPerPhotoFailures(t *testing.T) {
	db, dsn := testdb.NewWithDSN(t, "index_scoped_failure", testdb.SchemaSQL(indexTestSchema))
	seedPhotoForIndex(t, db, "photo", nil)
	batchEmbedServer(t, 1, 200) // malformed response is a per-photo error
	err := runSelected(dsn, reindexAll, 1, 10, []string{"photo"})
	if err == nil || !strings.Contains(err.Error(), "indexing finished with") {
		t.Fatalf("per-photo indexing error hidden: %v", err)
	}
}
