package library

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lib/pq"
)

// stubFilterLLM returns an httptest server whose /chat/completions response
// embeds `dropIDsJSON` as the content. The schema response shape is
// `{"choices":[{"message":{"content":"..."}}]}` where the inner content is
// itself JSON like `{"drop_ids":["p1"]}`.
//
// The fact that the LLM call is HTTP (not in-process) is what makes this
// useful — it exercises the full library/http.go retry + parsing layer the
// same way production does, but with deterministic, instant responses.
func stubFilterLLM(t *testing.T, dropIDsJSON string) (string, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Wrap dropIDsJSON in a chat-completion envelope. The inner content
		// must be JSON-encoded to survive marshaling as a string field.
		// Using %q gives us proper JSON-string escaping.
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, dropIDsJSON)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &calls
}

// seedClassified inserts a classified row with sensible defaults so tests
// don't have to spell out every column. Returns the photo name for the
// caller to thread through candidate lists.
func seedClassified(t *testing.T, db *sql.DB, name, povContainer, sceneTimeOfDay string, category []string) string {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO classified
			(photo_id, pov_container, scene_time_of_day, subject_category, classifier_model)
		VALUES ($1, $2, $3, $4, 'test-classifier')
	`, name, povContainer, sceneTimeOfDay, pq.Array(category)); err != nil {
		t.Fatalf("seed classified %s: %v", name, err)
	}
	return name
}

// TestFilterByClassification_EmptyCandidatesNoLLMCall: zero candidates is
// a fast pass-through. Important because cmd/web wires this in front of
// every search; an empty retrieval shouldn't trip an LLM round trip.
func TestFilterByClassification_EmptyCandidatesNoLLMCall(t *testing.T) {
	db := newTempDB(t)
	url, calls := stubFilterLLM(t, `{"drop_ids":[]}`)
	t.Setenv("TEXT_ENDPOINT", url)
	t.Setenv("LM_STUDIO_BASE", url)

	kept, stats, err := FilterByClassification(context.Background(), db, "any query", nil, "stub-model", true)
	if err != nil {
		t.Fatalf("FilterByClassification: %v", err)
	}
	if kept != nil {
		t.Errorf("kept = %v, want nil for empty input", kept)
	}
	if stats.Total != 0 || stats.Dropped != 0 {
		t.Errorf("stats = %+v, want zeroed", stats)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("LLM calls = %d, want 0", got)
	}
}

// TestFilterByClassification_DropsViaLLM exercises the core path: the LLM
// returns drop_ids for the airborne photo; the ground photo passes through.
// Verifies (a) the right photo is dropped, (b) order is preserved for kept,
// (c) stats reflect the drop count.
func TestFilterByClassification_DropsViaLLM(t *testing.T) {
	db := newTempDB(t)
	url, calls := stubFilterLLM(t, `{"drop_ids":["sky_photo"]}`)
	t.Setenv("TEXT_ENDPOINT", url)
	t.Setenv("LM_STUDIO_BASE", url)

	seedPhoto(t, db, "ground_photo")
	seedClassified(t, db, "ground_photo", "ground", "afternoon", []string{"portrait"})
	seedPhoto(t, db, "sky_photo")
	seedClassified(t, db, "sky_photo", "from_plane", "afternoon", []string{"aerial"})

	candidates := []Result{
		{Name: "ground_photo", Similarity: 0.8},
		{Name: "sky_photo", Similarity: 0.75},
	}

	kept, stats, err := FilterByClassification(
		context.Background(), db, "indoor scenes only",
		candidates, "stub-model", false, // useCache=false to skip cache for this test
	)
	if err != nil {
		t.Fatalf("FilterByClassification: %v", err)
	}
	if len(kept) != 1 || kept[0].Name != "ground_photo" {
		t.Errorf("kept = %+v, want [ground_photo]", kept)
	}
	if stats.Total != 2 || stats.Dropped != 1 || stats.LLM != 2 || stats.Cached != 0 {
		t.Errorf("stats = %+v, want {Total:2, Dropped:1, LLM:2, Cached:0}", stats)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("LLM calls = %d, want 1 (one batched call)", got)
	}
}

// TestFilterByClassification_LLMKeepsAllWhenDropListEmpty: drop_ids=[] means
// every candidate passes through unchanged.
func TestFilterByClassification_LLMKeepsAllWhenDropListEmpty(t *testing.T) {
	db := newTempDB(t)
	url, _ := stubFilterLLM(t, `{"drop_ids":[]}`)
	t.Setenv("TEXT_ENDPOINT", url)
	t.Setenv("LM_STUDIO_BASE", url)

	seedPhoto(t, db, "p1")
	seedClassified(t, db, "p1", "ground", "afternoon", nil)
	seedPhoto(t, db, "p2")
	seedClassified(t, db, "p2", "ground", "morning", nil)

	candidates := []Result{{Name: "p1", Similarity: 0.9}, {Name: "p2", Similarity: 0.5}}
	kept, stats, err := FilterByClassification(context.Background(), db, "any", candidates, "stub-model", false)
	if err != nil {
		t.Fatalf("FilterByClassification: %v", err)
	}
	if len(kept) != 2 {
		t.Errorf("kept len = %d, want 2", len(kept))
	}
	if stats.Dropped != 0 {
		t.Errorf("stats.Dropped = %d, want 0", stats.Dropped)
	}
}

// TestFilterByClassification_CacheHitSkipsLLM pre-seeds a fresh drop verdict
// in classify_filter_cache; the LLM should not be called at all. This is
// the cache's whole job — make iteration cheap.
func TestFilterByClassification_CacheHitSkipsLLM(t *testing.T) {
	db := newTempDB(t)
	url, calls := stubFilterLLM(t, `{"drop_ids":[]}`)
	t.Setenv("TEXT_ENDPOINT", url)
	t.Setenv("LM_STUDIO_BASE", url)

	seedPhoto(t, db, "cached_drop")
	seedClassified(t, db, "cached_drop", "from_plane", "afternoon", nil)
	seedPhoto(t, db, "cached_keep")
	seedClassified(t, db, "cached_keep", "ground", "afternoon", nil)

	// Pre-seed cache. Use canonical query form (lowercased + trimmed) since
	// FilterByClassification canonicalizes before lookup.
	nl := "indoor scenes only"
	for _, row := range []struct {
		id   string
		drop bool
	}{
		{"cached_drop", true},
		{"cached_keep", false},
	} {
		if _, err := db.Exec(`
			INSERT INTO classify_filter_cache (nl_query, photo_id, classify_model, drop_verdict)
			VALUES ($1, $2, $3, $4)
		`, CanonicalQuery(nl), row.id, "stub-model", row.drop); err != nil {
			t.Fatalf("seed cache: %v", err)
		}
	}

	candidates := []Result{
		{Name: "cached_drop", Similarity: 0.9},
		{Name: "cached_keep", Similarity: 0.8},
	}
	kept, stats, err := FilterByClassification(context.Background(), db, nl, candidates, "stub-model", true)
	if err != nil {
		t.Fatalf("FilterByClassification: %v", err)
	}
	if len(kept) != 1 || kept[0].Name != "cached_keep" {
		t.Errorf("kept = %+v, want [cached_keep]", kept)
	}
	if stats.Cached != 2 || stats.LLM != 0 {
		t.Errorf("stats = %+v, want {Cached:2, LLM:0}", stats)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("LLM calls = %d, want 0 (full cache hit)", got)
	}
}

// TestFilterByClassification_CacheMissWritesVerdicts covers the post-LLM
// write-back: every candidate the LLM judged should land in the cache
// (drop=true for dropped, drop=false for kept) so a re-run is free.
func TestFilterByClassification_CacheMissWritesVerdicts(t *testing.T) {
	db := newTempDB(t)
	url, _ := stubFilterLLM(t, `{"drop_ids":["airborne"]}`)
	t.Setenv("TEXT_ENDPOINT", url)
	t.Setenv("LM_STUDIO_BASE", url)

	seedPhoto(t, db, "ground")
	seedClassified(t, db, "ground", "ground", "afternoon", nil)
	seedPhoto(t, db, "airborne")
	seedClassified(t, db, "airborne", "from_plane", "afternoon", nil)

	nl := "no aerial"
	candidates := []Result{{Name: "ground", Similarity: 0.8}, {Name: "airborne", Similarity: 0.7}}

	if _, _, err := FilterByClassification(context.Background(), db, nl, candidates, "stub-model", true); err != nil {
		t.Fatalf("FilterByClassification: %v", err)
	}

	// Cache now has both rows. Check both verdicts.
	rows, err := db.Query(`
		SELECT photo_id, drop_verdict
		FROM classify_filter_cache
		WHERE nl_query = $1 AND classify_model = 'stub-model'
		ORDER BY photo_id
	`, CanonicalQuery(nl))
	if err != nil {
		t.Fatalf("query cache: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var id string
		var verdict bool
		if err := rows.Scan(&id, &verdict); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[id] = verdict
	}
	want := map[string]bool{"airborne": true, "ground": false}
	if len(got) != len(want) {
		t.Errorf("cache rows = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("cache[%s] = %v, want %v", k, got[k], v)
		}
	}
}

// TestFilterByClassification_StaleCacheIgnoredAfterReclassify: if a photo
// was re-classified AFTER the cached verdict was written, the cache row is
// stale and must not be used. This is the "re-classifying invalidates" rule.
func TestFilterByClassification_StaleCacheIgnoredAfterReclassify(t *testing.T) {
	db := newTempDB(t)
	url, calls := stubFilterLLM(t, `{"drop_ids":[]}`)
	t.Setenv("TEXT_ENDPOINT", url)
	t.Setenv("LM_STUDIO_BASE", url)

	seedPhoto(t, db, "p")
	seedClassified(t, db, "p", "ground", "morning", nil)

	nl := "afternoon shots"
	// Cache verdict timestamped BEFORE the classified row (stale).
	if _, err := db.Exec(`
		INSERT INTO classify_filter_cache
			(nl_query, photo_id, classify_model, drop_verdict, filtered_at)
		VALUES ($1, 'p', 'stub-model', true, now() - interval '1 hour')
	`, CanonicalQuery(nl)); err != nil {
		t.Fatalf("seed stale cache: %v", err)
	}

	candidates := []Result{{Name: "p", Similarity: 0.9}}
	kept, stats, err := FilterByClassification(context.Background(), db, nl, candidates, "stub-model", true)
	if err != nil {
		t.Fatalf("FilterByClassification: %v", err)
	}
	if len(kept) != 1 {
		t.Errorf("kept len = %d, want 1 (LLM returned empty drop list)", len(kept))
	}
	if stats.Cached != 0 || stats.LLM != 1 {
		t.Errorf("stats = %+v, want {Cached:0, LLM:1} (stale cache row ignored)", stats)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("LLM calls = %d, want 1", got)
	}
}

// TestFilterByClassification_UseCacheFalseBypassesAllCaching: even with a
// fresh cache row, useCache=false must skip both the lookup and the write.
func TestFilterByClassification_UseCacheFalseBypassesAllCaching(t *testing.T) {
	db := newTempDB(t)
	url, calls := stubFilterLLM(t, `{"drop_ids":[]}`)
	t.Setenv("TEXT_ENDPOINT", url)
	t.Setenv("LM_STUDIO_BASE", url)

	seedPhoto(t, db, "p")
	seedClassified(t, db, "p", "ground", "afternoon", nil)

	nl := "any q"
	// Pre-seed a fresh cache row — LLM should still be called because
	// useCache=false.
	if _, err := db.Exec(`
		INSERT INTO classify_filter_cache (nl_query, photo_id, classify_model, drop_verdict)
		VALUES ($1, 'p', 'stub-model', true)
	`, CanonicalQuery(nl)); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	candidates := []Result{{Name: "p", Similarity: 0.9}}
	_, stats, err := FilterByClassification(context.Background(), db, nl, candidates, "stub-model", false)
	if err != nil {
		t.Fatalf("FilterByClassification: %v", err)
	}
	if stats.LLM != 1 || stats.Cached != 0 {
		t.Errorf("stats = %+v, want {LLM:1, Cached:0}", stats)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("LLM calls = %d, want 1", got)
	}

	// And no new cache row should have been written.
	var nRows int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM classify_filter_cache WHERE nl_query = $1 AND photo_id = 'p'
	`, CanonicalQuery(nl)).Scan(&nRows); err != nil {
		t.Fatalf("count cache: %v", err)
	}
	if nRows != 1 {
		t.Errorf("cache row count = %d, want 1 (the pre-seeded row; no write under useCache=false)", nRows)
	}
}

// TestFilterByClassification_LLMWhitelistsResponseIDs: if the LLM returns a
// drop_id that wasn't in the batch (hallucinated), it must not poison the
// drop set. The classifyFilterLLM whitelist step is what enforces this.
func TestFilterByClassification_LLMWhitelistsResponseIDs(t *testing.T) {
	db := newTempDB(t)
	// LLM smuggles in "ghost" — an ID not in the batch. Should be dropped
	// by the whitelist, so the in-batch candidate passes through.
	url, _ := stubFilterLLM(t, `{"drop_ids":["ghost","in_batch"]}`)
	t.Setenv("TEXT_ENDPOINT", url)
	t.Setenv("LM_STUDIO_BASE", url)

	seedPhoto(t, db, "in_batch")
	seedClassified(t, db, "in_batch", "ground", "afternoon", nil)
	seedPhoto(t, db, "other")
	seedClassified(t, db, "other", "ground", "afternoon", nil)

	candidates := []Result{
		{Name: "in_batch", Similarity: 0.9},
		{Name: "other", Similarity: 0.8},
	}
	kept, _, err := FilterByClassification(context.Background(), db, "q", candidates, "stub-model", false)
	if err != nil {
		t.Fatalf("FilterByClassification: %v", err)
	}
	// in_batch dropped (it's in the LLM list AND the batch). ghost ignored.
	if len(kept) != 1 || kept[0].Name != "other" {
		t.Errorf("kept = %+v, want [other]", kept)
	}
}

// TestFormatClassification_OmitsEmptyFields pure-function: only valid+
// non-empty scalar values render; empty arrays are skipped.
func TestFormatClassification_OmitsEmptyFields(t *testing.T) {
	got := formatClassification(
		sql.NullString{String: "ground", Valid: true},
		sql.NullString{}, // pov_altitude absent
		sql.NullString{}, // pov_angle absent
		sql.NullString{String: "on_ground", Valid: true},
		[]string{"portrait", "candid"},
		sql.NullString{}, // subject_distance absent
		sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{},
		nil, // framing absent
		sql.NullString{}, sql.NullString{},
	)
	wantSubstrings := []string{
		"pov_container=ground",
		"subject_altitude=on_ground",
		"subject_category=[portrait,candid]",
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(got, sub) {
			t.Errorf("formatted classification missing %q; got: %s", sub, got)
		}
	}
	// And ABSENT fields should not appear.
	for _, sub := range []string{"pov_altitude", "framing", "subject_distance"} {
		if strings.Contains(got, sub) {
			t.Errorf("formatted classification should omit %q; got: %s", sub, got)
		}
	}
}

// TestFormatClassification_AllEmptyReturnsNoClassification: the sentinel
// string the LLM prompt uses to recognize "this candidate has no
// classifier signal" — instead of an empty line which the model could
// mis-parse as malformed input.
func TestFormatClassification_AllEmptyReturnsNoClassification(t *testing.T) {
	got := formatClassification(
		sql.NullString{}, sql.NullString{}, sql.NullString{},
		sql.NullString{}, nil,
		sql.NullString{}, sql.NullString{}, sql.NullString{},
		sql.NullString{}, sql.NullString{}, sql.NullString{},
		nil, sql.NullString{}, sql.NullString{},
	)
	if got != "(no classification)" {
		t.Errorf("all-empty = %q, want %q", got, "(no classification)")
	}
}
