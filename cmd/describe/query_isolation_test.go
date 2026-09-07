package main

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pgvector/pgvector-go"

	"ragotogar/library"
)

const isolationProse = "Subject: cedar trees\nMood: peaceful"
const isolationResponse = isolationProse + "\n- **Queries** (search phrases):\nzeppelins at sunset\nsubmarines at dawn"

func TestInsertPhotoIsolatesGeneratedQueries(t *testing.T) {
	db := newTempDB(t)
	// Exercise both insert and UPSERT, including replacement of the raw audit
	// response. The marker words occur only in generated search phrasings.
	for _, raw := range []string{"Subject: old scene\nQueries: old phrasing", isolationResponse} {
		if err := insertPhoto(db, "forest", "/forest.jpg", exifData{}, raw,
			parseDescriptionFields(raw), []byte{0xff}, "vision", 1, 2); err != nil {
			t.Fatal(err)
		}
	}
	assertIsolatedPhoto(t, db, "forest", isolationResponse)
}

func assertIsolatedPhoto(t *testing.T, db *sql.DB, id, wantRaw string) {
	t.Helper()
	var prose, raw string
	var queriesJSON []byte
	var treeMatch, queryMatch bool
	if err := db.QueryRow(`
		SELECT d.full_description, i.raw_response, q.queries,
		       d.fts @@ plainto_tsquery('english', 'cedar'),
		       d.fts @@ plainto_tsquery('english', 'zeppelins')
		FROM descriptions d JOIN inference i USING (photo_id)
		JOIN query_generations q USING (photo_id) WHERE d.photo_id = $1
	`, id).Scan(&prose, &raw, &queriesJSON, &treeMatch, &queryMatch); err != nil {
		t.Fatal(err)
	}
	if prose != isolationProse || raw != wantRaw {
		t.Errorf("prose/raw mismatch: prose=%q raw=%q", prose, raw)
	}
	if !treeMatch || queryMatch {
		t.Errorf("FTS contamination: scene match=%v query-only match=%v", treeMatch, queryMatch)
	}
	var queries []string
	if err := json.Unmarshal(queriesJSON, &queries); err != nil {
		t.Fatal(err)
	}
	if want := []string{"zeppelins at sunset", "submarines at dawn"}; !reflect.DeepEqual(queries, want) {
		t.Errorf("generated queries changed: %v", queries)
	}
	photo, err := library.LoadPhoto(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if doc := library.BuildDescriptionDocument(photo); strings.Contains(doc, "zeppelins") {
		t.Errorf("query leaked into embedding/verifier input: %q", doc)
	}
}

func seedV14QueryIsolation(t *testing.T, db *sql.DB) {
	t.Helper()
	vec := pgvector.NewHalfVector(make([]float32, library.EmbedDim))
	for _, id := range []string{"affected", "clean", "existing_raw"} {
		if err := insertPhoto(db, id, "/forest.jpg", exifData{}, isolationResponse,
			parseDescriptionFields(isolationResponse), []byte{0xff}, "vision", 1, 2); err != nil {
			t.Fatal(err)
		}
		// Recreate the v14 storage shape, without running new ingestion code
		// during migration. A clean photo's vectors/cache must remain intact.
		if id != "clean" {
			if _, err := db.Exec(`UPDATE descriptions SET full_description = $2 WHERE photo_id = $1`, id, isolationResponse); err != nil {
				t.Fatal(err)
			}
		}
		var raw any
		if id == "existing_raw" {
			raw = "previously archived provider response"
		}
		if _, err := db.Exec(`UPDATE inference SET raw_response = $2 WHERE photo_id = $1`, id, raw); err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{
			`INSERT INTO photo_descriptions (photo_id, schema_version, chunk_index, chunk_text, embedding) VALUES ($1, 2, 0, 'old chunk', $2)`,
			`INSERT INTO photo_metadata (photo_id, schema_version, metadata_text, embedding) VALUES ($1, 2, 'camera tokens', $2)`,
			`INSERT INTO photo_queries (photo_id, schema_version, query_index, query_text, embedding) VALUES ($1, 2, 0, 'zeppelins at sunset', $2)`,
		} {
			if _, err := db.Exec(query, id, vec); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`INSERT INTO verify_cache (query, photo_id, verify_model, verdict, verified_at)
			VALUES ('zeppelins', $1, 'verifier', true, now())`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM schema_version;
		INSERT INTO schema_version (version, applied_at) VALUES (14, now())`); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateV15RepairsExistingQueries(t *testing.T) {
	db := newTempDB(t)
	seedV14QueryIsolation(t, db)
	for range 2 { // Entire startup path must be safe to rerun.
		if err := initSchema(db); err != nil {
			t.Fatal(err)
		}
	}
	assertIsolatedPhoto(t, db, "affected", isolationResponse)
	assertIsolatedPhoto(t, db, "existing_raw", "previously archived provider response")
	for table, want := range map[string]int{
		"photo_descriptions": 1, "verify_cache": 1,
		"photo_metadata": 3, "photo_queries": 3, "query_generations": 3,
	} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Errorf("%s: got %d rows, want %d", table, count, want)
		}
	}
	for _, table := range []string{"photo_descriptions", "verify_cache"} {
		var id string
		if err := db.QueryRow("SELECT photo_id FROM " + table).Scan(&id); err != nil || id != "clean" {
			t.Errorf("%s should retain the clean photo: id=%q err=%v", table, id, err)
		}
	}
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 15 {
		t.Errorf("migration version=%d err=%v", version, err)
	}
}

func TestMigrateV15RollsBackOnFailure(t *testing.T) {
	db := newTempDB(t)
	seedV14QueryIsolation(t, db)
	if _, err := db.Exec(`
		CREATE FUNCTION reject_invalidation() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'synthetic invalidation failure'; END $$;
		CREATE TRIGGER reject_invalidation BEFORE DELETE ON photo_descriptions
		FOR EACH ROW EXECUTE FUNCTION reject_invalidation()
	`); err != nil {
		t.Fatal(err)
	}
	if err := initSchema(db); err == nil || !strings.Contains(err.Error(), "synthetic invalidation failure") {
		t.Fatalf("expected injected migration failure, got %v", err)
	}
	var prose string
	var raw sql.NullString
	var version int
	if err := db.QueryRow(`SELECT d.full_description, i.raw_response FROM descriptions d
		JOIN inference i USING (photo_id) WHERE photo_id = 'affected'`).Scan(&prose, &raw); err != nil {
		t.Fatal(err)
	}
	if prose != isolationResponse || raw.Valid {
		t.Errorf("partial migration survived rollback: prose=%q raw=%v", prose, raw)
	}
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 14 {
		t.Errorf("failed migration advanced version: version=%d err=%v", version, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_invalidation ON photo_descriptions`); err != nil {
		t.Fatal(err)
	}
	if err := initSchema(db); err != nil {
		t.Fatalf("retry: %v", err)
	}
	assertIsolatedPhoto(t, db, "affected", isolationResponse)
}
