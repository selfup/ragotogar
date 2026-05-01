package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// adminDSN is the DSN we use to CREATE/DROP transient test databases.
// Connects to the maintenance database (`postgres`) so we can issue DDL
// against the cluster itself.
func adminDSN(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("TEST_LIBRARY_DSN"); v != "" {
		return v
	}
	if v := os.Getenv("LIBRARY_DSN"); v != "" {
		// strip the dbname; replace with `postgres`
		return rewriteDBName(v, "postgres")
	}
	return "postgres:///postgres"
}

func rewriteDBName(dsn, newDB string) string {
	// crude but adequate for the two DSN shapes we use:
	//   postgres:///dbname            (path-style)
	//   postgres://host:port/dbname   (URL-style)
	// strip everything after the last "/" if it isn't already empty
	idx := strings.LastIndex(dsn, "/")
	if idx < 0 || idx == len(dsn)-1 {
		return dsn + newDB
	}
	return dsn[:idx+1] + newDB
}

// newTempDB creates a uniquely-named Postgres database, applies the schema,
// and returns an open connection plus a cleanup function. Skips the test
// (rather than failing) when no Postgres is reachable so unit tests don't
// brick on machines without ./scripts/bootstrap.sh having run.
func newTempDB(t *testing.T) *sql.DB {
	t.Helper()
	admin, err := sql.Open("pgx", adminDSN(t))
	if err != nil {
		t.Skipf("cannot reach Postgres for tests: %v (run ./scripts/bootstrap.sh)", err)
	}
	if err := admin.Ping(); err != nil {
		admin.Close()
		t.Skipf("cannot reach Postgres for tests: %v (run ./scripts/bootstrap.sh)", err)
	}
	defer admin.Close()

	rnd := make([]byte, 6)
	rand.Read(rnd)
	name := "ragotogar_test_" + hex.EncodeToString(rnd)
	if _, err := admin.Exec(fmt.Sprintf("CREATE DATABASE %s", name)); err != nil {
		t.Fatalf("create test db: %v", err)
	}

	dsn := rewriteDBName(adminDSN(t), name)
	db, err := openDB(dsn)
	if err != nil {
		// best-effort cleanup
		admin2, _ := sql.Open("pgx", adminDSN(t))
		if admin2 != nil {
			admin2.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", name))
			admin2.Close()
		}
		t.Fatalf("open test db: %v", err)
	}

	t.Cleanup(func() {
		db.Close()
		// ensure the test DB is droppable: kill any lingering backends
		admin2, err := sql.Open("pgx", adminDSN(t))
		if err != nil {
			return
		}
		defer admin2.Close()
		admin2.Exec(fmt.Sprintf(
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s'", name,
		))
		admin2.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", name))
	})

	return db
}

func TestOpenDBCreatesSchema(t *testing.T) {
	db := newTempDB(t)

	expected := []string{
		"schema_version", "photos", "exif", "descriptions",
		"thumbnails", "inference", "chunks", "verify_cache",
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
		"idx_descriptions_fts", "idx_chunks_embedding",
		"idx_verify_cache_query",
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
