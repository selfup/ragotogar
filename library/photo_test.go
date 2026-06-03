package library

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestLoadPhoto_MissingReturnsErrNoRows(t *testing.T) {
	db := newTempDB(t)

	_, err := LoadPhoto(db, "no-such-photo")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("LoadPhoto for missing name: got err = %v, want sql.ErrNoRows", err)
	}
}

// TestLoadPhoto_MinimalRowAllOtherTablesAbsent verifies the LEFT JOIN
// behavior — a photo with only the photos row (no exif / descriptions /
// classified / query_generations) loads cleanly with every optional field
// zero-valued and pointer fields nil.
func TestLoadPhoto_MinimalRowAllOtherTablesAbsent(t *testing.T) {
	db := newTempDB(t)
	if _, err := db.Exec(
		`INSERT INTO photos (id, name, file_basename) VALUES ($1, $1, $2)`,
		"bare_photo", "BARE.JPG",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	p, err := LoadPhoto(db, "bare_photo")
	if err != nil {
		t.Fatalf("LoadPhoto: %v", err)
	}
	if p.Name != "bare_photo" || p.FileBasename != "BARE.JPG" {
		t.Errorf("name/basename mismatch: %q / %q", p.Name, p.FileBasename)
	}
	// Every nullable scalar should be empty.
	for label, got := range map[string]string{
		"CameraMake":   p.CameraMake,
		"CameraModel":  p.CameraModel,
		"LensModel":    p.LensModel,
		"DateTaken":    p.DateTaken,
		"Subject":      p.Subject,
		"Setting":      p.Setting,
		"FullDesc":     p.FullDescription,
		"Mood":         p.Mood,
		"POVContainer": p.POVContainer,
		"Motion":       p.Motion,
	} {
		if got != "" {
			t.Errorf("%s = %q, want empty", label, got)
		}
	}
	// Pointer fields should be nil — compare each directly. Storing a
	// typed nil pointer in an `any` value creates a non-nil interface,
	// so a map[string]any sweep can't be used here.
	if p.FocalLengthMM != nil {
		t.Errorf("FocalLengthMM = %v, want nil", *p.FocalLengthMM)
	}
	if p.FNumber != nil {
		t.Errorf("FNumber = %v, want nil", *p.FNumber)
	}
	if p.ShutterSeconds != nil {
		t.Errorf("ShutterSeconds = %v, want nil", *p.ShutterSeconds)
	}
	if p.ISO != nil {
		t.Errorf("ISO = %v, want nil", *p.ISO)
	}
	if p.ExposureCompensation != nil {
		t.Errorf("ExposureCompensation = %v, want nil", *p.ExposureCompensation)
	}
	// Array fields should be nil/empty.
	if p.SubjectCategory != nil {
		t.Errorf("SubjectCategory should be nil, got %v", p.SubjectCategory)
	}
	if p.Framing != nil {
		t.Errorf("Framing should be nil, got %v", p.Framing)
	}
	if p.GeneratedQueries != nil {
		t.Errorf("GeneratedQueries should be nil, got %v", p.GeneratedQueries)
	}
}

// TestLoadPhoto_FullStack populates every joined table and verifies every
// field round-trips through LoadPhoto's scan/assign pipeline.
func TestLoadPhoto_FullStack(t *testing.T) {
	db := newTempDB(t)
	seedFullPhoto(t, db, "full_photo")

	p, err := LoadPhoto(db, "full_photo")
	if err != nil {
		t.Fatalf("LoadPhoto: %v", err)
	}

	// EXIF scalars
	if p.CameraMake != "FUJIFILM" || p.CameraModel != "X100VI" {
		t.Errorf("camera: %q / %q", p.CameraMake, p.CameraModel)
	}
	if p.LensModel != "FUJINON 23mm" {
		t.Errorf("lens model: %q", p.LensModel)
	}
	if p.DateTaken != "2024-04-21T16:27:54" {
		t.Errorf("date_taken: %q", p.DateTaken)
	}
	if p.Software != "X100VI 1.20" || p.Artist != "selfup" {
		t.Errorf("software/artist: %q / %q", p.Software, p.Artist)
	}

	// EXIF pointer fields
	if p.FocalLengthMM == nil || *p.FocalLengthMM != 23.0 {
		t.Errorf("focal: %v", p.FocalLengthMM)
	}
	if p.FNumber == nil || *p.FNumber != 5.6 {
		t.Errorf("fnumber: %v", p.FNumber)
	}
	if p.ShutterSeconds == nil {
		t.Fatalf("shutter nil")
	}
	if *p.ShutterSeconds < 1.0/250-1e-9 || *p.ShutterSeconds > 1.0/250+1e-9 {
		t.Errorf("shutter = %v, want ~%v", *p.ShutterSeconds, 1.0/250)
	}
	if p.ISO == nil || *p.ISO != 500 {
		t.Errorf("iso: %v", p.ISO)
	}
	if p.ExposureCompensation == nil || *p.ExposureCompensation != -0.67 {
		t.Errorf("ec: %v", p.ExposureCompensation)
	}

	// Description fields
	if p.Subject != "a man in a gray shirt" || p.Mood != "warm, contemplative" {
		t.Errorf("subject/mood: %q / %q", p.Subject, p.Mood)
	}
	if p.FullDescription != "A quiet scene." {
		t.Errorf("full_description: %q", p.FullDescription)
	}

	// Classified scalars + arrays
	if p.POVContainer != "ground" || p.SceneIndoorOutdoor != "indoor" {
		t.Errorf("classified scalars: %q / %q", p.POVContainer, p.SceneIndoorOutdoor)
	}
	wantCategory := []string{"portrait", "candid"}
	if len(p.SubjectCategory) != len(wantCategory) {
		t.Errorf("subject_category len: %d, want %d", len(p.SubjectCategory), len(wantCategory))
	} else {
		for i, v := range wantCategory {
			if p.SubjectCategory[i] != v {
				t.Errorf("subject_category[%d] = %q, want %q", i, p.SubjectCategory[i], v)
			}
		}
	}
	wantFraming := []string{"rule_of_thirds"}
	if len(p.Framing) != len(wantFraming) || p.Framing[0] != wantFraming[0] {
		t.Errorf("framing = %v, want %v", p.Framing, wantFraming)
	}

	// query_generations → GeneratedQueries
	if len(p.GeneratedQueries) != 2 {
		t.Fatalf("GeneratedQueries len = %d, want 2: %v", len(p.GeneratedQueries), p.GeneratedQueries)
	}
	if p.GeneratedQueries[0] != "warm afternoon at a cafe" {
		t.Errorf("queries[0] = %q", p.GeneratedQueries[0])
	}
}

// TestLoadPhoto_EmptyQueriesJSONBLeavesGeneratedQueriesNil guards the
// documented semantics: an empty JSONB array `[]` is treated as "no
// queries" — GeneratedQueries stays nil, not []string{}.
func TestLoadPhoto_EmptyQueriesJSONBLeavesGeneratedQueriesNil(t *testing.T) {
	db := newTempDB(t)
	if _, err := db.Exec(
		`INSERT INTO photos (id, name) VALUES ($1, $1)`, "empty_q",
	); err != nil {
		t.Fatalf("seed photo: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO query_generations (photo_id, schema_version, model, prompt_hash, queries)
		VALUES ($1, 2, 'test-model', 'h', '[]'::jsonb)
	`, "empty_q"); err != nil {
		t.Fatalf("seed query_generations: %v", err)
	}

	p, err := LoadPhoto(db, "empty_q")
	if err != nil {
		t.Fatalf("LoadPhoto: %v", err)
	}
	if p.GeneratedQueries != nil {
		t.Errorf("empty JSONB array should leave GeneratedQueries nil, got %v", p.GeneratedQueries)
	}
}

// TestLoadPhoto_MalformedQueriesJSONReturnsError verifies the JSONB decode
// path surfaces an error instead of silently swallowing it. Pg accepts any
// valid JSON in JSONB, so writing a JSON object (not array) goes through
// pg fine but fails json.Unmarshal into []string. That's the canary.
func TestLoadPhoto_MalformedQueriesJSONReturnsError(t *testing.T) {
	db := newTempDB(t)
	if _, err := db.Exec(
		`INSERT INTO photos (id, name) VALUES ($1, $1)`, "bad_q",
	); err != nil {
		t.Fatalf("seed photo: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO query_generations (photo_id, schema_version, model, prompt_hash, queries)
		VALUES ($1, 2, 'm', 'h', '{"not":"an array"}'::jsonb)
	`, "bad_q"); err != nil {
		t.Fatalf("seed query_generations: %v", err)
	}

	_, err := LoadPhoto(db, "bad_q")
	if err == nil {
		t.Fatal("LoadPhoto should error on non-array queries JSONB")
	}
	if !strings.Contains(err.Error(), "decode query_generations.queries") {
		t.Errorf("error message should name the field, got: %v", err)
	}
}


func TestDateTakenToExifString(t *testing.T) {
	tests := []struct{ iso, want string }{
		{"", ""},
		{"2024-04-21T16:27:54", "2024:04:21 16:27:54"},
		{"2024-04-21", "2024:04:21"},
	}
	for _, tc := range tests {
		got := dateTakenToExifString(tc.iso)
		if got != tc.want {
			t.Errorf("dateTakenToExifString(%q) = %q, want %q", tc.iso, got, tc.want)
		}
	}
}

// seedFullPhoto inserts a fully-populated photo across photos, exif,
// descriptions, classified, and query_generations. Mirrors the production
// shape: an X100VI photo with full EXIF, prose description fields, and
// classifier verdicts. Used by the full-stack LoadPhoto test.
func seedFullPhoto(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO photos (id, name, file_basename) VALUES ($1, $1, $2)`,
		name, name+".JPG",
	); err != nil {
		t.Fatalf("photos: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO exif (
			photo_id, camera_make, camera_model, lens_model, lens_info,
			date_taken, date_taken_year, date_taken_month,
			focal_length_mm, focal_length_35mm, f_number, exposure_time_seconds,
			iso, exposure_compensation, exposure_mode, white_balance, flash,
			software, artist
		) VALUES ($1, 'FUJIFILM', 'X100VI', 'FUJINON 23mm', 'FUJINON 23mm f/2',
		          '2024-04-21T16:27:54', 2024, 4,
		          23.0, 35.0, 5.6, $2,
		          500, -0.67, 'Auto', 'Auto', 'No Flash',
		          'X100VI 1.20', 'selfup')
	`, name, 1.0/250); err != nil {
		t.Fatalf("exif: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO descriptions
			(photo_id, subject, setting, light, colors, composition,
			 vantage, ground_truth, condition, mood, full_description)
		VALUES ($1, 'a man in a gray shirt', 'indoor cafe',
		        'natural daylight', 'muted greens', 'shallow depth of field',
		        'eye level', 'a man', 'pristine', 'warm, contemplative',
		        'A quiet scene.')
	`, name); err != nil {
		t.Fatalf("descriptions: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO classified
			(photo_id, pov_container, pov_altitude, pov_angle,
			 subject_altitude, subject_category, subject_distance, subject_count,
			 scene_time_of_day, scene_indoor_outdoor, framing, motion, color_palette,
			 classifier_model)
		VALUES ($1, 'ground', 'eye_level', 'frontal',
		        'on_ground', $2, 'medium', 'single',
		        'afternoon', 'indoor', $3, 'static', 'muted',
		        'test-classifier')
	`, name, "{portrait,candid}", "{rule_of_thirds}"); err != nil {
		t.Fatalf("classified: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO query_generations (photo_id, schema_version, model, prompt_hash, queries)
		VALUES ($1, 2, 'test-model', 'aabbccdd', $2::jsonb)
	`, name, `["warm afternoon at a cafe","quiet candid portrait"]`); err != nil {
		t.Fatalf("query_generations: %v", err)
	}
}
