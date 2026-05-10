// cmd/edge serves search out of the static artifacts produced by
// cmd/edge_build. pg stays the system-of-record + hydration store but
// is not in the search query path — search runs entirely against the
// artifacts. See EDGE.md for design and parity contracts.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"ragotogar/library"
)

func main() {
	artifactsDir := flag.String("artifacts", "", "directory containing edge_build output (required)")
	addr := flag.String("addr", ":8081", "HTTP listen address")
	dsn := flag.String("dsn", library.DefaultDSN(), "Postgres library DSN (used for hydration only)")
	flag.Parse()

	if *artifactsDir == "" {
		log.Fatal("-artifacts is required")
	}

	mf, err := loadManifest(*artifactsDir)
	if err != nil {
		log.Fatalf("load manifest: %v", err)
	}
	log.Printf("manifest: corpus_hash=%s built_at=%s dim=%d quantization=%s photos=%d",
		mf.CorpusHash[:12]+"...", mf.BuiltAt, mf.Dim, mf.Quantization, mf.IDSpace.Count)

	if err := checkEmbedderDrift(mf); err != nil {
		log.Fatalf("embedder drift check: %v", err)
	}
	log.Printf("drift check ok: all lanes match runtime EMBED_MODEL=%s", library.EmbedModel())

	arts, err := openArtifacts(*artifactsDir, mf)
	if err != nil {
		log.Fatalf("open artifacts: %v", err)
	}
	defer arts.Close()
	log.Printf("artifacts loaded: fst (%d terms reachable), postings=%d B, payload=%d B, lanes={descriptions:%d, metadata:%d, queries:%d}",
		arts.FST.Len(),
		len(arts.Postings),
		len(arts.PayloadBytes),
		arts.Lanes["descriptions"].Rows,
		arts.Lanes["metadata"].Rows,
		arts.Lanes["queries"].Rows,
	)

	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("ping db (%s): %v", library.MaskDSN(*dsn), err)
	}
	log.Printf("pg connected: %s (hydration only — not in search path)", library.MaskDSN(*dsn))

	srvState := &server{arts: arts, mux: http.NewServeMux()}
	srvState.mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":           true,
			"corpus_hash":  mf.CorpusHash,
			"photos":       mf.IDSpace.Count,
			"schema":       mf.SchemaVersion,
			"quantization": mf.Quantization,
		})
	})
	srvState.mux.HandleFunc("/search", srvState.handleSearch)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           srvState.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("edge listening on %s", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

// loadManifest reads <dir>/manifest.json, validates schema_version,
// and returns the parsed struct. Schema-version mismatch is fatal —
// see Manifest comment.
func loadManifest(dir string) (*Manifest, error) {
	path := filepath.Join(dir, "manifest.json")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var mf Manifest
	if err := json.NewDecoder(f).Decode(&mf); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if mf.SchemaVersion != supportedManifestVersion {
		return nil, fmt.Errorf("manifest schema_version=%d, this binary supports %d", mf.SchemaVersion, supportedManifestVersion)
	}
	if mf.IDSpace.Count != len(mf.IDSpace.Names) {
		return nil, fmt.Errorf("manifest id_space inconsistent: count=%d, names=%d", mf.IDSpace.Count, len(mf.IDSpace.Names))
	}
	return &mf, nil
}

// checkEmbedderDrift compares each lane's manifest-claimed
// embedder_version to the runtime's library.EmbedModel(). Mismatch is
// fatal — the per-lane field exists precisely to catch silent drift
// (operator-asserted at build time, runtime-verified here). Comparing
// only the env value rather than probing /v1/models is v1's pragmatic
// posture: drift-detect at startup, not per-query.
func checkEmbedderDrift(mf *Manifest) error {
	want := library.EmbedModel()
	for lane, entry := range mf.Lanes {
		if entry.EmbedderVersion != want {
			return fmt.Errorf("lane %q claims embedder_version=%q, runtime EMBED_MODEL=%q — re-run cmd/edge_build with the runtime model, or set EMBED_MODEL to match", lane, entry.EmbedderVersion, want)
		}
	}
	return nil
}
