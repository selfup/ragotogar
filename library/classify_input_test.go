package library

import "testing"

func TestLoadClassifyInputExcludesQueriesFromLegacyFallback(t *testing.T) {
	db := newTempDB(t)
	name := seedPhoto(t, db, "legacy")
	if _, err := db.Exec(`INSERT INTO descriptions (photo_id, full_description) VALUES ($1, $2)`,
		name, "A forest.\nQueries:\nzeppelins at sunset"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadClassifyInput(db, name)
	if err != nil {
		t.Fatal(err)
	}
	if got != "A forest." {
		t.Errorf("classifier fallback leaked generated queries: %q", got)
	}
}
