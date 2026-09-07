package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ragotogar/library"
	"ragotogar/library/testdb"
)

var reindexAll = reindexSet{descriptions: true, metadata: true, queries: true}

func seedOldVectorStores(t *testing.T, db *sql.DB) {
	t.Helper()
	t.Setenv("EMBED_MODEL", "text-embedding-qwen3-embedding-4b")
	t.Setenv("EMBED_DIM", "")
	batchEmbedServer(t, 0, 0)
	seedPhotoForIndex(t, db, "old_photo", []string{"old query"})
	photo, err := library.LoadPhoto(db, "old_photo")
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range []documentStore{descriptionsStore, metadataStore, queriesStore} {
		if _, err := indexPhotoStore(context.Background(), db, photo, store); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"photo_descriptions", "photo_metadata", "photo_queries"} {
		if _, err := db.Exec(fmt.Sprintf(`CREATE INDEX idx_%s_embedding ON %s USING hnsw (embedding halfvec_cosine_ops)`, table, table)); err != nil {
			t.Fatal(err)
		}
	}
}

func assertOldVectorStores(t *testing.T, db *sql.DB) {
	t.Helper()
	dim, err := library.StoredEmbeddingDimensions(context.Background(), db)
	if err != nil || dim != 2560 {
		t.Fatalf("old schema changed: dim=%d err=%v", dim, err)
	}
	for _, table := range []string{"photo_descriptions", "photo_metadata", "photo_queries"} {
		if got := countStoreRows(t, db, table, "old_photo"); got != 1 {
			t.Errorf("%s lost old rows: %d", table, got)
		}
	}
}

func TestDimensionChangeRequiresFullReindex(t *testing.T) {
	db := newTempDB(t)
	seedOldVectorStores(t, db)
	t.Setenv("EMBED_MODEL", "text-embedding-qwen3-embedding-0.6b")
	recorder := batchEmbedServerWithDim(t, 0, 0, 1024)
	for _, flags := range []reindexSet{{}, {descriptions: true}, {descriptions: true, metadata: true}} {
		err := prepareVectorStores(context.Background(), db, 1024, flags)
		if err == nil || !strings.Contains(err.Error(), "-reindex=descriptions,metadata,queries") {
			t.Errorf("partial reindex must give migration command: %v", err)
		}
	}
	if len(recorder.batches()) != 0 {
		t.Error("made HTTP calls without a full reindex")
	}
	assertOldVectorStores(t, db)
}

func TestDimensionChangeProbesBeforeClearing(t *testing.T) {
	db := newTempDB(t)
	seedOldVectorStores(t, db)
	t.Setenv("EMBED_MODEL", "text-embedding-qwen3-embedding-0.6b")
	recorder := batchEmbedServer(t, 0, 0) // misconfigured server still serves 2560
	err := prepareVectorStores(context.Background(), db, 1024, reindexAll)
	if !errors.Is(err, library.ErrEmbeddingDimension) {
		t.Fatalf("expected dimension mismatch, got %v", err)
	}
	if len(recorder.batches()) != 1 {
		t.Error("expected one preflight request")
	}
	assertOldVectorStores(t, db)
}

func TestDimensionMigrationRollsBackAllStoresOnDDLFailure(t *testing.T) {
	db := newTempDB(t)
	seedOldVectorStores(t, db)
	// PostgreSQL refuses to change a column type referenced by a view. The
	// failure occurs after the first store's ALTER, exercising full rollback.
	if _, err := db.Exec(`CREATE VIEW embedding_dependency AS SELECT embedding FROM photo_metadata`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EMBED_MODEL", "text-embedding-qwen3-embedding-0.6b")
	batchEmbedServerWithDim(t, 0, 0, 1024)
	if err := prepareVectorStores(context.Background(), db, 1024, reindexAll); err == nil {
		t.Fatal("expected DDL dependency error")
	}
	assertOldVectorStores(t, db)
}

func TestRunReindexesAndSearchesWith1024Dimensions(t *testing.T) {
	db, dsn := testdb.NewWithDSN(t, "index_1024", testdb.SchemaSQL(indexTestSchema))
	seedOldVectorStores(t, db)
	for i := range 12 {
		seedPhotoForIndex(t, db, fmt.Sprintf("new_photo_%02d", i), []string{fmt.Sprintf("query %d", i)})
	}
	t.Setenv("EMBED_MODEL", "qwen3-embedding-0.6b-dwq")
	recorder := batchEmbedServerWithDim(t, 0, 0, 1024)
	if err := run(dsn, reindexAll, 2, 10); err != nil {
		t.Fatal(err)
	}
	dim, err := library.StoredEmbeddingDimensions(context.Background(), db)
	if err != nil || dim != 1024 {
		t.Fatalf("new schema: dim=%d err=%v", dim, err)
	}
	for _, table := range []string{"photo_descriptions", "photo_metadata", "photo_queries"} {
		var rows, indexes int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&rows); err != nil || rows != 13 {
			t.Errorf("%s: rows=%d err=%v", table, rows, err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE tablename = $1 AND indexdef LIKE '%USING hnsw%'`, table).Scan(&indexes); err != nil || indexes != 1 {
			t.Errorf("%s HNSW index not rebuilt: indexes=%d err=%v", table, indexes, err)
		}
	}
	photo, err := library.LoadPhoto(db, "old_photo")
	if err != nil || photo.FullDescription != "A quiet candid scene with warm light." || len(photo.GeneratedQueries) != 1 || photo.GeneratedQueries[0] != "old query" {
		t.Fatalf("migration changed source data: photo=%+v err=%v", photo, err)
	}
	assertStoredBatchVectors(t, db, recorder)
	calls := len(recorder.batches())
	runIndexOutputWith(t, dsn, 2, 10)
	if len(recorder.batches()) != calls {
		t.Error("restart should skip every completed 1024-dimensional store")
	}
	opts := library.DefaultSearchOptionsV2()
	opts.TopK = 0
	results, err := library.NewSearcher(db).SearchV2(context.Background(), "a cafe photo", opts)
	if err != nil || len(results) != 13 {
		t.Fatalf("1024-dimensional search: results=%d err=%v", len(results), err)
	}
}

func TestRunStopsOnDimensionMismatchWithinFirstBatch(t *testing.T) {
	schema := strings.ReplaceAll(indexTestSchema, "halfvec(2560)", "halfvec(1024)")
	db, dsn := testdb.NewWithDSN(t, "index_bad_dim", testdb.SchemaSQL(schema))
	if _, err := db.Exec(`UPDATE embedding_config SET model = 'text-embedding-qwen3-embedding-0.6b', dimensions = 1024`); err != nil {
		t.Fatal(err)
	}
	for i := range 23 {
		seedPhotoForIndex(t, db, fmt.Sprintf("photo_%02d", i), nil)
	}
	t.Setenv("EMBED_MODEL", "text-embedding-qwen3-embedding-0.6b")
	t.Setenv("EMBED_DIM", "")
	recorder := batchEmbedServer(t, 0, 0) // wrong dimension for this model
	err := run(dsn, reindexSet{}, 1, 10)
	if !errors.Is(err, library.ErrEmbeddingDimension) || len(recorder.batches()) != 1 {
		t.Fatalf("must stop after first batch: calls=%d err=%v", len(recorder.batches()), err)
	}
}
