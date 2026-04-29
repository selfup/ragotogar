package main

import (
	"database/sql"
	"flag"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type result struct {
	Name string
}

type pageData struct {
	Q       string
	Mode    string // "naive" | "local" | "hybrid"
	Results []result
}

// validModes mirrors search.py's --mode choices, minus "global" (synthesis-only).
// "naive-verify" is a cmd/web-specific compound mode → search.py --retrieve
// --mode naive --verify (LLM filters each candidate).
var validModes = map[string]bool{
	"naive":        true,
	"naive-verify": true,
	"local":        true,
	"hybrid":       true,
}

func resolveMode(m string) string {
	if validModes[m] {
		return m
	}
	// naive (pure vector similarity) is the validated default — graph modes
	// underperform on small corpora where entity coverage is thin.
	// See STRATEGIES.md "Why naive mode is better for retrieval".
	return "naive"
}

func defaultDSN() string {
	if v := os.Getenv("LIBRARY_DSN"); v != "" {
		return v
	}
	return "postgres:///ragotogar"
}

func main() {
	var (
		addr     = flag.String("addr", ":8080", "listen address")
		dsn      = flag.String("dsn", defaultDSN(), "Postgres library DSN (overrides LIBRARY_DSN env var)")
		repoRoot = flag.String("repo", ".", "repo root (where tools/search.sh lives)")
	)
	flag.Parse()

	absRepo, err := filepath.Abs(*repoRoot)
	if err != nil {
		log.Fatalf("invalid -repo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(absRepo, "scripts", "search.sh")); err != nil {
		log.Fatalf("search.sh not found under %s/scripts: %v", absRepo, err)
	}

	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("ping db (%s): %v", *dsn, err)
	}

	indexTmpl := template.Must(template.New("index").Parse(indexHTML))
	photoTmpl := template.Must(template.New("photo").Funcs(templateFuncMap()).Parse(photoHTML))

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		mode := resolveMode(r.URL.Query().Get("mode"))
		var results []result
		if q != "" {
			results = search(db, q, mode, absRepo)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := indexTmpl.Execute(w, pageData{Q: q, Mode: mode, Results: results}); err != nil {
			log.Printf("template: %v", err)
		}
	})
	mux.HandleFunc("/photos/", func(w http.ResponseWriter, r *http.Request) {
		// Both /photos/<name> (HTML) and /photos/<name>.jpg (BLOB) live here.
		path := strings.TrimPrefix(r.URL.Path, "/photos/")
		if strings.HasSuffix(path, ".jpg") {
			servePhotoJPG(w, r, db, strings.TrimSuffix(path, ".jpg"))
			return
		}
		servePhotoHTML(w, r, db, photoTmpl, path)
	})
	// styles.css lives at the repo root and is the cashier design system.
	// http.ServeFile sets ETag + handles If-Modified-Since for browser caching.
	mux.HandleFunc("/styles.css", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(absRepo, "styles.css"))
	})

	log.Printf("library: %s", *dsn)
	log.Printf("repo:    %s", absRepo)
	log.Printf("listening on http://localhost%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
