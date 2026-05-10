package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ragotogar/library/testdb"
)

// indexTestSchema is the minimum schema cmd/index's helpers touch:
// photos / exif / descriptions / classified / query_generations (read by
// LoadPhoto) + the three v12 vector stores (write target). FTS columns
// are omitted from descriptions/exif here because cmd/index never reads
// them — only cmd/web's FTS arm does.
const indexTestSchema = `
CREATE TABLE photos (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    file_path     TEXT,
    file_basename TEXT
);
CREATE TABLE exif (
    photo_id TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    camera_make TEXT, camera_model TEXT, lens_model TEXT, lens_info TEXT,
    date_taken TEXT, focal_length_mm DOUBLE PRECISION, focal_length_35mm DOUBLE PRECISION,
    f_number DOUBLE PRECISION, exposure_time_seconds DOUBLE PRECISION, iso INTEGER,
    exposure_compensation DOUBLE PRECISION, exposure_mode TEXT,
    white_balance TEXT, flash TEXT, software TEXT, artist TEXT
);
CREATE TABLE descriptions (
    photo_id TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    subject TEXT, setting TEXT, light TEXT, colors TEXT, composition TEXT,
    vantage TEXT, ground_truth TEXT, condition TEXT, mood TEXT,
    full_description TEXT
);
CREATE TABLE classified (
    photo_id TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    pov_container TEXT, pov_altitude TEXT, pov_angle TEXT,
    subject_altitude TEXT, subject_category TEXT[],
    subject_distance TEXT, subject_count TEXT, animal_count TEXT,
    scene_time_of_day TEXT, scene_indoor_outdoor TEXT, scene_weather TEXT,
    framing TEXT[], motion TEXT, color_palette TEXT
);
CREATE TABLE query_generations (
    photo_id        TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    schema_version  INTEGER NOT NULL,
    model           TEXT NOT NULL,
    prompt_hash     TEXT NOT NULL,
    queries         JSONB NOT NULL
);
CREATE TABLE photo_descriptions (
    photo_id        TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    schema_version  INTEGER NOT NULL,
    chunk_index     INTEGER NOT NULL,
    chunk_text      TEXT NOT NULL,
    embedding       halfvec(2560) NOT NULL,
    UNIQUE (photo_id, schema_version, chunk_index)
);
CREATE TABLE photo_metadata (
    photo_id        TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    schema_version  INTEGER NOT NULL,
    metadata_text   TEXT NOT NULL,
    embedding       halfvec(2560) NOT NULL,
    UNIQUE (photo_id, schema_version)
);
CREATE TABLE photo_queries (
    photo_id        TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    schema_version  INTEGER NOT NULL,
    query_index     INTEGER NOT NULL,
    query_text      TEXT NOT NULL,
    embedding       halfvec(2560) NOT NULL,
    UNIQUE (photo_id, schema_version, query_index)
);
`

func newTempDB(t *testing.T) *sql.DB {
	t.Helper()
	return testdb.New(t, "index", testdb.SchemaSQL(indexTestSchema))
}

// stubEmbedServer returns an OpenAI-shaped embeddings endpoint that
// answers any batch with one 2560-dim embedding per input. Variation
// across positions (i%5 * 0.1) gives non-zero, non-pathological vectors
// that pgvector accepts as halfvec(2560). callCount lets tests assert
// the mock was hit (or not).
//
// failNext returns a server that errors on the Nth call (counting from 1)
// — used by the partial-failure test below to inject a mid-pipeline embed
// failure without modifying production code.
func stubEmbedServer(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeEmbedResponse(t, w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &calls
}

// stubEmbedServerAlwaysFails returns a server that returns HTTP 400 on
// every request — a non-retryable status per library/http.go's retry
// policy. Using 400 (not 500) keeps the test fast: a 500 would burn
// ~30s of exponential backoff before the retry budget exhausts.
func stubEmbedServerAlwaysFails(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "synthetic embed failure", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &calls
}

func writeEmbedResponse(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	var req struct {
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	embedding := make([]float32, 2560)
	for i := range embedding {
		embedding[i] = float32(i%5) * 0.1
	}
	data := make([]map[string]any, len(req.Input))
	for i := range data {
		data[i] = map[string]any{"embedding": embedding}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// seedPhotoForIndex inserts a photos + exif + descriptions row triple so
// LoadPhoto returns a Photo with non-empty Description / Metadata text.
// query_generations is optional — pass non-nil queries to populate it.
func seedPhotoForIndex(t *testing.T, db *sql.DB, name string, queries []string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO photos (id, name) VALUES ($1, $1)`, name); err != nil {
		t.Fatalf("photos %s: %v", name, err)
	}
	if _, err := db.Exec(`
		INSERT INTO exif (photo_id, camera_make, camera_model, lens_model,
		                  date_taken, focal_length_mm, f_number,
		                  exposure_time_seconds, iso)
		VALUES ($1, 'FUJIFILM', 'X100VI', 'FUJINON 23mm',
		        '2024-04-21T16:27:54', 23.0, 5.6, $2, 500)
	`, name, 1.0/250); err != nil {
		t.Fatalf("exif %s: %v", name, err)
	}
	if _, err := db.Exec(`
		INSERT INTO descriptions (photo_id, subject, setting, full_description)
		VALUES ($1, 'a man in a gray shirt', 'indoor cafe',
		        'A quiet candid scene with warm light.')
	`, name); err != nil {
		t.Fatalf("descriptions %s: %v", name, err)
	}
	if len(queries) > 0 {
		qJSON, _ := json.Marshal(queries)
		if _, err := db.Exec(`
			INSERT INTO query_generations
				(photo_id, schema_version, model, prompt_hash, queries)
			VALUES ($1, 2, 'test-model', 'h', $2::jsonb)
		`, name, qJSON); err != nil {
			t.Fatalf("query_generations %s: %v", name, err)
		}
	}
}

// countStoreRows is a small helper used across the resumability tests to
// assert row deltas across each of the three v12 vector stores.
func countStoreRows(t *testing.T, db *sql.DB, table, photoID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE photo_id = $1", photoID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}
