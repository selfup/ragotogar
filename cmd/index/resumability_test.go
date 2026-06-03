package main

import (
	"context"
	"database/sql"
	"testing"

	"ragotogar/library"
)

// TestIndexDescriptions_HappyPath writes one chunk row per chunk produced
// by library.Chunk for the seeded description. Counts the per-store row
// delta and confirms the embed endpoint was called exactly once (single
// batch of N chunks → one HTTP request via library.EmbedTexts).
func TestIndexDescriptions_HappyPath(t *testing.T) {
	db := newTempDB(t)
	embedURL, calls := stubEmbedServer(t)
	t.Setenv("EMBED_ENDPOINT", embedURL)

	seedPhotoForIndex(t, db, "p1", nil)
	photo, err := library.LoadPhoto(db, "p1")
	if err != nil {
		t.Fatalf("LoadPhoto: %v", err)
	}

	added, err := indexDescriptions(context.Background(), db, photo)
	if err != nil {
		t.Fatalf("indexDescriptions: %v", err)
	}
	if added <= 0 {
		t.Fatalf("added = %d, want >= 1", added)
	}
	if got := countStoreRows(t, db, "photo_descriptions", "p1"); got != added {
		t.Errorf("photo_descriptions rows = %d, want %d (return value)", got, added)
	}
	if calls.Load() != 1 {
		t.Errorf("embed calls = %d, want 1 (single batch)", calls.Load())
	}
}

// TestIndexDescriptions_IdempotentReplaceOnRerun: the DELETE-then-INSERT
// transaction shape means a second call leaves only the latest rows, not
// duplicates. Critical invariant — without it, repeated indexing inflates
// the chunks table linearly per run.
func TestIndexDescriptions_IdempotentReplaceOnRerun(t *testing.T) {
	db := newTempDB(t)
	embedURL, _ := stubEmbedServer(t)
	t.Setenv("EMBED_ENDPOINT", embedURL)

	seedPhotoForIndex(t, db, "p1", nil)
	photo, _ := library.LoadPhoto(db, "p1")

	first, err := indexDescriptions(context.Background(), db, photo)
	if err != nil {
		t.Fatalf("first indexDescriptions: %v", err)
	}
	second, err := indexDescriptions(context.Background(), db, photo)
	if err != nil {
		t.Fatalf("second indexDescriptions: %v", err)
	}
	if first != second {
		t.Errorf("rerun added a different count (%d vs %d)", first, second)
	}
	if got := countStoreRows(t, db, "photo_descriptions", "p1"); got != second {
		t.Errorf("after rerun: rows = %d, want %d (no dupes)", got, second)
	}
}

// TestIndexMetadata_WritesOneRow: photo_metadata is one-row-per-photo by
// design — verify the constraint is honored end-to-end.
func TestIndexMetadata_WritesOneRow(t *testing.T) {
	db := newTempDB(t)
	embedURL, _ := stubEmbedServer(t)
	t.Setenv("EMBED_ENDPOINT", embedURL)

	seedPhotoForIndex(t, db, "p1", nil)
	photo, _ := library.LoadPhoto(db, "p1")

	added, err := indexMetadata(context.Background(), db, photo)
	if err != nil {
		t.Fatalf("indexMetadata: %v", err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1", added)
	}
	if got := countStoreRows(t, db, "photo_metadata", "p1"); got != 1 {
		t.Errorf("photo_metadata rows = %d, want 1", got)
	}
}

// TestIndexQueries_HappyPath: when query_generations holds N phrasings,
// photo_queries gets exactly N rows.
func TestIndexQueries_HappyPath(t *testing.T) {
	db := newTempDB(t)
	embedURL, _ := stubEmbedServer(t)
	t.Setenv("EMBED_ENDPOINT", embedURL)

	queries := []string{"warm afternoon at a cafe", "candid portrait", "X100VI street shot"}
	seedPhotoForIndex(t, db, "p1", queries)
	photo, _ := library.LoadPhoto(db, "p1")

	added, err := indexQueries(context.Background(), db, photo)
	if err != nil {
		t.Fatalf("indexQueries: %v", err)
	}
	if added != len(queries) {
		t.Errorf("added = %d, want %d", added, len(queries))
	}
	if got := countStoreRows(t, db, "photo_queries", "p1"); got != len(queries) {
		t.Errorf("photo_queries rows = %d, want %d", got, len(queries))
	}
}

// TestIndexQueries_NoQueriesReturnsZero: photos without GeneratedQueries
// (no query_generations row) return (0, nil). Caller treats this as a
// benign skip — a re-describe will regenerate.
func TestIndexQueries_NoQueriesReturnsZero(t *testing.T) {
	db := newTempDB(t)
	embedURL, calls := stubEmbedServer(t)
	t.Setenv("EMBED_ENDPOINT", embedURL)

	seedPhotoForIndex(t, db, "p1", nil) // no queries seeded
	photo, _ := library.LoadPhoto(db, "p1")

	added, err := indexQueries(context.Background(), db, photo)
	if err != nil {
		t.Fatalf("indexQueries: %v", err)
	}
	if added != 0 {
		t.Errorf("added = %d, want 0 (no queries)", added)
	}
	if calls.Load() != 0 {
		t.Errorf("embed calls = %d, want 0 (early return before HTTP)", calls.Load())
	}
}

// TestPartialFailure_DescriptionsSurviveWhenMetadataFails is the
// load-bearing resumability invariant. Indexes descriptions first
// (succeeds), then metadata (fails because the embed endpoint is broken).
// The descriptions rows must remain intact — never silently half-indexed.
// This is exactly the "each store its own transaction" guarantee.
func TestPartialFailure_DescriptionsSurviveWhenMetadataFails(t *testing.T) {
	db := newTempDB(t)
	embedURL, _ := stubEmbedServer(t)
	t.Setenv("EMBED_ENDPOINT", embedURL)

	seedPhotoForIndex(t, db, "p1", nil)
	photo, _ := library.LoadPhoto(db, "p1")

	// Step 1: descriptions succeeds.
	descAdded, err := indexDescriptions(context.Background(), db, photo)
	if err != nil {
		t.Fatalf("indexDescriptions: %v", err)
	}
	descBefore := countStoreRows(t, db, "photo_descriptions", "p1")
	if descBefore != descAdded {
		t.Fatalf("photo_descriptions setup mismatch: %d", descBefore)
	}

	// Step 2: point EMBED_ENDPOINT at a server that always 500s. The retry
	// layer in library/http.go gives up after a few attempts and returns
	// an error to indexMetadata, which surfaces without writing anything.
	badURL, _ := stubEmbedServerAlwaysFails(t)
	t.Setenv("EMBED_ENDPOINT", badURL)

	_, err = indexMetadata(context.Background(), db, photo)
	if err == nil {
		t.Fatalf("indexMetadata should have failed under broken embed endpoint")
	}

	// Step 3: descriptions rows must still be there.
	if got := countStoreRows(t, db, "photo_descriptions", "p1"); got != descBefore {
		t.Errorf("photo_descriptions rows = %d, want %d (must survive metadata failure)", got, descBefore)
	}
	if got := countStoreRows(t, db, "photo_metadata", "p1"); got != 0 {
		t.Errorf("photo_metadata rows = %d, want 0 (failed write should leave nothing)", got)
	}
}

// TestPartialFailure_MetadataFailureRollsBackOwnTransaction: a failing
// metadata insert (synthetic constraint violation) must NOT leave a
// partial photo_metadata row. The defer tx.Rollback() in indexMetadata is
// what enforces this. We can't reach a SQL-side failure without invasive
// hooks, so this test uses the embed-fail proxy from above and confirms
// no rows were written.
func TestPartialFailure_MetadataFailureLeavesNoRows(t *testing.T) {
	db := newTempDB(t)
	// Embed fails on the very first call.
	badURL, _ := stubEmbedServerAlwaysFails(t)
	t.Setenv("EMBED_ENDPOINT", badURL)

	seedPhotoForIndex(t, db, "p1", nil)
	photo, _ := library.LoadPhoto(db, "p1")

	_, err := indexMetadata(context.Background(), db, photo)
	if err == nil {
		t.Fatal("indexMetadata should have failed under broken embed")
	}
	if got := countStoreRows(t, db, "photo_metadata", "p1"); got != 0 {
		t.Errorf("photo_metadata rows = %d, want 0", got)
	}
}

// TestLoadExistingV2_EmptyTable: with no rows in the named table,
// loadExistingV2 returns an empty (not nil) map so callers can index .[k]
// without nil-guard.
func TestLoadExistingV2_EmptyTable(t *testing.T) {
	db := newTempDB(t)

	for _, table := range []string{"photo_descriptions", "photo_metadata", "photo_queries"} {
		got, err := loadExistingV2(db, table, false)
		if err != nil {
			t.Fatalf("loadExistingV2(%s): %v", table, err)
		}
		if got == nil {
			t.Errorf("%s: got nil map", table)
		}
		if len(got) != 0 {
			t.Errorf("%s: got %v, want empty", table, got)
		}
	}
}

// TestLoadExistingV2_ReturnsExistingAtSchemaVersion: rows at
// v2SchemaVersion populate the map; rows at other versions don't appear.
// The skip-if-exists path relies on this precise filter.
func TestLoadExistingV2_ReturnsExistingAtSchemaVersion(t *testing.T) {
	db := newTempDB(t)
	embedURL, _ := stubEmbedServer(t)
	t.Setenv("EMBED_ENDPOINT", embedURL)

	seedPhotoForIndex(t, db, "indexed", nil)
	photo, _ := library.LoadPhoto(db, "indexed")
	if _, err := indexDescriptions(context.Background(), db, photo); err != nil {
		t.Fatalf("indexDescriptions: %v", err)
	}

	// Also insert a row at a DIFFERENT schema_version directly — this row
	// should NOT appear in the loadExistingV2 result for library.V2SchemaVersion.
	seedPhotoForIndex(t, db, "stale", nil)
	makeStaleRow(t, db, "stale", library.V2SchemaVersion+1)

	got, err := loadExistingV2(db, "photo_descriptions", false)
	if err != nil {
		t.Fatalf("loadExistingV2: %v", err)
	}
	if !got["indexed"] {
		t.Errorf("expected 'indexed' in result, got %v", got)
	}
	if got["stale"] {
		t.Errorf("'stale' is at schema_version+1; should be filtered out, got %v", got)
	}
}

// TestLoadExistingV2_ReindexBypassesLookup: when reindex=true, the
// function short-circuits and returns an empty map without touching the
// DB. That's the -reindex=<store> path — caller treats every photo as
// needing re-indexing regardless of what's on disk.
func TestLoadExistingV2_ReindexBypassesLookup(t *testing.T) {
	db := newTempDB(t)
	embedURL, _ := stubEmbedServer(t)
	t.Setenv("EMBED_ENDPOINT", embedURL)

	seedPhotoForIndex(t, db, "p1", nil)
	photo, _ := library.LoadPhoto(db, "p1")
	if _, err := indexDescriptions(context.Background(), db, photo); err != nil {
		t.Fatalf("indexDescriptions: %v", err)
	}

	got, err := loadExistingV2(db, "photo_descriptions", true)
	if err != nil {
		t.Fatalf("loadExistingV2: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("reindex=true should return empty, got %v", got)
	}
}

// TestIndexMetadata_EmptyTextSkips: a photo with no EXIF and no
// description has no metadata text — indexMetadata should return (0, nil)
// without hitting the embed endpoint. Caller treats this as "nothing to
// do" rather than an error.
func TestIndexMetadata_EmptyTextSkips(t *testing.T) {
	db := newTempDB(t)
	embedURL, calls := stubEmbedServer(t)
	t.Setenv("EMBED_ENDPOINT", embedURL)

	// Seed the photo row only (no exif, no descriptions).
	if _, err := db.Exec(`INSERT INTO photos (id, name) VALUES ('bare', 'bare')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	photo, err := library.LoadPhoto(db, "bare")
	if err != nil {
		t.Fatalf("LoadPhoto: %v", err)
	}

	added, err := indexMetadata(context.Background(), db, photo)
	if err != nil {
		t.Fatalf("indexMetadata: %v", err)
	}
	if added != 0 {
		t.Errorf("added = %d, want 0 (empty metadata text)", added)
	}
	if calls.Load() != 0 {
		t.Errorf("embed calls = %d, want 0 (early return)", calls.Load())
	}
}

// makeStaleRow inserts a photo_descriptions row at a non-current
// schema_version. Used to confirm loadExistingV2 filters by version.
// The embedding is a 2560-dim zero vector represented as the halfvec
// literal '[0,0,...,0]' — pgvector accepts this directly.
func makeStaleRow(t *testing.T, db *sql.DB, photoID string, schemaVersion int) {
	t.Helper()
	// Build [0,0,...,0] of length 2560.
	buf := make([]byte, 0, 5121)
	buf = append(buf, '[')
	for i := range 2560 {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, '0')
	}
	buf = append(buf, ']')
	if _, err := db.Exec(`
		INSERT INTO photo_descriptions
		    (photo_id, schema_version, chunk_index, chunk_text, embedding)
		VALUES ($1, $2, 0, 'stale text', $3::halfvec)
	`, photoID, schemaVersion, string(buf)); err != nil {
		t.Fatalf("makeStaleRow: %v", err)
	}
}
