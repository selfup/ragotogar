package main

import (
	"strings"
	"testing"
)

// TestParseDescriptionFields covers the section-header variants we've seen in
// the wild from both devstral and ministral. The key regression cases are the
// parenthetical-aside headers (e.g. "Colors (in B&W):") — ministral ~3-4% of
// the time emits those, and the parser used to silently drop the colors field.
func TestParseDescriptionFields(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want descriptionFields
	}{
		{
			name: "plain headers",
			in: `Subject: a red cup
Setting: on a wooden desk
Light: window light from left
Colors: red, brown, beige
Composition: centered, eye level`,
			want: descriptionFields{
				Subject:     "a red cup",
				Setting:     "on a wooden desk",
				Light:       "window light from left",
				Colors:      "red, brown, beige",
				Composition: "centered, eye level",
			},
		},
		{
			name: "bold headers with list markers",
			in: `- **Subject**: a red cup
- **Setting**: on a wooden desk
- **Light**: window light from left
- **Colors**: red, brown, beige
- **Composition**: centered, eye level`,
			want: descriptionFields{
				Subject:     "a red cup",
				Setting:     "on a wooden desk",
				Light:       "window light from left",
				Colors:      "red, brown, beige",
				Composition: "centered, eye level",
			},
		},
		{
			name: "bold-wrapped colon",
			in: `**Subject:** a red cup
**Setting:** on a wooden desk
**Light:** window light from left
**Colors:** red, brown, beige
**Composition:** centered`,
			want: descriptionFields{
				Subject:     "a red cup",
				Setting:     "on a wooden desk",
				Light:       "window light from left",
				Colors:      "red, brown, beige",
				Composition: "centered",
			},
		},
		{
			name: "parenthetical aside on Colors header (the bug)",
			in: `Subject: a street
Setting: suburban road
Light: diffuse daylight
- **Colors** (from metadata and visual observation):
  - Grayscale palette due to the black-and-white format
  - Darker shades for shadows
Composition: leading lines`,
			want: descriptionFields{
				Subject: "a street",
				Setting: "suburban road",
				Light:   "diffuse daylight",
				Colors: "- Grayscale palette due to the black-and-white format\n" +
					"- Darker shades for shadows",
				Composition: "leading lines",
			},
		},
		{
			name: "parenthetical aside, short form",
			in: `Subject: a bridge
Setting: elevated roadway
Light: bright but diffuse
Colors (in black-and-white): grayscale tones dominate
Composition: low angle`,
			want: descriptionFields{
				Subject:     "a bridge",
				Setting:     "elevated roadway",
				Light:       "bright but diffuse",
				Colors:      "grayscale tones dominate",
				Composition: "low angle",
			},
		},
		{
			name: "multi-line content under headers",
			in: `Subject:
- a man in a black cap
- holding a drink
Setting: inside a music venue
Light: dim, from above
Colors: muted browns and yellows
Composition: low angle, shallow DOF`,
			want: descriptionFields{
				Subject:     "- a man in a black cap\n- holding a drink",
				Setting:     "inside a music venue",
				Light:       "dim, from above",
				Colors:      "muted browns and yellows",
				Composition: "low angle, shallow DOF",
			},
		},
		{
			name: "all seven fields including vantage and ground truth",
			in: `Subject: a man at the gate
Setting: airport terminal, indoor
Light: overhead fluorescent, suggests midday
Colors: greys, blues
Composition: medium distance, eye level
Vantage: handheld from inside the terminal, shooting through the boarding area window toward parked aircraft
Ground truth: one person visible, no animals, subject is static`,
			want: descriptionFields{
				Subject:     "a man at the gate",
				Setting:     "airport terminal, indoor",
				Light:       "overhead fluorescent, suggests midday",
				Colors:      "greys, blues",
				Composition: "medium distance, eye level",
				Vantage:     "handheld from inside the terminal, shooting through the boarding area window toward parked aircraft",
				GroundTruth: "one person visible, no animals, subject is static",
			},
		},
		{
			name: "vantage and ground truth with bold markers and list dash",
			in: `- **Subject:** a kitchen table
- **Setting:** indoor kitchen, residential
- **Light:** window from the right, daytime, clear weather
- **Colors:** warm wood tones, white
- **Composition:** eye level, close distance
- **Vantage:** handheld at table height
- **Ground truth:** no people, no animals, static`,
			want: descriptionFields{
				Subject:     "a kitchen table",
				Setting:     "indoor kitchen, residential",
				Light:       "window from the right, daytime, clear weather",
				Colors:      "warm wood tones, white",
				Composition: "eye level, close distance",
				Vantage:     "handheld at table height",
				GroundTruth: "no people, no animals, static",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDescriptionFields(tc.in)
			if got.Subject != tc.want.Subject {
				t.Errorf("Subject mismatch:\n  got:  %q\n  want: %q", got.Subject, tc.want.Subject)
			}
			if got.Setting != tc.want.Setting {
				t.Errorf("Setting mismatch:\n  got:  %q\n  want: %q", got.Setting, tc.want.Setting)
			}
			if got.Light != tc.want.Light {
				t.Errorf("Light mismatch:\n  got:  %q\n  want: %q", got.Light, tc.want.Light)
			}
			if got.Colors != tc.want.Colors {
				t.Errorf("Colors mismatch:\n  got:  %q\n  want: %q", got.Colors, tc.want.Colors)
			}
			if got.Composition != tc.want.Composition {
				t.Errorf("Composition mismatch:\n  got:  %q\n  want: %q", got.Composition, tc.want.Composition)
			}
			if got.Vantage != tc.want.Vantage {
				t.Errorf("Vantage mismatch:\n  got:  %q\n  want: %q", got.Vantage, tc.want.Vantage)
			}
			if got.GroundTruth != tc.want.GroundTruth {
				t.Errorf("GroundTruth mismatch:\n  got:  %q\n  want: %q", got.GroundTruth, tc.want.GroundTruth)
			}
		})
	}
}

// TestParseDescriptionFieldsMoodAndQueries covers the v13 prompt extension:
// the describer's combined-call output now includes a Mood section
// (aesthetic descriptors, comma-separated) and a Queries section (one
// search-shaped phrasing per line).
func TestParseDescriptionFieldsMoodAndQueries(t *testing.T) {
	in := `Subject: two friends at a wooden table
Setting: indoor cafe, exposed brick walls
Light: warm late-afternoon window light from the right
Colors: amber, brown, cream
Mood: warm, nostalgic, intimate
Composition: medium distance, eye level, shallow DOF
Vantage: handheld at table height
Ground truth: two people, no animals, both static
Condition: clean, well-maintained
Queries:
warm afternoon at a corner cafe
two friends sharing coffee
intimate candid portrait
shallow depth of field, indoor
golden hour cafe scene`

	got := parseDescriptionFields(in)

	if got.Mood != "warm, nostalgic, intimate" {
		t.Errorf("Mood mismatch: %q", got.Mood)
	}

	wantQueries := []string{
		"warm afternoon at a corner cafe",
		"two friends sharing coffee",
		"intimate candid portrait",
		"shallow depth of field, indoor",
		"golden hour cafe scene",
	}
	if len(got.Queries) != len(wantQueries) {
		t.Fatalf("Queries len = %d, want %d (%q)", len(got.Queries), len(wantQueries), got.Queries)
	}
	for i, w := range wantQueries {
		if got.Queries[i] != w {
			t.Errorf("Queries[%d] = %q, want %q", i, got.Queries[i], w)
		}
	}

	// Sanity: existing fields still parse alongside the new ones.
	if got.Subject != "two friends at a wooden table" {
		t.Errorf("Subject regressed: %q", got.Subject)
	}
	if got.Condition != "clean, well-maintained" {
		t.Errorf("Condition regressed: %q", got.Condition)
	}
}

// TestExtractQueriesListVariants covers the line-prefix shapes a less-
// disciplined model emits: numbered lists, bullets, quoted phrasings,
// trailing blank lines.
func TestExtractQueriesListVariants(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "plain lines",
			in: `warm afternoon
candid portrait
two friends`,
			want: []string{"warm afternoon", "candid portrait", "two friends"},
		},
		{
			name: "numbered list",
			in: `1. warm afternoon
2. candid portrait
3. two friends`,
			want: []string{"warm afternoon", "candid portrait", "two friends"},
		},
		{
			name: "mixed bullets and quotes",
			in: `- "warm afternoon"
* 'candid portrait'
• two friends`,
			want: []string{"warm afternoon", "candid portrait", "two friends"},
		},
		{
			name: "blank lines and trailing whitespace",
			in: `

warm afternoon

candid portrait
   `,
			want: []string{"warm afternoon", "candid portrait"},
		},
		{
			name: "empty input",
			in:   "",
			want: nil,
		},
		{
			name: "whitespace only",
			in:   "   \n\n  \n",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractQueriesList(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (got=%q want=%q)", len(got), len(tc.want), got, tc.want)
			}
			for i, w := range tc.want {
				if got[i] != w {
					t.Errorf("[%d] got %q want %q", i, got[i], w)
				}
			}
		})
	}
}

// TestParseDescriptionFieldsEmptyQueriesNeverWritesGarbage is the contract
// insertPhoto relies on: if the model omits the Queries section entirely,
// fields.Queries is nil (not an empty slice with empty strings) so the
// query_generations write is skipped per the spec's "do not write empty/
// broken JSON files silently" rule.
func TestParseDescriptionFieldsEmptyQueriesNeverWritesGarbage(t *testing.T) {
	in := `Subject: a quiet desk
Setting: home office
Light: lamp from above
Colors: warm wood tones`
	got := parseDescriptionFields(in)
	if got.Queries != nil {
		t.Errorf("expected nil queries, got %q", got.Queries)
	}
}

// TestDescribePromptHashStable pins the prompt-hash format so an accidental
// rewrite of the hash function (e.g. lengthening or changing the algorithm)
// surfaces as a test failure rather than silently invalidating every cached
// query_generations row's prompt_hash. The exact value is not pinned —
// just the shape (16 hex chars).
func TestDescribePromptHashStable(t *testing.T) {
	if len(describePromptHash) != 16 {
		t.Errorf("prompt hash length = %d, want 16", len(describePromptHash))
	}
	for _, c := range describePromptHash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-hex char %q in hash %q", c, describePromptHash)
		}
	}
}

func TestDetectRepetitionLoop(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantLoop  bool
		wantCount int
	}{
		{
			name:     "normal description, no loop",
			text:     "Subject: a red cup on a desk. Setting: wooden table near a window. Light: warm afternoon sun from the left. Colors: red, brown, cream. Composition: centered, shallow depth of field.",
			wantLoop: false,
		},
		{
			name:      "repetition loop like qwen airport bug",
			text:      "Subject: Several people seated. " + strings.Repeat("A person in a dark shirt sits near the center-right. ", 50) + "Composition: wide angle.",
			wantLoop:  true,
			wantCount: 50,
		},
		{
			name:     "short repeated fragments are ignored",
			text:     strings.Repeat("yes. ", 100),
			wantLoop: false, // "yes" is under minLen=20
		},
		{
			name:     "exactly at threshold is not flagged",
			text:     strings.Repeat("A person in a dark shirt sits near the center. ", 5) + "Done.",
			wantLoop: false, // 5 repeats, threshold is >5
		},
		{
			name:      "just over threshold is flagged",
			text:      strings.Repeat("A person in a dark shirt sits near the center. ", 6) + "Done.",
			wantLoop:  true,
			wantCount: 6,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sentence, count := detectRepetitionLoop(tc.text, 20, 5)
			gotLoop := count > 0
			if gotLoop != tc.wantLoop {
				t.Errorf("wantLoop=%v but got sentence=%q count=%d", tc.wantLoop, sentence, count)
			}
			if tc.wantLoop && count != tc.wantCount {
				t.Errorf("wantCount=%d but got %d", tc.wantCount, count)
			}
		})
	}
}
