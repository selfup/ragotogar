package main

import (
	"strings"
	"testing"
)

// ── parse.go ──────────────────────────────────────────────────────────────

func TestEsc(t *testing.T) {
	tests := []struct{ in, want string }{
		{"hello", "hello"},
		{"a & b", "a &amp; b"},
		{"<div>", "&lt;div&gt;"},
		{"<a>x</a>", "&lt;a&gt;x&lt;/a&gt;"},
		{"a < b > c", "a &lt; b &gt; c"},
	}
	for _, tt := range tests {
		if got := esc(tt.in); got != tt.want {
			t.Errorf("esc(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestInline(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "hello world", "hello world"},
		{"escape", "a & b", "a &amp; b"},
		{"backtick", "`code`", `<code class="code-inline">code</code>`},
		{"bold", "**bold**", "<strong>bold</strong>"},
		{"italic", "*italic*", "<em>italic</em>"},
		{"link", "[text](https://example.com)", `<a href="https://example.com">text</a>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inline(tt.in); got != tt.want {
				t.Errorf("inline(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseBlocks(t *testing.T) {
	t.Run("paragraph", func(t *testing.T) {
		blocks := parseBlocks("hello world")
		if len(blocks) != 1 {
			t.Fatalf("got %d blocks, want 1", len(blocks))
		}
		if blocks[0].Type != "p" || blocks[0].Text != "hello world" {
			t.Errorf("got %+v", blocks[0])
		}
	})

	t.Run("heading level 2", func(t *testing.T) {
		blocks := parseBlocks("## My Heading")
		if len(blocks) != 1 {
			t.Fatalf("got %d blocks, want 1", len(blocks))
		}
		b := blocks[0]
		if b.Type != "h" || b.Level != 2 || b.Text != "My Heading" {
			t.Errorf("got %+v", b)
		}
	})

	t.Run("code fence with lang", func(t *testing.T) {
		blocks := parseBlocks("```go\nfmt.Println()\n```")
		if len(blocks) != 1 {
			t.Fatalf("got %d blocks, want 1", len(blocks))
		}
		b := blocks[0]
		if b.Type != "code" || b.Lang != "go" || b.Body != "fmt.Println()" {
			t.Errorf("got %+v", b)
		}
	})

	t.Run("code fence with lang and file", func(t *testing.T) {
		blocks := parseBlocks("```rust main.rs\nfn main() {}\n```")
		if len(blocks) != 1 {
			t.Fatalf("got %d blocks, want 1", len(blocks))
		}
		b := blocks[0]
		if b.Type != "code" || b.Lang != "rust" || b.File != "main.rs" {
			t.Errorf("got %+v", b)
		}
	})

	t.Run("blockquote", func(t *testing.T) {
		blocks := parseBlocks("> quoted text")
		if len(blocks) != 1 || blocks[0].Type != "quote" || blocks[0].Text != "quoted text" {
			t.Errorf("got %+v", blocks)
		}
	})

	t.Run("unordered list dash", func(t *testing.T) {
		blocks := parseBlocks("- one\n- two\n- three")
		if len(blocks) != 1 || blocks[0].Type != "ul" || len(blocks[0].Items) != 3 {
			t.Errorf("got %+v", blocks)
		}
		if blocks[0].Items[0] != "one" {
			t.Errorf("first item = %q, want %q", blocks[0].Items[0], "one")
		}
	})

	t.Run("ordered list", func(t *testing.T) {
		blocks := parseBlocks("1. first\n2. second")
		if len(blocks) != 1 || blocks[0].Type != "ol" || len(blocks[0].Items) != 2 {
			t.Errorf("got %+v", blocks)
		}
		if blocks[0].Items[1] != "second" {
			t.Errorf("second item = %q, want %q", blocks[0].Items[1], "second")
		}
	})

	t.Run("multiple blocks", func(t *testing.T) {
		md := "## Title\n\nA paragraph.\n\n- item one\n- item two"
		blocks := parseBlocks(md)
		if len(blocks) != 3 {
			t.Fatalf("got %d blocks, want 3", len(blocks))
		}
		if blocks[0].Type != "h" || blocks[1].Type != "p" || blocks[2].Type != "ul" {
			t.Errorf("types = %s %s %s", blocks[0].Type, blocks[1].Type, blocks[2].Type)
		}
	})

	t.Run("blank lines skipped", func(t *testing.T) {
		blocks := parseBlocks("\n\nhello\n\n")
		if len(blocks) != 1 || blocks[0].Type != "p" {
			t.Errorf("got %+v", blocks)
		}
	})
}

func TestParseSections(t *testing.T) {
	t.Run("no sections returns empty pre", func(t *testing.T) {
		// pre is only populated when at least one section follows it
		pre, secs := parseSections("just some text")
		if pre != "" || len(secs) != 0 {
			t.Errorf("pre=%q secs=%v", pre, secs)
		}
	})

	t.Run("single section no props", func(t *testing.T) {
		md := ":::prose\nhello world\n:::"
		pre, secs := parseSections(md)
		if pre != "" {
			t.Errorf("pre = %q, want empty", pre)
		}
		if len(secs) != 1 {
			t.Fatalf("got %d sections, want 1", len(secs))
		}
		if secs[0].Type != "prose" || secs[0].Body != "hello world" {
			t.Errorf("got %+v", secs[0])
		}
	})

	t.Run("section with json props", func(t *testing.T) {
		md := ":::built {\"num\":\"III.\",\"label\":\"Requirements\"}\n# Heading\n:::"
		_, secs := parseSections(md)
		if len(secs) != 1 {
			t.Fatalf("got %d sections", len(secs))
		}
		if secs[0].Type != "built" {
			t.Errorf("type = %q, want built", secs[0].Type)
		}
		if secs[0].Props["num"] != "III." {
			t.Errorf("num = %v, want III.", secs[0].Props["num"])
		}
		if secs[0].Props["label"] != "Requirements" {
			t.Errorf("label = %v", secs[0].Props["label"])
		}
	})

	t.Run("pre content before first section", func(t *testing.T) {
		md := "preamble text\n\n:::prose\nbody\n:::"
		pre, secs := parseSections(md)
		if pre != "preamble text" {
			t.Errorf("pre = %q", pre)
		}
		if len(secs) != 1 || secs[0].Body != "body" {
			t.Errorf("secs = %+v", secs)
		}
	})

	t.Run("multiple sections", func(t *testing.T) {
		md := ":::prose\nfirst\n:::\n\n:::close\nsecond\n:::"
		_, secs := parseSections(md)
		if len(secs) != 2 {
			t.Fatalf("got %d sections, want 2", len(secs))
		}
		if secs[0].Type != "prose" || secs[1].Type != "close" {
			t.Errorf("types = %s, %s", secs[0].Type, secs[1].Type)
		}
		if secs[0].Body != "first" || secs[1].Body != "second" {
			t.Errorf("bodies = %q, %q", secs[0].Body, secs[1].Body)
		}
	})
}

// ── photo.go ──────────────────────────────────────────────────────────────

func TestFormatDate(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2025:09:28 16:38:17", "28 September 2025"},
		{"2026:01:01 00:00:00", "1 January 2026"},
		{"2026:04:05 08:00:00", "5 April 2026"},
		{"2024:12:31 23:59:59", "31 December 2024"},
		{"2023:07:04 12:00:00", "4 July 2023"},
		{"", ""},
		{"not-a-date", "not-a-date"},
	}
	for _, tt := range tests {
		if got := formatDate(tt.in); got != tt.want {
			t.Errorf("formatDate(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStripBullets(t *testing.T) {
	tests := []struct{ in, want string }{
		{"- item one", "item one"},
		{"* item one", "item one"},
		{"- one\n- two", "one two"},
		{"plain text", "plain text"},
		{"- first\n* second\n- third", "first second third"},
	}
	for _, tt := range tests {
		if got := stripBullets(tt.in); got != tt.want {
			t.Errorf("stripBullets(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFirstSentence(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Hello world. More text.", "Hello world."},
		{"Exclamation! And more.", "Exclamation!"},
		{"A question? Yes.", "A question?"},
		{"No sentence end", "No sentence end"},
		{"- A bullet sentence. More.", "A bullet sentence."},
	}
	for _, tt := range tests {
		if got := firstSentence(tt.in); got != tt.want {
			t.Errorf("firstSentence(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildMarkdownContains(t *testing.T) {
	data := PhotoData{
		Name:        "20260417_X100VI_DSCF1781",
		File:        "DSCF1781.JPG",
		Path:        "/photos/DSCF1781.JPG",
		Preview:     "180ms",
		PreviewMs:   180,
		Inference:   "24.252s",
		InferenceMs: 24252,
		Metadata: exifData{
			FileName:         "DSCF1781.JPG",
			DateTimeOriginal: "2026:04:17 10:30:00",
			Make:             "FUJIFILM",
			Model:            "X100VI",
			FocalLength:      "23.0 mm",
			FNumber:          "2",
			ExposureTime:     "1/250",
			ISO:              "200",
			ExposureMode:     "Manual",
			MeteringMode:     "Spot",
			WhiteBalance:     "Auto",
			Flash:            "No Flash",
			ImageWidth:       "6240",
			ImageHeight:      "4160",
		},
		Fields: photoFields{
			Subject:     "A street scene with people.",
			Setting:     "Urban environment.",
			Light:       "Bright daylight.",
			Colors:      "Blues and greys.",
			Composition: "Rule of thirds.",
		},
	}
	md := buildMarkdown(data)

	checks := []string{
		"DSCF1781",               // file stem in title
		"FUJIFILM X100VI",        // camera
		"17 April 2026",          // formatted date
		"file_name: DSCF1781.JPG", // metadata entry
		"iso: 200",               // metadata entry
		"A street scene",         // subject field
		"Urban environment.",     // setting field (first sentence)
	}
	for _, want := range checks {
		if !strings.Contains(md, want) {
			t.Errorf("buildMarkdown output missing %q", want)
		}
	}
}

func TestBuildMarkdownOmitsEmptyFields(t *testing.T) {
	data := PhotoData{
		File: "TEST.JPG",
		Metadata: exifData{
			FileName: "TEST.JPG",
			// Artist, Software, Copyright all empty
		},
	}
	md := buildMarkdown(data)
	for _, absent := range []string{"artist:", "software:", "copyright:"} {
		if strings.Contains(md, absent) {
			t.Errorf("buildMarkdown should omit empty %q field", absent)
		}
	}
}

// ── render.go ─────────────────────────────────────────────────────────────

func TestSplitByRule(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"chunk one\n---\nchunk two", []string{"chunk one", "chunk two"}},
		{"a\n---\nb\n---\nc", []string{"a", "b", "c"}},
		{"no rule here", []string{"no rule here"}},
		{"  ---  \n", []string{}},
	}
	for _, tt := range tests {
		got := splitByRule(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("splitByRule(%q): got %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitByRule(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

func TestSplitHeading(t *testing.T) {
	t.Run("heading first", func(t *testing.T) {
		h, rest := splitHeading("# My Title\n\nsome body text")
		if h != "My Title" {
			t.Errorf("heading = %q, want %q", h, "My Title")
		}
		if !strings.Contains(rest, "some body text") {
			t.Errorf("rest = %q should contain body", rest)
		}
	})

	t.Run("heading level 2", func(t *testing.T) {
		h, _ := splitHeading("## Section Title\nbody")
		if h != "Section Title" {
			t.Errorf("heading = %q, want %q", h, "Section Title")
		}
	})

	t.Run("no heading — returns empty heading and original body", func(t *testing.T) {
		h, rest := splitHeading("just text\nmore text")
		if h != "" {
			t.Errorf("heading = %q, want empty", h)
		}
		if !strings.Contains(rest, "just text") {
			t.Errorf("rest = %q", rest)
		}
	})
}

func TestRenderBody(t *testing.T) {
	t.Run("paragraph no class", func(t *testing.T) {
		got := renderBody("hello world", "")
		if !strings.Contains(got, "<p>hello world</p>") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("paragraph with class", func(t *testing.T) {
		got := renderBody("hello", "body")
		if !strings.Contains(got, `<p class="body">hello</p>`) {
			t.Errorf("got %q", got)
		}
	})

	t.Run("heading", func(t *testing.T) {
		got := renderBody("## Title", "")
		if !strings.Contains(got, "<h2>Title</h2>") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("unordered list", func(t *testing.T) {
		got := renderBody("- alpha\n- beta", "")
		if !strings.Contains(got, "<ul>") || !strings.Contains(got, "<li>alpha</li>") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("ordered list", func(t *testing.T) {
		got := renderBody("1. first\n2. second", "")
		if !strings.Contains(got, "<ol>") || !strings.Contains(got, "<li>first</li>") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("blockquote", func(t *testing.T) {
		got := renderBody("> quote text", "")
		if !strings.Contains(got, "<blockquote>quote text</blockquote>") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("inline escaping", func(t *testing.T) {
		got := renderBody("a & b", "")
		if !strings.Contains(got, "a &amp; b") {
			t.Errorf("got %q", got)
		}
	})
}

func TestHighlightRust(t *testing.T) {
	t.Run("keyword fn", func(t *testing.T) {
		got := highlightRust("fn main")
		if !strings.Contains(got, `<span class="tok-key">fn</span>`) {
			t.Errorf("got %q", got)
		}
	})

	t.Run("keyword let", func(t *testing.T) {
		got := highlightRust("let x")
		if !strings.Contains(got, `<span class="tok-key">let</span>`) {
			t.Errorf("got %q", got)
		}
	})

	t.Run("line comment", func(t *testing.T) {
		got := highlightRust("// a comment")
		if !strings.Contains(got, `<span class="tok-com">// a comment</span>`) {
			t.Errorf("got %q", got)
		}
	})

	t.Run("string literal", func(t *testing.T) {
		got := highlightRust(`"hello"`)
		if !strings.Contains(got, `<span class="tok-str">"hello"</span>`) {
			t.Errorf("got %q", got)
		}
	})

	t.Run("integer literal", func(t *testing.T) {
		got := highlightRust("42")
		if !strings.Contains(got, `<span class="tok-num">42</span>`) {
			t.Errorf("got %q", got)
		}
	})

	t.Run("attribute", func(t *testing.T) {
		got := highlightRust("#[derive(Debug)]")
		if !strings.Contains(got, `<span class="tok-attr">#[derive(Debug)]</span>`) {
			t.Errorf("got %q", got)
		}
	})

	t.Run("macro call tok-fn", func(t *testing.T) {
		got := highlightRust("println!()")
		if !strings.Contains(got, `<span class="tok-fn">println</span>`) {
			t.Errorf("got %q", got)
		}
	})

	t.Run("escapes html chars", func(t *testing.T) {
		got := highlightRust("a < b")
		if strings.Contains(got, "<b") {
			t.Errorf("unescaped < in output: %q", got)
		}
		if !strings.Contains(got, "&lt;") {
			t.Errorf("expected &lt;: %q", got)
		}
	})
}

func TestRenderPhotoMeta(t *testing.T) {
	sec := Section{
		Type:  "photo-meta",
		Props: map[string]any{"num": "III.", "label": "Camera Settings"},
		Body: `# All metadata for this frame:

1. file_name: TEST.JPG
2. iso: 3200
3. f_number: f/2

---

*Captured at 10:30.*`,
	}
	got := renderPhotoMeta(sec)

	if !strings.Contains(got, "photo-meta") {
		t.Errorf("missing photo-meta class: %q", got)
	}
	// key goes into .num div, value into .text div
	if !strings.Contains(got, "file_name") {
		t.Errorf("missing file_name key: %q", got)
	}
	if !strings.Contains(got, "TEST.JPG") {
		t.Errorf("missing TEST.JPG value: %q", got)
	}
	if !strings.Contains(got, "iso") {
		t.Errorf("missing iso key: %q", got)
	}
	if !strings.Contains(got, "3200") {
		t.Errorf("missing 3200 value: %q", got)
	}
}

func TestRenderClose(t *testing.T) {
	sec := Section{
		Type:  "close",
		Props: map[string]any{"num": "IV.", "label": "In Closing", "mark": "selfup", "meta": "2026 · X100VI"},
		Body:  "A great quote.\n\n---\n\nThe post text.",
	}
	got := renderClose(sec)

	if !strings.Contains(got, `class="close"`) {
		t.Errorf("missing close class: %q", got)
	}
	if !strings.Contains(got, "A great quote.") {
		t.Errorf("missing quote: %q", got)
	}
	if !strings.Contains(got, "The post text.") {
		t.Errorf("missing post: %q", got)
	}
	if !strings.Contains(got, "selfup") {
		t.Errorf("missing mark: %q", got)
	}
	if !strings.Contains(got, "2026") {
		t.Errorf("missing meta: %q", got)
	}
}

func TestRenderSection_KnownTypes(t *testing.T) {
	tests := []struct {
		name    string
		sec     Section
		contain string
	}{
		{
			"prose",
			Section{Type: "prose", Props: map[string]any{}, Body: "# Title\n\nBody text."},
			`class="prose"`,
		},
		{
			"close",
			Section{Type: "close", Props: map[string]any{}, Body: "quote text"},
			`class="close"`,
		},
		{
			"built",
			Section{Type: "built", Props: map[string]any{}, Body: "# Title\n\n1. item one"},
			`class="built"`,
		},
		{
			"dual-pillars",
			Section{Type: "dual-pillars", Props: map[string]any{}, Body: "# Title\n\n---\n\n# Left\n- a — b\n\n---\n\n# Right\n- c — d"},
			`class="dual-pillars"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renderSection(tt.sec)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(got, tt.contain) {
				t.Errorf("renderSection(%q) missing %q in output", tt.sec.Type, tt.contain)
			}
		})
	}
}

func TestRenderSection_UnknownType(t *testing.T) {
	sec := Section{Type: "my-custom-section", Body: "some body text"}
	got, err := renderSection(sec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `class="my-custom-section"`) {
		t.Errorf("missing type as class: %q", got)
	}
	if !strings.Contains(got, "some body text") {
		t.Errorf("missing body: %q", got)
	}
}
