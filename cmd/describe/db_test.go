package main

import (
	"database/sql"
	"strings"
	"testing"

	"ragotogar/library/testdb"
)

// newTempDB is the cmd/describe convenience wrapper around testdb.New that
// routes schema application through the live initSchema path — same code
// path production uses on every cmd/describe startup. This makes
// TestOpenDBCreatesSchema below a true integration test of the migrate +
// schemaSQL pipeline, not a copy of the production DDL.
func newTempDB(t *testing.T) *sql.DB {
	t.Helper()
	return testdb.New(t, "describe", func(db *sql.DB) error {
		return initSchema(db)
	})
}

func TestOpenDBCreatesSchema(t *testing.T) {
	db := newTempDB(t)

	expected := []string{
		"schema_version", "photos", "exif", "descriptions",
		"thumbnails", "inference", "verify_cache",
		"photo_descriptions", "photo_metadata", "photo_queries", "query_generations",
	}
	for _, name := range expected {
		var found int
		err := db.QueryRow(
			"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name=$1", name,
		).Scan(&found)
		if err != nil {
			t.Fatalf("query for %s: %v", name, err)
		}
		if found != 1 {
			t.Errorf("table %s missing (count=%d)", name, found)
		}
	}

	expectedIdx := []string{
		"idx_photos_name", "idx_exif_camera", "idx_exif_make", "idx_exif_date",
		"idx_exif_year_month", "idx_exif_iso", "idx_exif_aperture",
		"idx_exif_focal", "idx_exif_artist",
		"idx_exif_fts",
		"idx_descriptions_fts",
		"idx_verify_cache_query",
		"idx_photo_descriptions_embedding", "idx_photo_descriptions_photo_id",
		"idx_photo_metadata_embedding", "idx_photo_metadata_photo_id",
		"idx_photo_queries_embedding", "idx_photo_queries_photo_id",
	}
	for _, name := range expectedIdx {
		var found int
		err := db.QueryRow(
			"SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND indexname=$1", name,
		).Scan(&found)
		if err != nil || found != 1 {
			t.Errorf("index %s missing (err=%v count=%d)", name, err, found)
		}
	}
}

// TestPhotosTableNoLegacyPathColumns guards against the slice-1 path columns
// reappearing in the schema.
func TestPhotosTableNoLegacyPathColumns(t *testing.T) {
	db := newTempDB(t)

	for _, col := range []string{"json_path", "md_path", "html_path", "jpg_path"} {
		var found int
		err := db.QueryRow(
			"SELECT COUNT(*) FROM information_schema.columns WHERE table_name='photos' AND column_name=$1", col,
		).Scan(&found)
		if err != nil {
			t.Fatalf("information_schema query: %v", err)
		}
		if found != 0 {
			t.Errorf("photos.%s should not exist (found=%d)", col, found)
		}
	}
}

func TestInsertPhotoRoundTrip(t *testing.T) {
	db := newTempDB(t)

	exif := exifData{
		FileName:             "DSCF0086.JPG",
		DateTimeOriginal:     "2024:04:21 16:27:54",
		Make:                 "FUJIFILM",
		Model:                "X100VI",
		LensModel:            "",
		FocalLength:          "23.0 mm",
		FNumber:              "5.6",
		ExposureTime:         "1/250",
		ISO:                  "500",
		ExposureCompensation: "-0.67",
		ExposureMode:         "Auto",
		WhiteBalance:         "Auto",
		Flash:                "No Flash",
		ImageWidth:           "7728",
		ImageHeight:          "5152",
		Software:             "X100VI",
	}
	fields := descriptionFields{
		Subject:     "a man in a gray shirt",
		Setting:     "indoor scene with trees visible",
		Light:       "natural daylight",
		Colors:      "muted greens",
		Composition: "shallow depth of field",
	}
	thumb := []byte{0xff, 0xd8, 0xff, 0xe0, 'F', 'a', 'k', 'e', 'J', 'P', 'G'}

	if err := insertPhoto(
		db, "test_photo", "/some/path/DSCF0086.JPG",
		exif, "Full description with trees and shadows.", fields,
		thumb, "qwen/qwen3-vl-8b", 1228, 11371,
	); err != nil {
		t.Fatalf("insertPhoto: %v", err)
	}

	var (
		name, fileBasename, filePath string
	)
	err := db.QueryRow(
		"SELECT name, file_path, file_basename FROM photos WHERE id = 'test_photo'",
	).Scan(&name, &filePath, &fileBasename)
	if err != nil {
		t.Fatalf("photos query: %v", err)
	}
	if name != "test_photo" || fileBasename != "DSCF0086.JPG" || filePath != "/some/path/DSCF0086.JPG" {
		t.Errorf("photos row mismatch: name=%s file_path=%s file_basename=%s",
			name, filePath, fileBasename)
	}

	var (
		make_, model     string
		focalMM, fNumber float64
		shutter          float64
		iso              int
		ec               float64
		dateTaken        string
		year, month      int
	)
	err = db.QueryRow(`
		SELECT camera_make, camera_model, focal_length_mm, f_number,
		       exposure_time_seconds, iso, exposure_compensation,
		       date_taken, date_taken_year, date_taken_month
		FROM exif WHERE photo_id = 'test_photo'
	`).Scan(&make_, &model, &focalMM, &fNumber, &shutter, &iso, &ec, &dateTaken, &year, &month)
	if err != nil {
		t.Fatalf("exif query: %v", err)
	}
	if make_ != "FUJIFILM" || model != "X100VI" {
		t.Errorf("camera mismatch: %s / %s", make_, model)
	}
	if focalMM != 23.0 || fNumber != 5.6 {
		t.Errorf("optics mismatch: focal=%v f=%v", focalMM, fNumber)
	}
	const eps = 1e-9
	if d := shutter - 1.0/250; d > eps || d < -eps {
		t.Errorf("shutter = %v, want ~%v", shutter, 1.0/250)
	}
	if iso != 500 || ec != -0.67 {
		t.Errorf("iso=%v ec=%v", iso, ec)
	}
	if dateTaken != "2024-04-21T16:27:54" || year != 2024 || month != 4 {
		t.Errorf("date mismatch: %s / %d-%d", dateTaken, year, month)
	}

	var subject, full string
	err = db.QueryRow(
		"SELECT subject, full_description FROM descriptions WHERE photo_id = 'test_photo'",
	).Scan(&subject, &full)
	if err != nil {
		t.Fatalf("descriptions: %v", err)
	}
	if subject != "a man in a gray shirt" {
		t.Errorf("subject mismatch: %s", subject)
	}
	if full != "Full description with trees and shadows." {
		t.Errorf("full_description mismatch: %s", full)
	}

	var got []byte
	var width int
	err = db.QueryRow(
		"SELECT bytes, width FROM thumbnails WHERE photo_id = 'test_photo'",
	).Scan(&got, &width)
	if err != nil {
		t.Fatalf("thumbnail: %v", err)
	}
	if string(got) != string(thumb) {
		t.Errorf("thumbnail bytes round-trip failed")
	}
	if width != 1024 {
		t.Errorf("width = %d, want 1024", width)
	}

	var modelName string
	var previewMs, inferenceMs int64
	err = db.QueryRow(
		"SELECT model, preview_ms, inference_ms FROM inference WHERE photo_id = 'test_photo'",
	).Scan(&modelName, &previewMs, &inferenceMs)
	if err != nil {
		t.Fatalf("inference: %v", err)
	}
	if modelName != "qwen/qwen3-vl-8b" || previewMs != 1228 || inferenceMs != 11371 {
		t.Errorf("inference mismatch: model=%s preview=%d inference=%d",
			modelName, previewMs, inferenceMs)
	}
}

// TestInsertPhotoMoodAndQueries covers the v13 describer's new write paths:
// descriptions.mood column populates from fields.Mood, and a non-empty
// fields.Queries triggers a query_generations row with JSONB queries +
// the package's static prompt_hash. Verifies absence semantics too: an
// empty fields.Queries skips the query_generations write per spec ("do
// not write empty/broken JSON silently").
func TestInsertPhotoMoodAndQueries(t *testing.T) {
	db := newTempDB(t)

	exif := exifData{
		FileName: "DSCF0091.JPG",
		Make:     "FUJIFILM",
		Model:    "X100VI",
	}
	fields := descriptionFields{
		Subject: "two friends at a wooden table",
		Mood:    "warm, nostalgic, intimate",
		Queries: []string{
			"warm afternoon at a corner cafe",
			"two friends sharing coffee",
			"intimate candid portrait",
		},
	}

	if err := insertPhoto(
		db, "moodphoto", "/p/x.JPG",
		exif, "Full description.", fields,
		[]byte{0xff}, "qwen/qwen3-vl-8b", 100, 200,
	); err != nil {
		t.Fatalf("insertPhoto: %v", err)
	}

	// descriptions.mood populates
	var mood string
	if err := db.QueryRow(
		"SELECT mood FROM descriptions WHERE photo_id = 'moodphoto'",
	).Scan(&mood); err != nil {
		t.Fatalf("read mood: %v", err)
	}
	if mood != "warm, nostalgic, intimate" {
		t.Errorf("mood roundtrip: %q", mood)
	}

	// query_generations row landed with JSONB array, prompt_hash, model
	var (
		schemaVersion int
		model         string
		promptHash    string
		queriesJSON   []byte
	)
	if err := db.QueryRow(`
		SELECT schema_version, model, prompt_hash, queries::text
		FROM query_generations WHERE photo_id = 'moodphoto'
	`).Scan(&schemaVersion, &model, &promptHash, &queriesJSON); err != nil {
		t.Fatalf("read query_generations: %v", err)
	}
	if schemaVersion != queryGenerationsSchemaVersion {
		t.Errorf("schema_version = %d, want %d", schemaVersion, queryGenerationsSchemaVersion)
	}
	if model != "qwen/qwen3-vl-8b" {
		t.Errorf("model = %q", model)
	}
	if promptHash != describePromptHash {
		t.Errorf("prompt_hash = %q, want %q (the package's computed hash)", promptHash, describePromptHash)
	}
	if !strings.Contains(string(queriesJSON), "warm afternoon at a corner cafe") ||
		!strings.Contains(string(queriesJSON), "intimate candid portrait") {
		t.Errorf("queries JSON missing expected phrasings: %s", queriesJSON)
	}
}

// TestInsertPhotoEmptyQueriesSkipsRow guards the spec rule "do not write
// empty/broken JSON silently" — when the model omits the Queries section
// entirely, the query_generations row stays absent.
func TestInsertPhotoEmptyQueriesSkipsRow(t *testing.T) {
	db := newTempDB(t)

	if err := insertPhoto(
		db, "no_queries", "/p/x.JPG", exifData{}, "desc",
		descriptionFields{Subject: "x"}, []byte{0xff}, "m", 1, 2,
	); err != nil {
		t.Fatalf("insertPhoto: %v", err)
	}

	var found int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM query_generations WHERE photo_id = 'no_queries'",
	).Scan(&found); err != nil {
		t.Fatalf("count: %v", err)
	}
	if found != 0 {
		t.Errorf("expected 0 query_generations rows for photo with empty Queries, got %d", found)
	}
}

func TestInsertPhotoIdempotent(t *testing.T) {
	db := newTempDB(t)

	exif := exifData{Make: "FUJIFILM", Model: "X100VI", DateTimeOriginal: "2024:04:21 16:27:54"}
	fields := descriptionFields{Subject: "x"}

	for i := 0; i < 3; i++ {
		if err := insertPhoto(db, "test", "/p", exif, "desc", fields, []byte{0xff}, "m", 1, 2); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	for _, table := range []string{"photos", "exif", "descriptions", "thumbnails", "inference"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("%s count = %d after 3 inserts, want 1", table, n)
		}
	}
}

func TestCascadeDelete(t *testing.T) {
	db := newTempDB(t)

	exif := exifData{Make: "F", Model: "X"}
	if err := insertPhoto(db, "p", "/p", exif, "d", descriptionFields{Subject: "trees"},
		[]byte{0xff}, "m", 1, 2); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if _, err := db.Exec("DELETE FROM photos WHERE id = 'p'"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, table := range []string{"exif", "descriptions", "thumbnails", "inference"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s count = %d after photos delete, want 0 (FK cascade)", table, n)
		}
	}
}

func TestFTSPorterStemming(t *testing.T) {
	db := newTempDB(t)

	exif := exifData{Make: "F"}
	fields := descriptionFields{Subject: "a forest with many trees"}
	if err := insertPhoto(db, "p", "/p", exif, "trees and shadows everywhere", fields,
		[]byte{0xff}, "m", 1, 2); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Postgres English stemmer should match "tree" → "trees"
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM descriptions WHERE fts @@ plainto_tsquery('english', $1)", "tree",
	).Scan(&n); err != nil {
		t.Fatalf("fts query: %v", err)
	}
	if n != 1 {
		t.Errorf("fts MATCH 'tree' should hit 'trees', got %d rows", n)
	}
}

func TestListExistingNames(t *testing.T) {
	db := newTempDB(t)

	exif := exifData{Make: "F"}
	fields := descriptionFields{Subject: "x"}
	for _, name := range []string{"a", "b", "c"} {
		if err := insertPhoto(db, name, "/p", exif, "d", fields, []byte{0xff}, "m", 1, 2); err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
	}

	got, err := listExistingNames(db)
	if err != nil {
		t.Fatalf("listExistingNames: %v", err)
	}
	for _, n := range []string{"a", "b", "c"} {
		if !got[n] {
			t.Errorf("missing name %q", n)
		}
	}
	if len(got) != 3 {
		t.Errorf("got %d names, want 3", len(got))
	}
}
