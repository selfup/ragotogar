package main

import (
	"database/sql"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const minimalSchema = `
CREATE TABLE photos (
    id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE,
    file_path TEXT, file_basename TEXT
);
CREATE TABLE exif (
    photo_id TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    camera_make TEXT, camera_model TEXT, lens_model TEXT, lens_info TEXT,
    date_taken TEXT, focal_length_mm REAL, focal_length_35mm REAL,
    f_number REAL, exposure_time_seconds REAL, iso INTEGER,
    exposure_compensation REAL, exposure_mode TEXT, metering_mode TEXT,
    white_balance TEXT, flash TEXT, image_width INTEGER, image_height INTEGER,
    gps_latitude REAL, gps_longitude REAL, artist TEXT, software TEXT
);
CREATE TABLE descriptions (
    photo_id TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    subject TEXT, setting TEXT, light TEXT, colors TEXT, composition TEXT,
    full_description TEXT
);
CREATE TABLE thumbnails (
    photo_id TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    bytes BLOB NOT NULL, width INTEGER NOT NULL DEFAULT 1024
);
CREATE TABLE inference (
    photo_id TEXT PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
    raw_response TEXT, model TEXT, preview_ms INTEGER, inference_ms INTEGER
);
`

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(minimalSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func seedPhoto(t *testing.T, db *sql.DB, name string, thumb []byte) {
	t.Helper()
	if _, err := db.Exec(
		"INSERT INTO photos (id, name, file_path, file_basename) VALUES (?, ?, ?, ?)",
		name, name, "/some/path/"+name+".jpg", name+".jpg",
	); err != nil {
		t.Fatalf("photos: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO exif (photo_id, camera_make, camera_model, date_taken,
		                  focal_length_mm, f_number, exposure_time_seconds, iso)
		VALUES (?, 'FUJIFILM', 'X100VI', '2024-04-21T16:27:54', 23.0, 5.6, ?, 500)
	`, name, 1.0/250); err != nil {
		t.Fatalf("exif: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO descriptions
		    (photo_id, subject, setting, light, colors, composition, full_description)
		VALUES (?, 'a man in a gray shirt', 'indoor scene',
		        'natural daylight', 'muted greens', 'shallow DOF',
		        'Full description of the scene.')
	`, name); err != nil {
		t.Fatalf("descriptions: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO thumbnails (photo_id, bytes, width) VALUES (?, ?, 1024)",
		name, thumb,
	); err != nil {
		t.Fatalf("thumbnails: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO inference (photo_id, model, preview_ms, inference_ms) VALUES (?, 'qwen/qwen3-vl-8b', 666, 10394)",
		name,
	); err != nil {
		t.Fatalf("inference: %v", err)
	}
}

func TestServePhotoHTMLRendersAllSections(t *testing.T) {
	db := newTestDB(t)
	seedPhoto(t, db, "test_photo", []byte{0xff, 0xd8, 0xff, 0xe0})

	tmpl := template.Must(template.New("photo").Funcs(templateFuncMap()).Parse(photoHTML))

	req := httptest.NewRequest(http.MethodGet, "/photos/test_photo", nil)
	rr := httptest.NewRecorder()
	servePhotoHTML(rr, req, db, tmpl, "test_photo")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %s", got)
	}
	body := rr.Body.String()
	for _, expect := range []string{
		// cashier section structure
		`class="hero"`,
		`class="dual-pillars"`,
		`class="built photo-meta"`,
		`class="section-marker"`,
		`Photograph Analysis`,
		`href="/styles.css"`,
		// content from direct SQL pulls
		"test_photo", // name (in title, h1, photo-meta)
		"FUJIFILM",   // camera make
		"X100VI",     // camera model
		"21 April 2024",         // humanDate output
		"23.0 mm",               // focal
		"f/5.6",                 // aperture
		"1/250",                 // shutter (shutterFraction func)
		"a man in a gray shirt", // subject pillar
		"indoor scene",          // setting pillar
		`/photos/test_photo.jpg`, // image src + tagline link
		// inference timing prose
		"Preview generated in 666ms",
		"10.394s", // msToSeconds
		"qwen/qwen3-vl-8b",
	} {
		if !strings.Contains(body, expect) {
			t.Errorf("rendered HTML missing %q", expect)
		}
	}

	// full_description is no longer rendered — parsed fields cover the content.
	// Guard against the duplicated rendering coming back.
	if strings.Contains(body, "Full description of the scene.") {
		t.Errorf("rendered HTML should NOT contain raw full_description (redundant with parsed fields)")
	}

	// Synthesized cashier "close" section was deliberately not replicated.
	if strings.Contains(body, `class="close"`) {
		t.Errorf("rendered HTML should NOT include the synthesized close section")
	}
}

func TestServePhotoHTML404OnUnknown(t *testing.T) {
	db := newTestDB(t)
	tmpl := template.Must(template.New("photo").Funcs(templateFuncMap()).Parse(photoHTML))

	rr := httptest.NewRecorder()
	servePhotoHTML(rr, httptest.NewRequest("GET", "/photos/nope", nil), db, tmpl, "nope")
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing photo, got %d", rr.Code)
	}
}

func TestServePhotoJPGStreamsBlob(t *testing.T) {
	db := newTestDB(t)
	thumb := []byte{0xff, 0xd8, 0xff, 0xe0, 'F', 'A', 'K', 'E', 'J', 'P', 'G'}
	seedPhoto(t, db, "p1", thumb)

	rr := httptest.NewRecorder()
	servePhotoJPG(rr, httptest.NewRequest("GET", "/photos/p1.jpg", nil), db, "p1")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if rr.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("Content-Type = %s, want image/jpeg", rr.Header().Get("Content-Type"))
	}
	got, _ := io.ReadAll(rr.Body)
	if string(got) != string(thumb) {
		t.Errorf("BLOB bytes mismatch")
	}
}

func TestServePhotoJPG404OnUnknown(t *testing.T) {
	db := newTestDB(t)
	rr := httptest.NewRecorder()
	servePhotoJPG(rr, httptest.NewRequest("GET", "/photos/nope.jpg", nil), db, "nope")
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestServePhotoRejectsPathTraversal(t *testing.T) {
	db := newTestDB(t)
	tmpl := template.Must(template.New("photo").Funcs(templateFuncMap()).Parse(photoHTML))

	rr := httptest.NewRecorder()
	servePhotoHTML(rr, httptest.NewRequest("GET", "/photos/../etc/passwd", nil), db, tmpl, "../etc/passwd")
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for path with /, got %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	servePhotoJPG(rr, httptest.NewRequest("GET", "/photos/x/y.jpg", nil), db, "x/y")
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for path with /, got %d", rr.Code)
	}
}

func TestPhotoExists(t *testing.T) {
	db := newTestDB(t)
	seedPhoto(t, db, "exists", []byte{0xff})

	if !photoExists(db, "exists") {
		t.Errorf("photoExists returned false for known name")
	}
	if photoExists(db, "missing") {
		t.Errorf("photoExists returned true for missing name")
	}
}
