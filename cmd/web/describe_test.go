package main

import (
	"html/template"
	"strings"
	"testing"
)

func TestParseCount(t *testing.T) {
	cases := []struct {
		raw                string
		fallback, min, max int
		want               int
	}{
		{"", 4, 1, 64, 4},        // empty → fallback
		{"8", 4, 1, 64, 8},       // typical value
		{"1", 4, 1, 64, 1},       // min is valid
		{"64", 4, 1, 64, 64},     // max is valid
		{"0", 4, 1, 64, 1},       // below min clamps up
		{"-3", 4, 1, 64, 1},      // negative clamps up
		{"999", 4, 1, 64, 64},    // above max clamps down
		{"garbage", 4, 1, 64, 4}, // bad parse → fallback
		{"2.5", 4, 1, 64, 4},     // float is not an int → fallback
		{"0", 3, 0, 10, 0},       // min 0 allows zero (retries)
	}
	for _, tc := range cases {
		if got := parseCount(tc.raw, tc.fallback, tc.min, tc.max); got != tc.want {
			t.Errorf("parseCount(%q, %d, %d, %d) = %d, want %d",
				tc.raw, tc.fallback, tc.min, tc.max, got, tc.want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "''"},
		{"/Volumes/T9/X100VI/JPEG", "/Volumes/T9/X100VI/JPEG"},
		{"qwen/qwen3-vl-8b", "qwen/qwen3-vl-8b"},
		{"/Volumes/My Drive/JPEG", "'/Volumes/My Drive/JPEG'"},
		{"~/Z8/RAW/", "~/Z8/RAW/"},         // leading ~/ stays bare so the shell expands it
		{"~/My Drive/x", "~/'My Drive/x'"}, // ~/ bare, the rest quoted
		{"x~y", "'x~y'"},                   // non-leading ~ still quoted (harmless)
		{"April2026(edit)", "'April2026(edit)'"},
		{"it's", `'it'\''s'`},
		{"a$b", "'a$b'"},
		{"a\nb", "'a\nb'"},
	}
	for _, tc := range cases {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildDescribeCommand(t *testing.T) {
	base := describeParams{
		dir:              "/photos/April2026",
		model:            "qwen/qwen3-vl-8b",
		classifyModel:    "mistralai/ministral-3-3b",
		previewWorkers:   4,
		inferenceWorkers: 1,
		retries:          3,
	}

	t.Run("defaults", func(t *testing.T) {
		got := buildDescribeCommand(base)
		want := "./scripts/photo_describe.sh -model qwen/qwen3-vl-8b" +
			" -preview-workers 4 -inference-workers 1 -retries 3 -- /photos/April2026"
		if got != want {
			t.Errorf("got  %q\nwant %q", got, want)
		}
	})

	t.Run("all toggles + classify model", func(t *testing.T) {
		p := base
		p.force, p.dryRun, p.classify = true, true, true
		p.inferenceWorkers = 4
		got := buildDescribeCommand(p)
		want := "./scripts/photo_describe.sh -force -dry-run -model qwen/qwen3-vl-8b" +
			" -preview-workers 4 -inference-workers 4 -retries 3" +
			" -classify -classify-model mistralai/ministral-3-3b -- /photos/April2026"
		if got != want {
			t.Errorf("got  %q\nwant %q", got, want)
		}
	})

	t.Run("classify model omitted unless classify is on", func(t *testing.T) {
		got := buildDescribeCommand(base)
		if strings.Contains(got, "-classify") {
			t.Errorf("classify off but command mentions -classify: %q", got)
		}
	})

	t.Run("path with spaces is quoted", func(t *testing.T) {
		p := base
		p.dir = "/Volumes/My Drive/JPEG/April2026"
		got := buildDescribeCommand(p)
		if !strings.HasSuffix(got, "'/Volumes/My Drive/JPEG/April2026'") {
			t.Errorf("dir not shell-quoted: %q", got)
		}
	})
}

// Template smoke tests. main() parses these with template.Must, but
// only at process start — a field rename or syntax slip would otherwise
// not surface in ./test.sh. Execute with representative data and assert
// the load-bearing bits render.

func TestDescribeTemplateRenders(t *testing.T) {
	tmpl := template.Must(template.Must(template.New("describe").Parse(sidebarTmpl)).Parse(describeHTML))
	var b strings.Builder
	err := tmpl.Execute(&b, describePageData{
		Active:           "describe",
		Dir:              "/photos/April2026",
		Model:            "qwen/qwen3-vl-8b",
		ClassifyModel:    "mistralai/ministral-3-3b",
		PreviewWorkers:   "4",
		InferenceWorkers: "2",
		Retries:          "3",
		Force:            true,
		DSN:              "postgres:///ragotogar",
		Command:          "./scripts/photo_describe.sh -force /photos/April2026",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := b.String()
	for _, want := range []string{
		`class="sidebar-item active" href="/describe"`, // nav highlights this section
		`href="/"`, // search reachable from sidebar
		"./scripts/photo_describe.sh -force /photos/April2026", // command preview shown
		`value="/photos/April2026"`,                            // form state round-trips
		`name="force" value="1" checked`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
}

func TestDescribeTemplateHidesCommandUntilSubmitted(t *testing.T) {
	tmpl := template.Must(template.Must(template.New("describe").Parse(sidebarTmpl)).Parse(describeHTML))
	var b strings.Builder
	if err := tmpl.Execute(&b, describePageData{Active: "describe", Model: "m", ClassifyModel: "c",
		PreviewWorkers: "4", InferenceWorkers: "1", Retries: "3", DSN: "postgres:///x"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(b.String(), `<pre class="cmd">`) {
		t.Error("command panel rendered with no submitted directory")
	}
	if strings.Contains(b.String(), `role="alert"`) {
		t.Error("error note rendered on first load (nothing submitted yet)")
	}
}

func TestDescribeTemplateShowsError(t *testing.T) {
	tmpl := template.Must(template.Must(template.New("describe").Parse(sidebarTmpl)).Parse(describeHTML))
	var b strings.Builder
	if err := tmpl.Execute(&b, describePageData{Active: "describe", Model: "m", ClassifyModel: "c",
		PreviewWorkers: "4", InferenceWorkers: "1", Retries: "3", DSN: "postgres:///x",
		Error: "enter a photo directory or image file"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(b.String(), "enter a photo directory or image file") {
		t.Error("submitted-without-dir page missing the explanatory note")
	}
}

func TestIndexTemplateRendersWithSidebar(t *testing.T) {
	tmpl := template.Must(template.Must(template.New("index").Parse(sidebarTmpl)).Parse(indexHTML))
	var b strings.Builder
	err := tmpl.Execute(&b, pageData{
		Active:          "search",
		Q:               Q("red truck"),
		Mode:            "naive",
		Sort:            "relevance",
		Merge:           "union",
		CosineThreshold: "0.500",
		FTSThresholdRel: "0.300",
		Results:         []result{{Name: "DSC00596"}},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := b.String()
	for _, want := range []string{
		`class="sidebar-item active" href="/"`, // nav highlights search
		`href="/describe"`,                     // describe reachable from sidebar
		`/photos/DSC00596`,                     // results grid still renders
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
}
