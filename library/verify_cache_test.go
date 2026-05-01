package library

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestCanonicalQueryLowercasesAndTrims(t *testing.T) {
	cases := map[string]string{
		"  Indoor Scenes  ":     "indoor scenes",
		"FROM A PLANE":          "from a plane",
		"warm light bedroom":    "warm light bedroom",
		"\tred truck\n":         "red truck",
		"":                      "",
		"   ":                   "",
	}
	for in, want := range cases {
		got := CanonicalQuery(in)
		if got != want {
			t.Errorf("CanonicalQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestVerifyCacheRoundtrip writes a verdict, looks it up, confirms the
// returned map matches. Sanity check that the SQL hasn't drifted from the
// schema.
func TestVerifyCacheRoundtrip(t *testing.T) {
	db := newTempDB(t)
	ctx := context.Background()

	id := seedPhoto(t, db, "photo_a")
	if err := writeVerifyCache(ctx, db, "warm light", id, "ministral-3-3b", true); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := lookupVerifyCache(ctx, db, "warm light", []string{id}, "ministral-3-3b")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if v, ok := got[id]; !ok || !v {
		t.Errorf("expected cached verdict true for %s, got map=%v", id, got)
	}
}

// TestVerifyCacheStaleRowFiltered: when inference.described_at advances past
// verified_at, the cache row must be invisible to the lookup. This is the
// invalidation guarantee — re-describe a photo and old verdicts stop counting
// without explicit teardown.
func TestVerifyCacheStaleRowFiltered(t *testing.T) {
	db := newTempDB(t)
	ctx := context.Background()

	id := seedPhoto(t, db, "photo_b")
	if err := writeVerifyCache(ctx, db, "indoor", id, "test-model", true); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Bump described_at to "now() + 1 minute" so it's strictly greater than
	// verified_at regardless of clock skew between the two writes.
	if _, err := db.Exec(`UPDATE inference SET described_at = now() + interval '1 minute' WHERE photo_id = $1`, id); err != nil {
		t.Fatalf("advance described_at: %v", err)
	}

	got, err := lookupVerifyCache(ctx, db, "indoor", []string{id}, "test-model")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if _, ok := got[id]; ok {
		t.Errorf("stale cache row should have been filtered out, got map=%v", got)
	}
}

// TestVerifyCacheModelSwapIsolated: caching a verdict under model A and then
// looking up under model B must miss — verify_model is part of the PK so
// SEARCH_MODEL swaps don't cross-contaminate.
func TestVerifyCacheModelSwapIsolated(t *testing.T) {
	db := newTempDB(t)
	ctx := context.Background()

	id := seedPhoto(t, db, "photo_c")
	if err := writeVerifyCache(ctx, db, "from a plane", id, "ministral-3-3b", true); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := lookupVerifyCache(ctx, db, "from a plane", []string{id}, "devstral-small-2-2512")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if _, ok := got[id]; ok {
		t.Errorf("different verify_model should miss, got map=%v", got)
	}

	// Sanity: the original model still hits.
	hit, err := lookupVerifyCache(ctx, db, "from a plane", []string{id}, "ministral-3-3b")
	if err != nil {
		t.Fatalf("lookup hit-arm: %v", err)
	}
	if v, ok := hit[id]; !ok || !v {
		t.Errorf("original model lookup missing, got map=%v", hit)
	}
}

// TestVerifyCacheConcurrentInsert: 8 goroutines hammering the same
// (query, photo_id, model) PK must not error — the ON CONFLICT clause is the
// load-bearing detail. Final state has exactly one row with the latest verdict.
func TestVerifyCacheConcurrentInsert(t *testing.T) {
	db := newTempDB(t)
	ctx := context.Background()

	id := seedPhoto(t, db, "photo_d")

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		// alternate true/false to make sure ON CONFLICT DO UPDATE actually
		// updates rather than silently keeping the original row.
		verdict := i%2 == 0
		go func() {
			defer wg.Done()
			if err := writeVerifyCache(ctx, db, "concurrent", id, "test-model", verdict); err != nil {
				t.Errorf("concurrent write %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	var rowCount int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM verify_cache
		WHERE query = 'concurrent' AND photo_id = $1 AND verify_model = 'test-model'
	`, id).Scan(&rowCount); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("expected 1 row after 8 concurrent UPSERTs, got %d", rowCount)
	}
}

// TestVerifyCacheLookupMultiplePhotos confirms the IN-list query returns one
// entry per matched photo, with cache-miss photos absent from the map (rather
// than mapped to a zero value the caller would mistake for a NO verdict).
func TestVerifyCacheLookupMultiplePhotos(t *testing.T) {
	db := newTempDB(t)
	ctx := context.Background()

	a := seedPhoto(t, db, "photo_e_yes")
	b := seedPhoto(t, db, "photo_e_no")
	c := seedPhoto(t, db, "photo_e_uncached")

	if err := writeVerifyCache(ctx, db, "q", a, "m", true); err != nil {
		t.Fatal(err)
	}
	if err := writeVerifyCache(ctx, db, "q", b, "m", false); err != nil {
		t.Fatal(err)
	}

	got, err := lookupVerifyCache(ctx, db, "q", []string{a, b, c}, "m")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if v, ok := got[a]; !ok || !v {
		t.Errorf("photo_a expected cached YES, got %v ok=%v", v, ok)
	}
	if v, ok := got[b]; !ok || v {
		t.Errorf("photo_b expected cached NO, got %v ok=%v", v, ok)
	}
	if _, ok := got[c]; ok {
		t.Errorf("photo_c was never cached, should be absent from map")
	}
}

// TestVerifyCacheUpdatesVerifiedAt: the second write to the same key must
// bump verified_at — important because the freshness predicate compares it
// against inference.described_at.
func TestVerifyCacheUpdatesVerifiedAt(t *testing.T) {
	db := newTempDB(t)
	ctx := context.Background()

	id := seedPhoto(t, db, "photo_f")
	if err := writeVerifyCache(ctx, db, "q", id, "m", true); err != nil {
		t.Fatal(err)
	}

	var firstAt time.Time
	if err := db.QueryRow(
		`SELECT verified_at FROM verify_cache WHERE query='q' AND photo_id=$1 AND verify_model='m'`, id,
	).Scan(&firstAt); err != nil {
		t.Fatalf("read first verified_at: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	if err := writeVerifyCache(ctx, db, "q", id, "m", false); err != nil {
		t.Fatal(err)
	}

	var secondAt time.Time
	var verdict bool
	if err := db.QueryRow(
		`SELECT verified_at, verdict FROM verify_cache WHERE query='q' AND photo_id=$1 AND verify_model='m'`, id,
	).Scan(&secondAt, &verdict); err != nil {
		t.Fatalf("read second: %v", err)
	}
	if !secondAt.After(firstAt) {
		t.Errorf("verified_at should advance: first=%v second=%v", firstAt, secondAt)
	}
	if verdict != false {
		t.Errorf("verdict should have flipped to false on UPSERT, got %v", verdict)
	}
}

// TestVerifyStatsHitRate covers the formatter the UI / CLI rely on so a
// changed denominator doesn't silently produce NaN or division-by-zero output.
func TestVerifyStatsHitRate(t *testing.T) {
	cases := []struct {
		name       string
		stats      VerifyStats
		wantRate   float64
	}{
		{"empty", VerifyStats{Total: 0, Cached: 0, LLM: 0}, 0},
		{"all cached", VerifyStats{Total: 10, Cached: 10, LLM: 0}, 1.0},
		{"all llm", VerifyStats{Total: 10, Cached: 0, LLM: 10}, 0},
		{"mixed", VerifyStats{Total: 10, Cached: 6, LLM: 4}, 0.6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.stats.HitRate()
			if got != tc.wantRate {
				t.Errorf("HitRate() = %v, want %v", got, tc.wantRate)
			}
		})
	}
}
