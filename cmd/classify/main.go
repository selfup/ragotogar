// cmd/classify — map description prose to typed enum fields.
//
// Reads each photo's full_description from Postgres, sends it to a small
// text LLM with a classifier prompt, validates the JSON response against
// allowed values, and UPSERTs into the classified table. Default is
// incremental (skip photos that already have a classified row);
// -reclassify rebuilds all.
//
// Usage:
//   go run ./cmd/classify
//   go run ./cmd/classify -reclassify
//   go run ./cmd/classify -dsn postgres:///other_db
//   CLASSIFY_MODEL=mistralai/devstral-small-2-2512 go run ./cmd/classify
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lib/pq"

	"ragotogar/internal/library"
)

func main() {
	var (
		dsn        = flag.String("dsn", library.DefaultDSN(), "Postgres library DSN (overrides LIBRARY_DSN env var)")
		reclassify = flag.Bool("reclassify", false, "TRUNCATE classified before re-classifying all photos")
		workers    = flag.Int("workers", 8, "parallel classifier workers")
	)
	flag.Parse()

	if err := run(*dsn, *reclassify, *workers); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(dsn string, reclassify bool, workers int) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return fmt.Errorf("connect %s: %w", dsn, err)
	}

	ctx := context.Background()

	if reclassify {
		if _, err := db.Exec("TRUNCATE classified"); err != nil {
			return fmt.Errorf("truncate classified: %w", err)
		}
		log.Printf("truncated classified table")
	}

	todo, err := listTodo(db, reclassify)
	if err != nil {
		return err
	}
	if len(todo) == 0 {
		fmt.Printf("Nothing to classify in %s.\n", dsn)
		return nil
	}

	model := library.ClassifyModel()
	fmt.Printf("Classifying %d photo(s) in %s\n", len(todo), dsn)
	fmt.Printf("Model: %s @ %s\n", model, library.LMStudioBase())
	fmt.Printf("Workers: %d\n\n", workers)

	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var ok, failed atomic.Int64

	start := time.Now()
	for i, name := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, n string) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := classifyOne(ctx, db, n, model); err != nil {
				fmt.Fprintf(os.Stderr, "  [error] %s: %v\n", n, err)
				failed.Add(1)
				return
			}
			ord := ok.Add(1)
			if ord%10 == 0 || int(ord)+int(failed.Load()) == len(todo) {
				fmt.Printf("  [%d/%d] last: %s\n", idx+1, len(todo), n)
			}
		}(i, name)
	}
	wg.Wait()
	fmt.Printf("\nDone. Classified: %d, Errors: %d, Elapsed: %s\n",
		ok.Load(), failed.Load(), time.Since(start).Round(time.Second))
	return nil
}

// listTodo returns the photo names that still need classification. When
// reclassify is true, this is every photo with a description; otherwise
// only photos lacking a classified row.
func listTodo(db *sql.DB, reclassify bool) ([]string, error) {
	q := `
		SELECT d.photo_id
		FROM descriptions d
		LEFT JOIN classified c ON c.photo_id = d.photo_id
		WHERE d.full_description IS NOT NULL`
	if !reclassify {
		q += " AND c.photo_id IS NULL"
	}
	q += " ORDER BY d.photo_id"
	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("list todo: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// classifyOne loads the photo's description prose, calls the LLM, validates
// the response, and UPSERTs the typed fields. Returns nil on success.
func classifyOne(ctx context.Context, db *sql.DB, name, model string) error {
	doc, err := loadDescription(db, name)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if doc == "" {
		return fmt.Errorf("empty description")
	}
	raw, err := library.LLMComplete(ctx, model, BuildPrompt(doc))
	if err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	c, err := ParseResponse(raw)
	if err != nil {
		// Dump the full raw body to stderr — the wrapped error truncates,
		// but for diagnosing classifier-output bugs we want everything.
		fmt.Fprintf(os.Stderr, "  [classifier raw output for %s]\n%s\n  [end raw]\n", name, raw)
		return fmt.Errorf("parse: %w", err)
	}
	c = Validate(c)
	if err := upsert(db, name, c, model); err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	return nil
}

// loadDescription concatenates the structured prose fields the describer
// wrote, falling back to full_description if the structured fields are all
// empty (older rows, or rows where the parser failed).
func loadDescription(db *sql.DB, name string) (string, error) {
	var subject, setting, light, colors, composition, vantage, gt, full sql.NullString
	err := db.QueryRow(`
		SELECT subject, setting, light, colors, composition, vantage, ground_truth, full_description
		FROM descriptions WHERE photo_id = $1
	`, name).Scan(&subject, &setting, &light, &colors, &composition, &vantage, &gt, &full)
	if err != nil {
		return "", err
	}
	// Prefer the structured fields when present — cleaner input than the raw blob.
	parts := []struct{ k, v string }{
		{"Subject", subject.String},
		{"Setting", setting.String},
		{"Light", light.String},
		{"Colors", colors.String},
		{"Composition", composition.String},
		{"Vantage", vantage.String},
		{"Ground truth", gt.String},
	}
	var lines []string
	for _, p := range parts {
		if p.v != "" {
			lines = append(lines, p.k+": "+p.v)
		}
	}
	if len(lines) > 0 {
		return strings.Join(lines, "\n"), nil
	}
	return full.String, nil
}

// upsert writes one classification row in a single statement. Arrays use
// pq.Array (the lib/pq driver-agnostic helper) so pgx accepts the TEXT[]
// shape correctly.
func upsert(db *sql.DB, name string, c Classification, model string) error {
	_, err := db.Exec(`
		INSERT INTO classified (
			photo_id,
			pov_container, pov_altitude, pov_angle,
			subject_altitude, subject_category, subject_distance,
			subject_count, animal_count,
			scene_time_of_day, scene_indoor_outdoor, scene_weather,
			framing, motion, color_palette,
			classified_at, classifier_model
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now(), $16
		)
		ON CONFLICT(photo_id) DO UPDATE SET
			pov_container        = EXCLUDED.pov_container,
			pov_altitude         = EXCLUDED.pov_altitude,
			pov_angle            = EXCLUDED.pov_angle,
			subject_altitude     = EXCLUDED.subject_altitude,
			subject_category     = EXCLUDED.subject_category,
			subject_distance     = EXCLUDED.subject_distance,
			subject_count        = EXCLUDED.subject_count,
			animal_count         = EXCLUDED.animal_count,
			scene_time_of_day    = EXCLUDED.scene_time_of_day,
			scene_indoor_outdoor = EXCLUDED.scene_indoor_outdoor,
			scene_weather        = EXCLUDED.scene_weather,
			framing              = EXCLUDED.framing,
			motion               = EXCLUDED.motion,
			color_palette        = EXCLUDED.color_palette,
			classified_at        = now(),
			classifier_model     = EXCLUDED.classifier_model
	`,
		name,
		c.POVContainer, c.POVAltitude, c.POVAngle,
		c.SubjectAltitude, pq.Array(c.SubjectCategory), c.SubjectDistance,
		c.SubjectCount, c.AnimalCount,
		c.SceneTimeOfDay, c.SceneIndoorOutdoor, c.SceneWeather,
		pq.Array(c.Framing), c.Motion, c.ColorPalette,
		model,
	)
	return err
}
