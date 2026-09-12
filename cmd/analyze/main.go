// Command analyze reports on the photo library without modifying it or calling an LLM.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"ragotogar/library"
)

type reportSpec struct{ name, description, query string }

// All reports start from the photo registry. Missing metadata stays visible as
// NULL groups, and camera/year filters always refer to EXIF, not model output.
const scope = `WITH scoped AS (
 SELECT p.id, p.name, e.camera_model, e.lens_model, e.date_taken_year AS year,
 e.date_taken_month AS month, e.f_number
 FROM photos p LEFT JOIN exif e ON e.photo_id=p.id
 WHERE ($1::text='' OR e.camera_model=$1) AND ($2::int=0 OR e.date_taken_year=$2)
) `

var reports = []reportSpec{
	{"lens-stats", "Photo counts by year, camera, and lens", `SELECT year, camera_model, lens_model, count(*) AS photos FROM scoped GROUP BY 1,2,3 ORDER BY year NULLS LAST, photos DESC, camera_model COLLATE "C" NULLS LAST, lens_model COLLATE "C" NULLS LAST`},
	{"aperture-distribution", "Exact recorded f-number counts per camera", `SELECT camera_model, f_number, count(*) AS photos FROM scoped GROUP BY 1,2 ORDER BY camera_model COLLATE "C" NULLS LAST, f_number NULLS LAST`},
	{"coverage-by-camera-month", "Photo counts by camera, year, and month", `SELECT camera_model, year, month, count(*) AS photos FROM scoped GROUP BY 1,2,3 ORDER BY camera_model COLLATE "C" NULLS LAST, year NULLS LAST, month NULLS LAST`},
	{"pov-breakdown", "Classifier viewpoints per camera, including unclassified photos", `SELECT s.camera_model, c.pov_container, count(*) AS photos FROM scoped s LEFT JOIN classified c ON c.photo_id=s.id GROUP BY 1,2 ORDER BY s.camera_model COLLATE "C" NULLS LAST, photos DESC, c.pov_container COLLATE "C" NULLS LAST`},
	{"vocabulary", "Search lexemes ranked by number of photos containing each term", `SELECT term, count(*) AS photos FROM scoped s LEFT JOIN descriptions d ON d.photo_id=s.id LEFT JOIN exif e ON e.photo_id=s.id CROSS JOIN LATERAL unnest(tsvector_to_array(coalesce(d.fts,''::tsvector) || coalesce(e.fts,''::tsvector))) AS terms(term) GROUP BY term ORDER BY photos DESC, term COLLATE "C"`},
	{"classifier-review", "Missing, stale, or uncertain classifications (review signals, not semantic verdicts)", `SELECT s.id AS photo_id, s.name, s.camera_model, issues.reason FROM scoped s LEFT JOIN classified c ON c.photo_id=s.id LEFT JOIN inference i ON i.photo_id=s.id CROSS JOIN LATERAL (
 SELECT 'not_classified' AS reason WHERE c.photo_id IS NULL
 UNION ALL SELECT 'description_newer_than_classification' WHERE c.classified_at < i.described_at
 UNION ALL SELECT 'uncertain_or_missing_fields' WHERE c.photo_id IS NOT NULL AND EXISTS (
 SELECT 1 FROM jsonb_each(to_jsonb(c) - ARRAY['photo_id','classified_at','classifier_model','extras']) f
 WHERE f.value IN ('null'::jsonb, '"unclear"'::jsonb, '""'::jsonb, '[]'::jsonb) OR (jsonb_typeof(f.value)='array' AND f.value @> '["unclear"]'::jsonb))
 ) issues ORDER BY s.name COLLATE "C", s.id COLLATE "C", issues.reason`},
}

type report struct {
	Name      string           `json:"report"`
	Camera    string           `json:"camera,omitempty"`
	Year      int              `json:"year,omitempty"`
	Limit     int              `json:"limit"`
	Truncated bool             `json:"truncated"`
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, stderr io.Writer) error {
	usage := func() {
		fmt.Fprintln(stderr, "Usage: analyze <report> [flags]\nReports:")
		for _, r := range reports {
			fmt.Fprintf(stderr, "  %-26s %s\n", r.name, r.description)
		}
	}
	if len(args) == 0 {
		usage()
		return errors.New("report required")
	}
	if args[0] == "-h" || args[0] == "--help" {
		usage()
		return nil
	}
	var spec *reportSpec
	for i := range reports {
		if reports[i].name == args[0] {
			spec = &reports[i]
			break
		}
	}
	if spec == nil {
		usage()
		return fmt.Errorf("unknown report %q", args[0])
	}
	fs := flag.NewFlagSet("analyze "+spec.name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dsn := fs.String("dsn", library.DefaultDSN(), "Postgres library DSN (or LIBRARY_DSN)")
	format := fs.String("format", "markdown", "markdown or json")
	camera := fs.String("camera", "", "Exact EXIF camera model (all cameras when empty)")
	year := fs.Int("year", 0, "EXIF year (0 = all, including unknown)")
	limit := fs.Int("limit", 0, "Maximum report rows (0 = all); counts are computed before limiting")
	timeout := fs.Duration("timeout", time.Minute, "Database query timeout")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments; flags follow the report name")
	}
	if *format != "markdown" && *format != "json" {
		return errors.New("format must be markdown or json")
	}
	if *year < 0 || *year > 9999 || *limit < 0 || *limit == int(^uint(0)>>1) || *timeout <= 0 {
		return errors.New("invalid year, limit, or timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := collect(ctx, db, *spec, *camera, *year, *limit)
	if err != nil {
		return err
	}
	return render(out, *format, result)
}

func collect(ctx context.Context, db *sql.DB, spec reportSpec, camera string, year, limit int) (report, error) {
	r := report{Name: spec.name, Camera: camera, Year: year, Limit: limit, Rows: []map[string]any{}}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	query := scope + spec.query
	args := []any{camera, year}
	if limit > 0 {
		query += " LIMIT $3"
		args = append(args, limit+1)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	r.Columns, err = rows.Columns()
	if err != nil {
		return r, err
	}
	for rows.Next() {
		values := make([]any, len(r.Columns))
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return r, err
		}
		if limit > 0 && len(r.Rows) == limit {
			r.Truncated = true
			break
		}
		row := make(map[string]any, len(values))
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			row[r.Columns[i]] = v
		}
		r.Rows = append(r.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return r, err
	}
	if err := rows.Close(); err != nil {
		return r, err
	}
	return r, tx.Commit()
}

func render(out io.Writer, format string, r report) error {
	if format == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", r.Name)
	if r.Camera != "" {
		fmt.Fprintf(&b, "Camera: %s\n\n", cell(r.Camera))
	}
	if r.Year != 0 {
		fmt.Fprintf(&b, "Year: %d\n\n", r.Year)
	}
	b.WriteString("Missing values are shown as —.\n\n")
	fmt.Fprintf(&b, "| %s |\n", strings.Join(r.Columns, " | "))
	for range r.Columns {
		b.WriteString("| --- ")
	}
	b.WriteString("|\n")
	for _, row := range r.Rows {
		for _, col := range r.Columns {
			fmt.Fprintf(&b, "| %s ", cell(row[col]))
		}
		b.WriteString("|\n")
	}
	fmt.Fprintf(&b, "\n%d rows", len(r.Rows))
	if r.Truncated {
		b.WriteString(" (truncated; increase -limit or use -limit=0)")
	}
	b.WriteString(".\n")
	_, err := io.WriteString(out, b.String())
	return err
}

func cell(v any) string {
	if v == nil {
		return "—"
	}
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\\", "&#92;", "|", "&#124;", "`", "&#96;", "*", "&#42;", "_", "&#95;", "[", "&#91;", "]", "&#93;", "\r\n", "<br>", "\n", "<br>", "\r", "<br>").Replace(fmt.Sprint(v))
}
