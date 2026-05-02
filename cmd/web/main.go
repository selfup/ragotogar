package main

import (
	"database/sql"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"ragotogar/library"
)

type result struct {
	Name string
}

type pageData struct {
	Q               Q
	Mode            string // "naive" | "naive-verify" | "fts-vector" | "fts-vector-verify"
	Sort            string // "relevance" (default) | "date-desc" | "date-asc"
	CosineThreshold string // formatted for the slider's value attribute
	FTSThresholdRel string
	Latency         string // formatted "234 ms" / "1.2 s"; empty when no search ran
	Total           int    // total photos in library; 0 = don't show
	Results         []result
	VerifyStats     *verifyStatsView // nil when no verify pass ran; non-nil enables the cache footer
}

// verifyStatsView is the template-friendly projection of library.VerifyStats.
// HitRate is pre-formatted as a percentage string so the template doesn't have
// to do arithmetic.
type verifyStatsView struct {
	Total   int
	Cached  int
	LLM     int
	HitRate string // "60%" / "0%" — formatted at the boundary
}

// countPhotos returns the total photo count for the status-line denominator.
// Errors are swallowed — the count is purely informational and a missing
// total just hides the "(out of N images)" suffix.
func countPhotos(db *sql.DB) int {
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM photos").Scan(&n)
	return n
}

// formatLatency renders a search duration for the status line. Sub-second
// gets ms (more readable than "0.234s"); ≥ 1s gets one decimal.
func formatLatency(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1f s", d.Seconds())
}

// Q is the trimmed query string. Wrapping it as a named type (vs. plain
// string) keeps the template binding readable since pageData has several
// string fields that mean different things.
type Q string

// parseThreshold reads a 0..1 float URL param and falls back to the default
// when missing or malformed. Clamps into [0, 1] so URL fiddling can't push
// the cosine cutoff into nonsense territory (negative, 5x, etc.).
func parseThreshold(raw string, fallback float64) float64 {
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// validModes — four pills the UI exposes.
//   naive             : pure vector cosine, cosine ≥ 0.5
//   naive-verify      : vector + LLM yes/no verify
//   fts-vector        : vector ∪ FTS via Reciprocal Rank Fusion
//   fts-vector-verify : RRF fusion + LLM yes/no verify
var validModes = map[string]bool{
	"naive":             true,
	"naive-verify":      true,
	"fts-vector":        true,
	"fts-vector-verify": true,
}

func resolveMode(m string) string {
	if validModes[m] {
		return m
	}
	// Pure vector is the validated default. Unknown modes (including the
	// retired "local" / "hybrid" LightRAG names) fall back here so old
	// bookmarks don't 404.
	return "naive"
}

// validSorts — three orderings the UI exposes.
//   relevance : keep the order returned by retrieval (cosine / RRF / verify)
//   date-desc : exif.date_taken DESC, NULL last
//   date-asc  : exif.date_taken ASC,  NULL last
var validSorts = map[string]bool{
	"relevance": true,
	"date-desc": true,
	"date-asc":  true,
}

func resolveSort(s string) string {
	if validSorts[s] {
		return s
	}
	return "relevance"
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
	if _, err := os.Stat(filepath.Join(absRepo, "styles.css")); err != nil {
		log.Fatalf("styles.css not found at %s: %v", absRepo, err)
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
		sortBy := resolveSort(r.URL.Query().Get("sort"))
		cosine := parseThreshold(r.URL.Query().Get("cosine"), library.CosineThreshold)
		ftsRel := parseThreshold(r.URL.Query().Get("fts"), library.FTSRelativeThreshold)

		var (
			results    []result
			latency    string
			total      int
			verifyView *verifyStatsView
		)
		if q != "" {
			res := search(db, q, mode, cosine, ftsRel)
			results = applySort(db, res.Results, sortBy)
			latency = formatLatency(res.Elapsed)
			total = countPhotos(db)
			if res.Stats != nil {
				verifyView = &verifyStatsView{
					Total:   res.Stats.Total,
					Cached:  res.Stats.Cached,
					LLM:     res.Stats.LLM,
					HitRate: fmt.Sprintf("%.0f%%", res.Stats.HitRate()*100),
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := indexTmpl.Execute(w, pageData{
			Q:               Q(q),
			Mode:            mode,
			Sort:            sortBy,
			CosineThreshold: fmt.Sprintf("%.2f", cosine),
			FTSThresholdRel: fmt.Sprintf("%.2f", ftsRel),
			Latency:         latency,
			Total:           total,
			Results:         results,
			VerifyStats:     verifyView,
		}); err != nil {
			log.Printf("template: %v", err)
		}
	})
	mux.HandleFunc("/photos/", func(w http.ResponseWriter, r *http.Request) {
		// Both /photos/<name> (HTML) and /photos/<name>.jpg (BLOB) live here.
		path := strings.TrimPrefix(r.URL.Path, "/photos/")
		if before, ok := strings.CutSuffix(path, ".jpg"); ok {
			servePhotoJPG(w, r, db, before)
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
