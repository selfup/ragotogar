package main

import (
	"os"
	"strings"
	"testing"

	"ragotogar/library"
)

func TestBuildRejectsUnknownAndMislabeledEmbeddingModels(t *testing.T) {
	db := newTempDB(t)
	out := t.TempDir()
	if err := run(db, out, "invented-model"); err == nil || !strings.Contains(err.Error(), "no recorded embedding model") {
		t.Fatalf("unrecorded model accepted: %v", err)
	}
	if _, err := db.Exec(library.EmbeddingConfigSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO embedding_config (model, dimensions) VALUES ('actual-model', 2560)`); err != nil {
		t.Fatal(err)
	}
	if err := run(db, out, "invented-model"); err == nil || !strings.Contains(err.Error(), "actual-model") {
		t.Fatalf("mislabeled model accepted: %v", err)
	}
	files, err := os.ReadDir(out)
	if err != nil || len(files) != 0 {
		t.Fatalf("rejected build wrote artifacts: files=%v err=%v", files, err)
	}
}
