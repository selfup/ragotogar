package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/pgvector/pgvector-go"

	"ragotogar/library"
	"ragotogar/library/testdb"
)

// Return distinct, exactly representable halfvec markers for every text, in
// reverse response order. This catches misplaced vectors as well as bad batch
// sizes. An optional one-shot failure lets the same endpoint serve a retry.
type batchRecorder struct {
	mu       sync.Mutex
	requests [][]string
	markers  map[string]float32
}

func batchEmbedServer(t *testing.T, failAt, failStatus int) *batchRecorder {
	t.Helper()
	return batchEmbedServerWithDim(t, failAt, failStatus, library.EmbedDim)
}

func batchEmbedServerWithDim(t *testing.T, failAt, failStatus, dim int) *batchRecorder {
	t.Helper()
	recorder := &batchRecorder{markers: map[string]float32{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, req.Input)
		call := len(recorder.requests)
		data := make([]map[string]any, len(req.Input))
		for i, text := range req.Input {
			marker, ok := recorder.markers[text]
			if !ok {
				marker = float32(len(recorder.markers) + 1)
				recorder.markers[text] = marker
			}
			vector := make([]float32, dim)
			vector[0] = marker
			data[len(data)-1-i] = map[string]any{"index": i, "embedding": vector}
		}
		recorder.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if call == failAt {
			w.WriteHeader(failStatus)
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("EMBED_ENDPOINT", srv.URL)
	return recorder
}

func (r *batchRecorder) batches() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.requests...)
}

func assertStoredBatchVectors(t *testing.T, db *sql.DB, recorder *batchRecorder) {
	t.Helper()
	rows, err := db.Query(`SELECT chunk_text, embedding FROM photo_descriptions
		UNION ALL SELECT metadata_text, embedding FROM photo_metadata
		UNION ALL SELECT query_text, embedding FROM photo_queries`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var text string
		var vector pgvector.HalfVector
		if err := rows.Scan(&text, &vector); err != nil {
			t.Fatal(err)
		}
		recorder.mu.Lock()
		want, ok := recorder.markers[text]
		recorder.mu.Unlock()
		if !ok || vector.Slice()[0] != want {
			t.Errorf("text %.80q has vector marker %g, want %g (recorded=%v)", text, vector.Slice()[0], want, ok)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRunBatchesAcrossPhotos(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		workers, batchSize, photos, calls int
	}{
		{"default_serial", 1, defaultBatchSize, 23, 11},
		{"default_parallel", 2, defaultBatchSize, 23, 11},
		{"single_document", 1, 1, 3, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, dsn := testdb.NewWithDSN(t, "index_batch", testdb.SchemaSQL(indexTestSchema))
			recorder := batchEmbedServer(t, 0, 0)
			for i := range tc.photos {
				name := fmt.Sprintf("photo_%02d", i)
				seedPhotoForIndex(t, db, name, []string{name + " at dawn", name + " at dusk"})
				if _, err := db.Exec(`UPDATE exif SET camera_model = $1 WHERE photo_id = $1`, name); err != nil {
					t.Fatal(err)
				}
			}
			output := runIndexOutputWith(t, dsn, tc.workers, tc.batchSize)
			if !strings.Contains(output, fmt.Sprintf("Done. %d photo(s) processed", tc.photos)) ||
				!strings.Contains(output, fmt.Sprintf("Batch size: %d documents", tc.batchSize)) {
				t.Fatalf("unexpected output: %s", output)
			}
			batches := recorder.batches()
			if len(batches) != tc.calls {
				t.Fatalf("got %d requests, want %d", len(batches), tc.calls)
			}
			var inputs int
			for _, batch := range batches {
				if len(batch) == 0 || len(batch) > tc.batchSize {
					t.Errorf("request has %d inputs, limit %d", len(batch), tc.batchSize)
				}
				inputs += len(batch)
			}
			if inputs != tc.photos*4 { // one description, one metadata, two queries
				t.Errorf("embedded %d inputs, want %d", inputs, tc.photos*4)
			}
			for i := range tc.photos {
				name := fmt.Sprintf("photo_%02d", i)
				for table, want := range map[string]int{"photo_descriptions": 1, "photo_metadata": 1, "photo_queries": 2} {
					if got := countStoreRows(t, db, table, name); got != want {
						t.Errorf("%s/%s has %d rows, want %d", name, table, got, want)
					}
				}
			}
			assertStoredBatchVectors(t, db, recorder)
			// A finished final partial batch must also be skipped on restart.
			runIndexOutputWith(t, dsn, tc.workers, tc.batchSize)
			if got := len(recorder.batches()); got != tc.calls {
				t.Errorf("restart made %d extra requests", got-tc.calls)
			}
		})
	}
}

func TestBatchFailureDoesNotCommitPartialPhoto(t *testing.T) {
	db, dsn := testdb.NewWithDSN(t, "index_batch_failure", testdb.SchemaSQL(indexTestSchema))
	recorder := batchEmbedServer(t, 2, http.StatusOK) // malformed second response
	var photos []*library.Photo
	for _, name := range []string{"a_complete", "b_long", "c_affected"} {
		seedPhotoForIndex(t, db, name, nil)
		if name == "b_long" {
			if _, err := db.Exec(`UPDATE descriptions SET full_description = $1 WHERE photo_id = $2`,
				strings.Repeat("Forest path and trees. ", 3000), name); err != nil {
				t.Fatal(err)
			}
		}
		photo, err := library.LoadPhoto(db, name)
		if err != nil {
			t.Fatal(err)
		}
		photos = append(photos, photo)
	}
	wantChunks := len(descriptionsStore.documents(photos[1]))
	if wantChunks <= defaultBatchSize {
		t.Fatal("fixture must span more than one embedding request")
	}
	results, err := indexStoreBatch(context.Background(), db, descriptionsStore, photos, defaultBatchSize)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].added != 1 || results[0].err != nil {
		t.Fatalf("unaffected photo not committed: %+v", results[0])
	}
	for i := 1; i < len(photos); i++ {
		if results[i].err == nil || countStoreRows(t, db, "photo_descriptions", photos[i].Name) != 0 {
			t.Errorf("failed batch left partial rows for %s: %+v", photos[i].Name, results[i])
		}
	}
	previousCalls := len(recorder.batches())
	runIndexOutputWith(t, dsn, 1, defaultBatchSize)
	if got := countStoreRows(t, db, "photo_descriptions", "b_long"); got != wantChunks {
		t.Errorf("resume wrote %d chunks, want all %d", got, wantChunks)
	}
	for _, batch := range recorder.batches()[previousCalls:] {
		if len(batch) > defaultBatchSize {
			t.Errorf("long photo exceeded request limit: %d inputs", len(batch))
		}
		for _, text := range batch {
			if strings.Contains(text, "Photo: a_complete") {
				t.Error("resume re-embedded an already completed description")
			}
		}
	}
	assertStoredBatchVectors(t, db, recorder)
}

func TestBatchReindexKeepsExistingRowsOnEmbeddingFailure(t *testing.T) {
	db := newTempDB(t)
	batchEmbedServer(t, 0, 0)
	queries := []string{"old query"}
	seedPhotoForIndex(t, db, "p1", queries)
	photo, err := library.LoadPhoto(db, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexPhotoStore(context.Background(), db, photo, queriesStore); err != nil {
		t.Fatal(err)
	}
	photo.GeneratedQueries = nil
	for i := range 13 {
		photo.GeneratedQueries = append(photo.GeneratedQueries, fmt.Sprintf("new query %d", i))
	}
	recorder := batchEmbedServer(t, 2, http.StatusBadRequest)
	_, err = indexStoreBatch(context.Background(), db, queriesStore, []*library.Photo{photo}, defaultBatchSize)
	if !errors.Is(err, library.ErrNonRetryable) {
		t.Fatalf("expected permanent error, got %v", err)
	}
	batches := recorder.batches()
	if len(batches) != 2 || len(batches[0]) != 10 || len(batches[1]) != 3 {
		t.Fatalf("queries were not split into 10 + 3 inputs: %v", batches)
	}
	var got string
	if err := db.QueryRow(`SELECT query_text FROM photo_queries WHERE photo_id = 'p1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "old query" || countStoreRows(t, db, "photo_queries", "p1") != 1 {
		t.Fatalf("failed replacement changed existing query rows: %q", got)
	}
}

func TestRunAbortsBatchWorkersOnPermanentFailure(t *testing.T) {
	db, dsn := testdb.NewWithDSN(t, "index_batch_abort", testdb.SchemaSQL(indexTestSchema))
	recorder := batchEmbedServer(t, 2, http.StatusBadRequest)
	for i := range 23 {
		seedPhotoForIndex(t, db, fmt.Sprintf("photo_%02d", i), nil)
	}
	err := run(dsn, reindexSet{}, 1, defaultBatchSize)
	if !errors.Is(err, library.ErrNonRetryable) {
		t.Fatalf("expected permanent error, got %v", err)
	}
	if got := len(recorder.batches()); got != 2 {
		t.Errorf("made %d requests after permanent failure, want 2", got)
	}
	for i := range 23 {
		want := 0
		if i < 10 {
			want = 1 // description store committed before metadata failed
		}
		name := fmt.Sprintf("photo_%02d", i)
		if got := countStoreRows(t, db, "photo_descriptions", name); got != want {
			t.Errorf("%s has %d descriptions, want %d", name, got, want)
		}
		if got := countStoreRows(t, db, "photo_metadata", name); got != 0 {
			t.Errorf("failed metadata request left %d rows", got)
		}
	}
}

func TestRunRejectsInvalidBatchSizeBeforeConnecting(t *testing.T) {
	for _, size := range []int{0, -1} {
		if err := run("invalid dsn", reindexSet{}, 1, size); err == nil || !strings.Contains(err.Error(), "batch-size") {
			t.Errorf("batch size %d: got %v", size, err)
		}
	}
}
