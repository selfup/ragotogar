package main

import (
	"path/filepath"
	"testing"
)

// TestOpenDBCreatesSchema verifies a fresh DB ends up with all expected
// tables, indexes, and triggers — the slice-1 schema additions land here too
// (thumbnails, inference) since cmd/describe is the schema authority.
func TestOpenDBCreatesSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "library.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	expected := []string{
		"schema_version", "photos", "exif", "descriptions",
		"descriptions_fts", "thumbnails", "inference",
	}
	for _, name := range expected {
		var found int
		err := db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name,
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
	}
	for _, name := range expectedIdx {
		var found int
		err := db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", name,
		).Scan(&found)
		if err != nil || found != 1 {
			t.Errorf("index %s missing (err=%v count=%d)", name, err, found)
		}
	}

	for _, name := range []string{"descriptions_ai", "descriptions_ad", "descriptions_au"} {
		var found int
		err := db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?", name,
		).Scan(&found)
		if err != nil || found != 1 {
			t.Errorf("trigger %s missing (err=%v count=%d)", name, err, found)
		}
	}
}

// TestPhotosTableNoLegacyPathColumns verifies the post-cutover photos table
// — no json_path / md_path / html_path / jpg_path. These were dropped from
// the schema; if they reappear, sql_sync's old shape leaked back in.
func TestPhotosTableNoLegacyPathColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "library.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	for _, col := range legacyPathCols {
		var found int
		err := db.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info('photos') WHERE name=?", col,
		).Scan(&found)
		if err != nil {
			t.Fatalf("pragma_table_info: %v", err)
		}
		if found != 0 {
			t.Errorf("photos.%s should not exist (found=%d)", col, found)
		}
	}
}

// TestDropLegacyPathColumns simulates an old DB shape and verifies the
// migration drops the columns idempotently.
func TestDropLegacyPathColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "library.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	// Re-add columns to simulate the slice-1 shape, then re-run init.
	for _, col := range legacyPathCols {
		if _, err := db.Exec("ALTER TABLE photos ADD COLUMN " + col + " TEXT"); err != nil {
			t.Fatalf("re-add %s: %v", col, err)
		}
	}
	if err := initSchema(db); err != nil {
		t.Fatalf("re-init: %v", err)
	}
	for _, col := range legacyPathCols {
		var found int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info('photos') WHERE name=?", col,
		).Scan(&found); err != nil {
			t.Fatalf("pragma: %v", err)
		}
		if found != 0 {
			t.Errorf("legacy column %s not dropped", col)
		}
	}
}

func TestInsertPhotoRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "library.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

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
		name, fileBasename string
		filePath           string
	)
	err = db.QueryRow(
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

func TestInsertPhotoIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "library.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

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
	dbPath := filepath.Join(t.TempDir(), "library.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

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
	dbPath := filepath.Join(t.TempDir(), "library.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	exif := exifData{Make: "F"}
	fields := descriptionFields{Subject: "a forest with many trees"}
	if err := insertPhoto(db, "p", "/p", exif, "trees and shadows everywhere", fields,
		[]byte{0xff}, "m", 1, 2); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// porter should stem "tree" → match "trees"
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM descriptions_fts WHERE descriptions_fts MATCH ?", "tree",
	).Scan(&n); err != nil {
		t.Fatalf("fts query: %v", err)
	}
	if n != 1 {
		t.Errorf("fts MATCH 'tree' should hit 'trees', got %d rows", n)
	}
}

func TestListExistingNames(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "library.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

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
