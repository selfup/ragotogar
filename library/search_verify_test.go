package library

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// stubLLMServer returns an httptest server that answers every chat completion
// with the given content. callCount is incremented per request so tests can
// confirm the cache short-circuits the LLM round trip.
func stubLLMServer(t *testing.T, content string) (string, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, content)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &calls
}

// TestVerifyFilterCacheMissCallsLLMAndWrites: first run with empty cache hits
// the LLM, writes the verdict, returns Cached=0 / LLM=N stats.
func TestVerifyFilterCacheMissCallsLLMAndWrites(t *testing.T) {
	db := newTempDB(t)
	url, calls := stubLLMServer(t, "YES")
	t.Setenv("TEXT_ENDPOINT", url)
	t.Setenv("LM_STUDIO_BASE", url)
	t.Setenv("SEARCH_MODEL", "stub-model")

	a := seedPhoto(t, db, "p1")
	b := seedPhoto(t, db, "p2")
	candidates := []Result{{Name: a, Similarity: 0.9}, {Name: b, Similarity: 0.8}}

	s := NewSearcher(db)
	verdicts, stats, err := s.VerifyFilter(context.Background(), "warm light", candidates)
	if err != nil {
		t.Fatalf("VerifyFilter: %v", err)
	}
	if got, want := calls.Load(), int64(2); got != want {
		t.Errorf("LLM calls = %d, want %d", got, want)
	}
	if stats.Total != 2 || stats.Cached != 0 || stats.LLM != 2 {
		t.Errorf("stats = %+v, want {Total:2, Cached:0, LLM:2}", stats)
	}
	for i, v := range verdicts {
		if !v.YES || v.FromCache {
			t.Errorf("verdict[%d] = %+v, want YES=true FromCache=false", i, v)
		}
	}

	// Cache should now have both rows.
	var rowCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM verify_cache WHERE query='warm light' AND verify_model='stub-model'`).Scan(&rowCount); err != nil {
		t.Fatalf("count cache: %v", err)
	}
	if rowCount != 2 {
		t.Errorf("expected 2 cache rows after miss-then-write, got %d", rowCount)
	}
}

// TestVerifyFilterCacheHitSkipsLLM: pre-seed verify_cache, run VerifyFilter,
// confirm zero LLM calls and Cached=2 stats. This is the load-bearing
// optimization — a cache hit must not pay the LLM round trip.
func TestVerifyFilterCacheHitSkipsLLM(t *testing.T) {
	db := newTempDB(t)
	url, calls := stubLLMServer(t, "YES")
	t.Setenv("TEXT_ENDPOINT", url)
	t.Setenv("LM_STUDIO_BASE", url)
	t.Setenv("SEARCH_MODEL", "stub-model")

	a := seedPhoto(t, db, "cached_a")
	b := seedPhoto(t, db, "cached_b")

	ctx := context.Background()
	if err := writeVerifyCache(ctx, db, "indoor", a, "stub-model", true); err != nil {
		t.Fatal(err)
	}
	if err := writeVerifyCache(ctx, db, "indoor", b, "stub-model", false); err != nil {
		t.Fatal(err)
	}

	candidates := []Result{{Name: a, Similarity: 0.9}, {Name: b, Similarity: 0.8}}
	s := NewSearcher(db)
	verdicts, stats, err := s.VerifyFilter(ctx, "indoor", candidates)
	if err != nil {
		t.Fatalf("VerifyFilter: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("LLM calls = %d, want 0 (full cache hit)", got)
	}
	if stats.Total != 2 || stats.Cached != 2 || stats.LLM != 0 {
		t.Errorf("stats = %+v, want {Total:2, Cached:2, LLM:0}", stats)
	}
	if !verdicts[0].YES || !verdicts[0].FromCache {
		t.Errorf("verdict[0] should be cached YES, got %+v", verdicts[0])
	}
	if verdicts[1].YES || !verdicts[1].FromCache {
		t.Errorf("verdict[1] should be cached NO, got %+v", verdicts[1])
	}
}

// TestVerifyFilterMixedCacheHitMissAccountsCorrectly: half the candidates are
// cached, half are misses. Stats reflect the split; only misses hit the LLM.
func TestVerifyFilterMixedCacheHitMissAccountsCorrectly(t *testing.T) {
	db := newTempDB(t)
	url, calls := stubLLMServer(t, "YES")
	t.Setenv("TEXT_ENDPOINT", url)
	t.Setenv("LM_STUDIO_BASE", url)
	t.Setenv("SEARCH_MODEL", "stub-model")

	cachedA := seedPhoto(t, db, "mix_cached_a")
	cachedB := seedPhoto(t, db, "mix_cached_b")
	missA := seedPhoto(t, db, "mix_miss_a")
	missB := seedPhoto(t, db, "mix_miss_b")

	ctx := context.Background()
	if err := writeVerifyCache(ctx, db, "q", cachedA, "stub-model", true); err != nil {
		t.Fatal(err)
	}
	if err := writeVerifyCache(ctx, db, "q", cachedB, "stub-model", false); err != nil {
		t.Fatal(err)
	}

	candidates := []Result{
		{Name: cachedA, Similarity: 0.95},
		{Name: missA, Similarity: 0.90},
		{Name: cachedB, Similarity: 0.85},
		{Name: missB, Similarity: 0.80},
	}
	s := NewSearcher(db)
	_, stats, err := s.VerifyFilter(ctx, "q", candidates)
	if err != nil {
		t.Fatalf("VerifyFilter: %v", err)
	}
	if got, want := calls.Load(), int64(2); got != want {
		t.Errorf("LLM calls = %d, want %d (only misses)", got, want)
	}
	if stats.Total != 4 || stats.Cached != 2 || stats.LLM != 2 {
		t.Errorf("stats = %+v, want {Total:4, Cached:2, LLM:2}", stats)
	}
	if got, want := stats.HitRate(), 0.5; got != want {
		t.Errorf("HitRate = %v, want %v", got, want)
	}
}

// TestVerifyFilterCanonicalizesQuery: queries differing only in case / leading
// whitespace must hit the same cache row. This is the contract that makes
// repeat-query economics work.
func TestVerifyFilterCanonicalizesQuery(t *testing.T) {
	db := newTempDB(t)
	url, calls := stubLLMServer(t, "YES")
	t.Setenv("TEXT_ENDPOINT", url)
	t.Setenv("LM_STUDIO_BASE", url)
	t.Setenv("SEARCH_MODEL", "stub-model")

	a := seedPhoto(t, db, "canon_a")
	candidates := []Result{{Name: a, Similarity: 0.9}}
	s := NewSearcher(db)
	ctx := context.Background()

	// First call — populates cache under canonicalized "warm light bedroom".
	_, _, err := s.VerifyFilter(ctx, "  Warm Light Bedroom  ", candidates)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("after first call, expected 1 LLM call, got %d", calls.Load())
	}

	// Second call — different cosmetic shape, same semantic query. Should
	// be a cache hit.
	_, stats, err := s.VerifyFilter(ctx, "warm light bedroom", candidates)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("second call should hit cache; LLM calls now %d, want still 1", calls.Load())
	}
	if stats.Cached != 1 || stats.LLM != 0 {
		t.Errorf("stats = %+v, want {Cached:1, LLM:0}", stats)
	}
}
