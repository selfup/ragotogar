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
		})
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
