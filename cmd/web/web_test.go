package main

import (
	"reflect"
	"testing"
)

func TestResolveMode(t *testing.T) {
	tests := []struct{ in, want string }{
		{"naive", "naive"},
		{"local", "local"},
		{"hybrid", "hybrid"},
		{"", "naive"},        // empty falls back to default
		{"global", "naive"},  // global is rejected (synthesizes summaries, useless for grid)
		{"NAIVE", "naive"},   // case-sensitive — argparse on the python side is too
		{"garbage", "naive"}, // unknown rejected
	}
	for _, tt := range tests {
		if got := resolveMode(tt.in); got != tt.want {
			t.Errorf("resolveMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseSearchOutput(t *testing.T) {
	t.Run("typical retrieve output", func(t *testing.T) {
		// shape mirrors tools/search.py print_sources()
		out := `
--- Retrieved Sources (3 files) ---
  [1] /Volumes/T9/X100VI/JPEG/March/descriptions/20260321_X100VI_DSCF1601.json
  [2] /Volumes/T9/X100VI/JPEG/March/descriptions/20260321_X100VI_DSCF1602.json
  [3] /Volumes/T9/X100VI/JPEG/March/descriptions/20260321_X100VI_DSCF1603.json
`
		got := parseSearchOutput(out)
		want := []string{
			"20260321_X100VI_DSCF1601",
			"20260321_X100VI_DSCF1602",
			"20260321_X100VI_DSCF1603",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("preserves retrieval order", func(t *testing.T) {
		out := `
  [1] /a/zebra.json
  [2] /a/alpha.json
  [3] /a/middle.json
`
		got := parseSearchOutput(out)
		want := []string{"zebra", "alpha", "middle"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("order changed: got %v, want %v", got, want)
		}
	})

	t.Run("deduplicates repeated paths", func(t *testing.T) {
		out := `
  [1] /a/photo.json
  [2] /a/other.json
  [3] /a/photo.json
`
		got := parseSearchOutput(out)
		want := []string{"photo", "other"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("ignores non-matching lines", func(t *testing.T) {
		out := `
INFO: starting LightRAG query
--- Retrieved Sources (2 files) ---
  [1] /a/photo1.json
some random log line
  [2] /a/photo2.json
INFO: finalizing storages
`
		got := parseSearchOutput(out)
		want := []string{"photo1", "photo2"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		got := parseSearchOutput("")
		if len(got) != 0 {
			t.Errorf("expected empty, got %v", got)
		}
	})

	t.Run("no sources header", func(t *testing.T) {
		got := parseSearchOutput("query returned no results\n")
		if len(got) != 0 {
			t.Errorf("expected empty, got %v", got)
		}
	})

	t.Run("strips arbitrary extension", func(t *testing.T) {
		out := `  [1] /a/foo.bar.json`
		got := parseSearchOutput(out)
		want := []string{"foo.bar"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("handles double-digit numbering", func(t *testing.T) {
		out := `
  [1]  /a/p1.json
  [9]  /a/p9.json
  [10] /a/p10.json
  [99] /a/p99.json
`
		got := parseSearchOutput(out)
		want := []string{"p1", "p9", "p10", "p99"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}
