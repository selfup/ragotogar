package library

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func rewriteServer(t *testing.T, content, finish string) *atomic.Int64 {
	t.Helper()
	calls := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.MaxTokens != 256 {
			t.Errorf("rewrite token budget = %d, want 256", req.MaxTokens)
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]string{"content": content}, "finish_reason": finish,
		}}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("TEXT_ENDPOINT", srv.URL)
	return calls
}

func TestRewriteRejectsUnsafeOutputWithoutCaching(t *testing.T) {
	db := newTempDB(t)
	if _, err := db.Exec(`CREATE TABLE query_rewrite_cache (
		nl_query TEXT, rewrite_model TEXT, rewritten TEXT, rewritten_at TIMESTAMPTZ,
		PRIMARY KEY (nl_query, rewrite_model))`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, output, finish string }{
		{"screenshot", strings.Repeat("飞机", 1000), "stop"},
		{"unsegmented repetition", strings.Repeat("飞机", 12), "stop"},
		{"word repetition", strings.Repeat("airplane ", 8), "stop"},
		{"multiline runaway", "airplane ground\n" + strings.Repeat("road ", 50), "stop"},
		{"truncated plausible text", "airplane ground", "length"},
		{"filtered", "airplane ground", "content_filter"},
		{"empty", "", "stop"},
		{"punctuation only", "---", "stop"},
		{"label only", "Rewritten:", "stop"},
		{"unclosed phrase", `airplane "on the ground`, "stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := rewriteServer(t, tc.output, tc.finish)
			original := "airplane on ground"
			got, err := RewriteQuery(t.Context(), db, original, "chat-model", true)
			if err == nil || got.Rewritten != original || got.Cached || calls.Load() != 1 {
				t.Fatalf("fallback failed: result=%+v err=%v calls=%d", got, err, calls.Load())
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM query_rewrite_cache`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("rejected output was cached: rows=%d err=%v", count, err)
			}
		})
	}
}

func TestRewriteRetainsValidMultilingualAndBooleanOutput(t *testing.T) {
	for _, tc := range []struct{ original, output, want string }{
		{"airplane on ground", `Rewritten: airplane "on the ground"`, `airplane "on the ground"`},
		{"飞机停在地面上", `飞机 "地面" -飞行`, `飞机 "地面" -飞行`},
		{"cafe at night", "```text\ncafé nuit\n```", "café nuit"},
		{"planes not flying", `planes -flying -"in flight" -"in the air" -airborne -aloft -"taking off"`, `planes -flying -"in flight" -"in the air" -airborne -aloft -"taking off"`},
	} {
		t.Run(tc.original, func(t *testing.T) {
			rewriteServer(t, tc.output, "stop")
			got, err := RewriteQuery(t.Context(), nil, tc.original, "chat-model", false)
			if err != nil || got.Rewritten != tc.want {
				t.Fatalf("valid rewrite rejected: result=%+v err=%v", got, err)
			}
		})
	}
}

func TestRewriteEvictsInvalidCachedOutputThenAcceptsFreshResult(t *testing.T) {
	db := newTempDB(t)
	if _, err := db.Exec(`CREATE TABLE query_rewrite_cache (
		nl_query TEXT, rewrite_model TEXT, rewritten TEXT, rewritten_at TIMESTAMPTZ,
		PRIMARY KEY (nl_query, rewrite_model))`); err != nil {
		t.Fatal(err)
	}
	original := "airplane on ground"
	if err := storeRewriteCache(t.Context(), db, CanonicalQuery(original), "chat-model", strings.Repeat("飞机", 12)); err != nil {
		t.Fatal(err)
	}
	calls := rewriteServer(t, `airplane "on the ground"`, "stop")
	got, err := RewriteQuery(t.Context(), db, original, "chat-model", true)
	if err == nil || got.Rewritten != original || got.Cached || calls.Load() != 0 {
		t.Fatalf("bad cache did not fall back: result=%+v err=%v", got, err)
	}
	got, err = RewriteQuery(t.Context(), db, original, "chat-model", true)
	if err != nil || got.Rewritten != `airplane "on the ground"` || got.Cached || calls.Load() != 1 {
		t.Fatalf("fresh rewrite failed: result=%+v err=%v", got, err)
	}
	got, err = RewriteQuery(t.Context(), db, original, "chat-model", true)
	if err != nil || !got.Cached || calls.Load() != 1 {
		t.Fatalf("valid cache not reused: result=%+v err=%v", got, err)
	}
}
