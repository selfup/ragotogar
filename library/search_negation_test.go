package library

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
)

func newNegationSearchDB(t *testing.T) *sql.DB {
	t.Helper()
	db := newTempDB(t)
	t.Setenv("EMBED_MODEL", "negation-test")
	t.Setenv("EMBED_DIM", "2")
	if _, err := db.Exec(EmbeddingConfigSchema + `
		INSERT INTO embedding_config (model, dimensions) VALUES ('negation-test', 2);
		CREATE TABLE photo_descriptions (photo_id TEXT, schema_version INTEGER, embedding halfvec(2));
		CREATE TABLE photo_metadata (photo_id TEXT, schema_version INTEGER, embedding halfvec(2));
		CREATE TABLE photo_queries (photo_id TEXT, schema_version INTEGER, embedding halfvec(2));
	`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSearchSpacedNegation(t *testing.T) {
	db := newNegationSearchDB(t)
	for _, photo := range []struct{ name, description string }{
		{"truck", "A black Ford F-150 pickup truck, moving forward on a highway."},
		{"sedan", "A white sedan traveling on a highway."},
		// No literal highway match: this result must survive through vectors.
		{"bridge", "An overpass spanning a busy road."},
	} {
		id := seedPhoto(t, db, photo.name)
		seedDescription(t, db, id, photo.description, photo.description)
		for _, store := range []string{"photo_descriptions", "photo_metadata", "photo_queries"} {
			if _, err := db.Exec(fmt.Sprintf(`INSERT INTO %s VALUES ($1, $2, '[1,0]')`, store), id, V2SchemaVersion); err != nil {
				t.Fatal(err)
			}
		}
	}
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "bad embedding request", http.StatusBadRequest)
			return
		}
		if !reflect.DeepEqual(req.Input, []string{"highway"}) {
			t.Errorf("embedding input = %q, want only highway", req.Input)
		}
		fmt.Fprintln(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("EMBED_ENDPOINT", srv.URL)
	searcher := NewSearcher(db)
	for _, hybrid := range []bool{false, true} {
		for _, tc := range []struct {
			query string
			want  []string
		}{
			{"highway", []string{"bridge", "sedan", "truck"}},
			{"highway -truck", []string{"bridge", "sedan"}},
			{"highway - truck", []string{"bridge", "sedan"}},
			{"highway -\t\n truck", []string{"bridge", "sedan"}},
			{`highway - "pickup truck"`, []string{"bridge", "sedan"}},
			{"highway - truck -sedan", []string{"bridge"}},
		} {
			t.Run(fmt.Sprintf("hybrid=%v/%s", hybrid, tc.query), func(t *testing.T) {
				opts := DefaultSearchOptionsV2()
				opts.VectorQuery = StripNegation(tc.query)
				search := searcher.SearchV2
				if hybrid {
					search = searcher.SearchHybridV2
				}
				before := calls.Load()
				got, err := search(t.Context(), tc.query, opts)
				if err != nil {
					t.Fatal(err)
				}
				if got := calls.Load() - before; got != 1 {
					t.Errorf("embedding calls = %d, want 1", got)
				}
				names := resultNames(got)
				sort.Strings(names)
				if !reflect.DeepEqual(names, tc.want) {
					t.Errorf("results = %v, want %v", names, tc.want)
				}
			})
		}
	}
}

func TestAutoRewriteExclusionsAcrossRetrievalArms(t *testing.T) {
	db := newNegationSearchDB(t)
	if _, err := db.Exec(`CREATE TABLE query_rewrite_cache (
		nl_query TEXT, rewrite_model TEXT, rewritten TEXT, rewritten_at TIMESTAMPTZ,
		PRIMARY KEY (nl_query, rewrite_model))`); err != nil {
		t.Fatal(err)
	}
	for _, photo := range []struct {
		name, description string
		vector            bool
	}{
		{"truck", "A black pickup truck passing a red car.", true},
		{"suv", "A car beside an SUV.", true},
		{"lexical-truck", "A car beside a truck.", false},
		{"sedan", "A sedan.", true},
		{"lexical-car", "A car.", false},
		{"vector-only", "A four-door passenger vehicle on the road.", true},
	} {
		id := seedPhoto(t, db, photo.name)
		seedDescription(t, db, id, photo.description, photo.description)
		if photo.vector {
			if _, err := db.Exec(`INSERT INTO photo_descriptions VALUES ($1, $2, '[1,0]')`, id, V2SchemaVersion); err != nil {
				t.Fatal(err)
			}
		}
	}
	const original = "4 door car or sedan NOT truck NOT suv"
	const rewritten = "(car OR sedan) -truck -suv"
	rewriteCalls := rewriteServer(t, rewritten, "stop")
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "bad embedding request", http.StatusBadRequest)
			return
		}
		if !reflect.DeepEqual(req.Input, []string{"(car OR sedan)"}) {
			t.Errorf("embedding input includes exclusions or loses alternatives: %q", req.Input)
		}
		fmt.Fprintln(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
	}))
	t.Cleanup(embed.Close)
	t.Setenv("EMBED_ENDPOINT", embed.URL)
	s := NewSearcher(db)
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%v", cached), func(t *testing.T) {
			rw, err := RewriteQuery(t.Context(), db, original, "chat-model", true)
			if err != nil || rw.Rewritten != rewritten || rw.Cached != cached {
				t.Fatalf("rewrite=%+v err=%v", rw, err)
			}
			opts := SearchOptionsV2{UseDescriptions: true, VectorQuery: StripNegation(rw.Rewritten)}
			for _, hybrid := range []bool{false, true} {
				search := s.SearchV2
				want := []string{"sedan", "vector-only"}
				if hybrid {
					search = s.SearchHybridV2
					want = []string{"lexical-car", "sedan", "vector-only"}
				}
				got, err := search(t.Context(), rw.Rewritten, opts)
				if err != nil {
					t.Fatal(err)
				}
				names := resultNames(got)
				sort.Strings(names)
				if !reflect.DeepEqual(names, want) {
					t.Errorf("hybrid=%v results=%v, want %v", hybrid, names, want)
				}
			}
		})
	}
	if rewriteCalls.Load() != 1 {
		t.Fatalf("rewrite calls = %d, want one live rewrite and one cache hit", rewriteCalls.Load())
	}
}
