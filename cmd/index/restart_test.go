package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"ragotogar/library"
	"ragotogar/library/testdb"
)

func TestRunResumeSkipsPhotosWithoutQuerySources(t *testing.T) {
	db, dsn := testdb.NewWithDSN(t, "index_resume", testdb.SchemaSQL(indexTestSchema))
	embedURL, calls := stubEmbedServer(t)
	t.Setenv("EMBED_ENDPOINT", embedURL)

	for _, name := range []string{"complete", "partial", "has_queries", "empty_queries", "null_queries"} {
		var queries []string
		if name == "has_queries" {
			queries = []string{"a cafe portrait"}
		}
		seedPhotoForIndex(t, db, name, queries)
		photo, err := library.LoadPhoto(db, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := indexPhotoStore(context.Background(), db, photo, descriptionsStore); err != nil {
			t.Fatal(err)
		}
		// Simulate an interruption after the description transaction committed
		// but before the metadata transaction for this photo.
		if name != "partial" {
			if _, err := indexPhotoStore(context.Background(), db, photo, metadataStore); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, queries := range map[string]string{"empty_queries": "[]", "null_queries": "null"} {
		if _, err := db.Exec(`INSERT INTO query_generations (photo_id, schema_version, model, prompt_hash, queries)
			VALUES ($1, 2, 'test-model', 'h', $2::jsonb)`, name, queries); err != nil {
			t.Fatal(err)
		}
	}
	calls.Store(0)

	output := runIndexOutput(t, dsn)
	if !strings.Contains(output, "Done. 2 photo(s) processed") {
		t.Errorf("resume should queue only the missing metadata and available query: %s", output)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("resume made %d embed calls, want 2", got)
	}
	if got := countStoreRows(t, db, "photo_metadata", "partial"); got != 1 {
		t.Errorf("partial photo metadata not resumed: %d rows", got)
	}
	if got := countStoreRows(t, db, "photo_queries", "has_queries"); got != 1 {
		t.Errorf("available query not indexed: %d rows", got)
	}

	calls.Store(0)
	output = runIndexOutput(t, dsn)
	if !strings.Contains(output, "Done. 0 photo(s) processed") || calls.Load() != 0 {
		t.Errorf("completed restart should have no work: calls=%d output=%s", calls.Load(), output)
	}

	// A later describe run can supply queries. No persistent skip marker
	// should prevent the next index run from discovering them.
	if _, err := db.Exec(`INSERT INTO query_generations (photo_id, schema_version, model, prompt_hash, queries)
		VALUES ('complete', 2, 'test-model', 'h', '["new phrasing"]'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	output = runIndexOutput(t, dsn)
	if !strings.Contains(output, "Done. 1 photo(s) processed") || calls.Load() != 1 {
		t.Errorf("new query source not picked up: calls=%d output=%s", calls.Load(), output)
	}
	if got := countStoreRows(t, db, "photo_queries", "complete"); got != 1 {
		t.Errorf("new phrasing not indexed: %d rows", got)
	}
}

// Index tests run serially; capture the CLI output to assert the actual worker
// count as well as HTTP calls and committed rows across repeated invocations.
func runIndexOutput(t *testing.T, dsn string) string {
	t.Helper()
	return runIndexOutputWith(t, dsn, 2, defaultBatchSize)
}

func runIndexOutputWith(t *testing.T, dsn string, workers, batchSize int) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "index-output")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stdout := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = stdout }()
	if err := run(dsn, reindexSet{}, workers, batchSize); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}
