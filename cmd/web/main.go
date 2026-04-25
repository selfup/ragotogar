package main

import (
	"flag"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type result struct {
	Name string
}

type pageData struct {
	Q       string
	Mode    string // "naive" | "local" | "hybrid"
	Results []result
}

// validModes mirrors search.py's --mode choices, minus "global" (which writes
// thematic summaries — useless for photo lookup).
var validModes = map[string]bool{"naive": true, "local": true, "hybrid": true}

func resolveMode(m string) string {
	if validModes[m] {
		return m
	}
	// naive (pure vector similarity) is the validated default — graph modes
	// underperform on small corpora where entity coverage is thin.
	// See STRATEGIES.md "Why naive mode is better for retrieval".
	return "naive"
}

func main() {
	var (
		addr     = flag.String("addr", ":8080", "listen address")
		photoDir = flag.String("dir", "describe_output", "photo output directory (where cashier wrote .jpg/.html)")
		repoRoot = flag.String("repo", ".", "repo root (where tools/search.sh lives)")
	)
	flag.Parse()

	absDir, err := filepath.Abs(*photoDir)
	if err != nil {
		log.Fatalf("invalid -dir: %v", err)
	}
	absRepo, err := filepath.Abs(*repoRoot)
	if err != nil {
		log.Fatalf("invalid -repo: %v", err)
	}
	if _, err := os.Stat(absDir); err != nil {
		log.Fatalf("photo dir %s: %v", absDir, err)
	}
	if _, err := os.Stat(filepath.Join(absRepo, "tools", "search.sh")); err != nil {
		log.Fatalf("search.sh not found under %s/tools: %v", absRepo, err)
	}

	tmpl := template.Must(template.New("index").Parse(indexHTML))

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
			results = search(q, mode, absRepo, absDir)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, pageData{Q: q, Mode: mode, Results: results}); err != nil {
			log.Printf("template: %v", err)
		}
	})
	mux.Handle("/photos/", http.StripPrefix("/photos/", http.FileServer(http.Dir(absDir))))

	log.Printf("photos: %s", absDir)
	log.Printf("repo:   %s", absRepo)
	log.Printf("listening on http://localhost%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
