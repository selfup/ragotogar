package library

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// seedExif inserts an exif row with the fields that feed exif.fts. The
// generated column populates automatically — caller doesn't pass it.
func seedExif(t *testing.T, db *sql.DB, photoID, cameraModel, lensModel string, year int, software string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO exif (photo_id, camera_make, camera_model, lens_model, date_taken_year, software)
		VALUES ($1, 'Fujifilm', $2, $3, $4, $5)
	`, photoID, cameraModel, lensModel, year, software); err != nil {
		t.Fatalf("seed exif %s: %v", photoID, err)
	}
}

func seedDescription(t *testing.T, db *sql.DB, photoID, subject, fullDesc string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO descriptions (photo_id, subject, full_description)
		VALUES ($1, $2, $3)
	`, photoID, subject, fullDesc); err != nil {
		t.Fatalf("seed description %s: %v", photoID, err)
	}
}

func TestSearchFTSExclusionsApplyAcrossOR(t *testing.T) {
	db := newTempDB(t)
	for _, photo := range []struct{ name, description string }{
		{"truck", strings.Repeat("car ", 64) + "beside a pickup truck"},
		{"suv", "A car beside an SUV."},
		{"metadata-truck", "A car."},
		{"car", "A car."},
		{"sedan", "A sedan."},
	} {
		id := seedPhoto(t, db, photo.name)
		seedDescription(t, db, id, photo.description, photo.description)
	}
	seedExif(t, db, "metadata-truck", "camera", "lens", 2024, "pickup truck")
	var parsed string
	if err := db.QueryRow(`SELECT websearch_to_tsquery('english', '(car OR sedan) -truck -suv')::text`).Scan(&parsed); err != nil {
		t.Fatal(err)
	}
	t.Logf("Postgres parses screenshot rewrite as: %s", parsed)
	s := NewSearcher(db)
	for _, query := range []string{
		"(car OR sedan) -truck -suv",
		"car OR sedan -truck -suv",
		"car -truck OR sedan -suv",
		`car OR sedan - "pickup truck" -suv`,
	} {
		for _, limits := range []struct {
			topK int
			rel  float64
		}{{0, 0}, {1, 0}, {0, 1}} {
			t.Run(fmt.Sprintf("%s/topK=%d/rel=%g", query, limits.topK, limits.rel), func(t *testing.T) {
				got, err := s.searchFTS(t.Context(), query, limits.topK, limits.rel)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) == 0 {
					t.Fatal("excluded matches displaced all eligible results")
				}
				for _, hit := range got {
					if hit.Name != "car" && hit.Name != "sedan" {
						t.Errorf("excluded photo returned: %s", hit.Name)
					}
				}
				if limits.topK == 0 && limits.rel == 0 && len(got) != 2 {
					t.Errorf("got %d results, want car and sedan", len(got))
				}
				if limits.topK == 1 && len(got) != 1 {
					t.Errorf("topK=1 returned %d results", len(got))
				}
			})
		}
	}
}

// TestSearchFTSFindsYearViaExifFTS: a query for "2024" should match a photo
// whose only "2024" token lives in exif.date_taken_year — the regression case
// the user demonstrated. Before v8 the FTS arm only saw descriptions.fts;
// this confirms the migration closed that gap.
func TestSearchFTSFindsYearViaExifFTS(t *testing.T) {
	db := newTempDB(t)
	ctx := context.Background()

	id := seedPhoto(t, db, "p_2024")
	seedExif(t, db, id, "X100VI", "Fujinon 23mm f/2", 2024, "Lightroom")
	seedDescription(t, db, id, "warm bedroom", "A warm bedroom with paisley duvet.")

	// Decoy: same description, different year. Should NOT match the 2024 query.
	otherID := seedPhoto(t, db, "p_2023")
	seedExif(t, db, otherID, "X100VI", "Fujinon 23mm f/2", 2023, "Lightroom")
	seedDescription(t, db, otherID, "warm bedroom", "A warm bedroom with paisley duvet.")

	s := NewSearcher(db)
	results, err := s.searchFTS(ctx, "2024", 30, 0.0)
	if err != nil {
		t.Fatalf("searchFTS: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 match for '2024', got %d: %+v", len(results), results)
	}
	if results[0].Name != id {
		t.Errorf("expected match %s, got %s", id, results[0].Name)
	}
}

// TestSearchFTSFindsCameraModelViaExifFTS: query for camera model name —
// "X100VI" is a token only present in exif.camera_model.
func TestSearchFTSFindsCameraModelViaExifFTS(t *testing.T) {
	db := newTempDB(t)
	ctx := context.Background()

	a := seedPhoto(t, db, "p_x100vi")
	seedExif(t, db, a, "X100VI", "Fujinon 23mm", 2024, "")
	seedDescription(t, db, a, "scene", "A scene.")

	b := seedPhoto(t, db, "p_zfc")
	seedExif(t, db, b, "Z fc", "Z 28mm f/2.8", 2024, "")
	seedDescription(t, db, b, "scene", "A scene.")

	s := NewSearcher(db)
	results, err := s.searchFTS(ctx, "X100VI", 30, 0.0)
	if err != nil {
		t.Fatalf("searchFTS: %v", err)
	}
	if len(results) != 1 || results[0].Name != a {
		t.Errorf("expected only %s for X100VI, got %+v", a, results)
	}
}

// TestSearchFTSDescriptionMatchStillWorks: prose-only query ("paisley") that
// hits descriptions.fts but has no exif token. Confirms the OR didn't break
// the existing description-FTS path.
func TestSearchFTSDescriptionMatchStillWorks(t *testing.T) {
	db := newTempDB(t)
	ctx := context.Background()

	id := seedPhoto(t, db, "p_paisley")
	seedExif(t, db, id, "X100VI", "Fujinon 23mm", 2024, "")
	seedDescription(t, db, id, "warm bedroom", "A warm bedroom with paisley duvet and brass lamp.")

	other := seedPhoto(t, db, "p_no_paisley")
	seedExif(t, db, other, "X100VI", "Fujinon 23mm", 2024, "")
	seedDescription(t, db, other, "kitchen", "A kitchen with copper pots and slate counters.")

	s := NewSearcher(db)
	results, err := s.searchFTS(ctx, "paisley", 30, 0.0)
	if err != nil {
		t.Fatalf("searchFTS: %v", err)
	}
	if len(results) != 1 || results[0].Name != id {
		t.Errorf("expected only %s for 'paisley', got %+v", id, results)
	}
}

// TestSearchFTSCombinedHitDoesNotDoubleCount: when a query hits BOTH columns
// for the same photo (e.g. "X100VI bedroom" — X100VI in exif, bedroom in
// description), the row appears once. GREATEST takes the max ts_rank rather
// than summing.
func TestSearchFTSCombinedHitDoesNotDoubleCount(t *testing.T) {
	db := newTempDB(t)
	ctx := context.Background()

	id := seedPhoto(t, db, "p_combo")
	seedExif(t, db, id, "X100VI", "Fujinon 23mm", 2024, "")
	seedDescription(t, db, id, "bedroom", "A bedroom with a bed and a window.")

	s := NewSearcher(db)
	results, err := s.searchFTS(ctx, "X100VI bedroom", 30, 0.0)
	if err != nil {
		t.Fatalf("searchFTS: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 row (no duplicate), got %d: %+v", len(results), results)
	}
	if results[0].Name != id {
		t.Errorf("expected %s, got %s", id, results[0].Name)
	}
}

// TestSearchFTSMissReturnsEmpty: a query that hits neither tsvector returns
// no rows (not an error). Guards against regressions where a mistyped LEFT
// JOIN OR clause would return everything.
func TestSearchFTSMissReturnsEmpty(t *testing.T) {
	db := newTempDB(t)
	ctx := context.Background()

	id := seedPhoto(t, db, "p_miss")
	seedExif(t, db, id, "X100VI", "Fujinon 23mm", 2024, "")
	seedDescription(t, db, id, "bedroom", "A bedroom.")

	s := NewSearcher(db)
	results, err := s.searchFTS(ctx, "submarine", 30, 0.0)
	if err != nil {
		t.Fatalf("searchFTS: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 matches for unrelated query, got %+v", results)
	}
}
