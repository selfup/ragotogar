package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"ragotogar/library/testdb"
	"strings"
	"testing"
)

func TestReports(t *testing.T) {
	db := testdb.New(t, "analyze", testdb.SchemaSQL(`
 CREATE TABLE photos(id text PRIMARY KEY, name text);
 CREATE TABLE exif(photo_id text PRIMARY KEY, camera_model text, lens_model text, date_taken_year int, date_taken_month int, f_number double precision, fts tsvector);
 CREATE TABLE descriptions(photo_id text PRIMARY KEY, fts tsvector);
 CREATE TABLE classified(photo_id text PRIMARY KEY, pov_container text, framing text[], classified_at timestamptz, classifier_model text, extras jsonb);
 CREATE TABLE inference(photo_id text PRIMARY KEY, described_at timestamptz);
 INSERT INTO photos VALUES ('1','one'),('2','two'),('3','unknown');
 INSERT INTO exif VALUES ('1','NIKON Z 8','zoom',2024,4,2.8,to_tsvector('english','nikon airplane')),('2','NIKON Z 8','zoom',2024,5,2.8,to_tsvector('english','nikon'));
 INSERT INTO descriptions VALUES ('1',to_tsvector('english','airplane airplane sky')),('2',to_tsvector('english','sky'));
 INSERT INTO classified VALUES ('1','handheld',ARRAY['unobstructed'],'2024-01-01','model',NULL),('2','unclear',ARRAY['unclear'],'2024-01-03','model',NULL);
 INSERT INTO inference VALUES ('1','2024-01-02'),('2','2024-01-02');
 `))
	ctx := context.Background()
	for _, spec := range reports {
		t.Run(spec.name, func(t *testing.T) {
			r, err := collect(ctx, db, spec, "", 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Rows) == 0 {
				t.Fatal("empty report")
			}
			switch spec.name {
			case "lens-stats", "aperture-distribution":
				var known, unknown int64
				for _, row := range r.Rows {
					if row["camera_model"] == nil {
						unknown = row["photos"].(int64)
					} else {
						known = row["photos"].(int64)
					}
				}
				if known != 2 || unknown != 1 {
					t.Fatalf("lost or duplicated photos: %+v", r.Rows)
				}
			case "coverage-by-camera-month":
				if len(r.Rows) != 3 {
					t.Fatal(r.Rows)
				}
			case "pov-breakdown":
				if len(r.Rows) != 3 {
					t.Fatal(r.Rows)
				}
			case "vocabulary":
				counts := map[string]int64{}
				for _, row := range r.Rows {
					counts[row["term"].(string)] = row["photos"].(int64)
				}
				if counts["airplan"] != 1 || counts["sky"] != 2 || counts["nikon"] != 2 {
					t.Fatalf("must count each photo once per term: %v", counts)
				}
			case "classifier-review":
				reasons := map[string]string{}
				for _, row := range r.Rows {
					reasons[row["photo_id"].(string)] = row["reason"].(string)
				}
				if len(r.Rows) != 3 || reasons["1"] != "description_newer_than_classification" || reasons["2"] != "uncertain_or_missing_fields" || reasons["3"] != "not_classified" {
					t.Fatal(r.Rows)
				}
			}
			limited, err := collect(ctx, db, spec, "", 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(limited.Rows) != 1 || !limited.Truncated {
				t.Fatal(limited)
			}
			empty, err := collect(ctx, db, spec, "NIKON Z 8' OR true --", 2024, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(empty.Rows) != 0 {
				t.Fatal("camera filter is not exact", empty)
			}
			empty, err = collect(ctx, db, spec, "", 2025, 0)
			if err != nil || len(empty.Rows) != 0 {
				t.Fatal(empty, err)
			}
		})
	}
	filtered, err := collect(ctx, db, reports[0], "NIKON Z 8", 2024, 1)
	if err != nil || len(filtered.Rows) != 1 || filtered.Rows[0]["photos"] != int64(2) || filtered.Truncated {
		t.Fatal(filtered, err)
	}
	if _, err := db.Exec(`TRUNCATE photos,exif,descriptions,classified,inference`); err != nil {
		t.Fatal(err)
	}
	for _, spec := range reports {
		r, err := collect(ctx, db, spec, "", 0, 0)
		if err != nil || len(r.Rows) != 0 || r.Rows == nil {
			t.Fatal(r, err)
		}
	}
}

func TestRender(t *testing.T) {
	r := report{Name: "example", Columns: []string{"lens", "photos"}, Rows: []map[string]any{{"lens": "a|<b>\nx", "photos": int64(2)}, {"lens": nil, "photos": int64(1)}}, Truncated: true}
	var b bytes.Buffer
	if err := render(&b, "markdown", r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a&#124;&lt;b&gt;<br>x", "| — | 1 |", "truncated"} {
		if !strings.Contains(b.String(), want) {
			t.Fatal(b.String())
		}
	}
	b.Reset()
	if err := render(&b, "json", r); err != nil {
		t.Fatal(err)
	}
	var got report
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Rows[1]["lens"] != nil || got.Rows[0]["photos"] != float64(2) {
		t.Fatal(got)
	}
}

func TestCLIValidation(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}, {"lens-stats", "-format=csv"}, {"lens-stats", "-year=-1"}, {"lens-stats", "-limit=-1"}, {"lens-stats", "-timeout=0"}, {"lens-stats", "unexpected"}} {
		if err := run(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, args := range [][]string{{"-h"}, {"lens-stats", "-h"}} {
		if err := run(context.Background(), args, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
}
