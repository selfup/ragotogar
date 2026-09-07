package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"ragotogar/library"
	"ragotogar/library/testdb"
)

func TestSameDimensionModelSwitchRequiresFullReindexAndResumes(t *testing.T) {
	db, dsn := testdb.NewWithDSN(t, "index_model", testdb.SchemaSQL(indexTestSchema))
	seedOldVectorStores(t, db)
	t.Setenv("EMBED_MODEL", "different-model-same-dimension")
	recorder := batchEmbedServer(t, 0, 0)
	for _, flags := range []reindexSet{{}, {descriptions: true, metadata: true}} {
		if err := run(dsn, flags, 2, 10); err == nil || !strings.Contains(err.Error(), "-reindex=descriptions,metadata,queries") {
			t.Fatalf("must reject model switch: %v", err)
		}
	}
	if _, err := library.NewSearcher(db).SearchV2(t.Context(), "a photo", library.DefaultSearchOptionsV2()); err == nil {
		t.Fatal("search accepted vectors from the wrong model")
	}
	if len(recorder.batches()) != 0 {
		t.Fatal("mismatched model reached the embedding endpoint")
	}
	assertOldVectorStores(t, db)
	// A now-empty source must not leave the previous model's query vectors.
	if _, err := db.Exec(`UPDATE query_generations SET queries = '[]'::jsonb`); err != nil {
		t.Fatal(err)
	}
	if err := run(dsn, reindexAll, 2, 10); err != nil {
		t.Fatal(err)
	}
	config, err := library.StoredEmbeddingConfig(t.Context(), db)
	if err != nil || config == nil || config.Model != library.EmbedModel() || config.Dimensions != 2560 {
		t.Fatalf("new identity not recorded: config=%+v err=%v", config, err)
	}
	if got := countStoreRows(t, db, "photo_queries", "old_photo"); got != 0 {
		t.Fatalf("retained %d old-model query vectors", got)
	}
	before := len(recorder.batches())
	runIndexOutputWith(t, dsn, 2, 10)
	if len(recorder.batches()) != before {
		t.Fatal("restart re-embedded completed stores")
	}
	if _, err := library.NewSearcher(db).SearchV2(t.Context(), "a photo", library.DefaultSearchOptionsV2()); err != nil {
		t.Fatalf("matching model search failed: %v", err)
	}
}

func TestModelSwitchFailuresPreserveIdentityAndVectors(t *testing.T) {
	for _, phase := range []string{"probe", "record identity"} {
		t.Run(phase, func(t *testing.T) {
			db := newTempDB(t)
			seedOldVectorStores(t, db)
			t.Setenv("EMBED_MODEL", "new-model")
			if phase == "probe" {
				batchEmbedServer(t, 1, http.StatusBadRequest)
			} else {
				batchEmbedServer(t, 0, 0)
				if _, err := db.Exec(`CREATE FUNCTION reject_model_change() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN RAISE EXCEPTION 'identity failure'; END $$;
					CREATE TRIGGER reject_model_change BEFORE UPDATE ON embedding_config
					FOR EACH ROW EXECUTE FUNCTION reject_model_change()`); err != nil {
					t.Fatal(err)
				}
			}
			if err := prepareVectorStores(t.Context(), db, 2560, reindexAll); err == nil {
				t.Fatal("expected model switch failure")
			}
			assertOldVectorStores(t, db)
			config, err := library.StoredEmbeddingConfig(t.Context(), db)
			if err != nil || config == nil || config.Model != "text-embedding-qwen3-embedding-4b" {
				t.Fatalf("failed switch changed identity: config=%+v err=%v", config, err)
			}
		})
	}
}

func TestUnrecordedModelsRequireReindexOnlyWhenVectorsExist(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "legacy"}[legacy], func(t *testing.T) {
			db, dsn := testdb.NewWithDSN(t, "index_unrecorded", testdb.SchemaSQL(indexTestSchema))
			if legacy {
				seedOldVectorStores(t, db)
			} else {
				seedPhotoForIndex(t, db, "fresh", nil)
			}
			if _, err := db.Exec(`DROP TABLE embedding_config`); err != nil {
				t.Fatal(err)
			}
			recorder := batchEmbedServer(t, 0, 0)
			err := run(dsn, reindexSet{}, 1, 10)
			if legacy {
				if err == nil || len(recorder.batches()) != 0 {
					t.Fatalf("legacy vectors silently adopted: err=%v", err)
				}
				if err := run(dsn, reindexAll, 1, 10); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatalf("fresh index needs no forced rebuild: %v", err)
			}
			if err := library.CheckEmbeddingConfig(t.Context(), db, library.EmbedModel(), 2560); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIndexLockPreventsConcurrentRunsAndReleases(t *testing.T) {
	db, dsn := testdb.NewWithDSN(t, "index_lock", testdb.SchemaSQL(indexTestSchema))
	seedPhotoForIndex(t, db, "photo", nil)
	lock, err := lockIndex(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	recorder := batchEmbedServer(t, 0, 0)
	if err := run(dsn, reindexSet{}, 2, 10); err == nil || !strings.Contains(err.Error(), "another indexer") {
		t.Fatalf("parallel indexer accepted: %v", err)
	}
	if len(recorder.batches()) != 0 {
		t.Fatal("parallel indexer called embed endpoint")
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := run(dsn, reindexSet{}, 2, 10); err != nil {
		t.Fatalf("index lock was not released: %v", err)
	}
}

func TestModelSwitchWaitsForVectorReaders(t *testing.T) {
	db := newTempDB(t)
	seedOldVectorStores(t, db)
	reader, err := library.LockEmbeddingConfig(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	// Matching incremental indexing remains possible during a search/build.
	if err := prepareVectorStores(t.Context(), db, 2560, reindexSet{}); err != nil {
		t.Fatalf("reader blocked incremental indexing: %v", err)
	}
	t.Setenv("EMBED_MODEL", "new-model")
	batchEmbedServer(t, 0, 0)
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	if err := prepareVectorStores(ctx, db, 2560, reindexAll); err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("model switch did not wait for reader: %v", err)
	}
	assertOldVectorStores(t, db)
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := prepareVectorStores(t.Context(), db, 2560, reindexAll); err != nil {
		t.Fatalf("model switch failed after reader finished: %v", err)
	}
	if err := library.CheckEmbeddingConfig(t.Context(), db, "new-model", 2560); err != nil {
		t.Fatal(err)
	}
}
