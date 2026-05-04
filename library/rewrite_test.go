package library

import "testing"

func TestLooksBoolean(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plain prose", "red trucks on roads no monochrome", false},
		{"single word", "trucks", false},
		{"leading dash with term", "-monochrome", true},
		{"leading dash with quoted phrase", `-"black and white"`, true},
		{"middle dash term", "trucks -monochrome on road", true},
		{"quoted phrase", `"red truck" on road`, true},
		{"OR uppercase", "trucks OR cars", true},
		{"OR at start", "OR cars", true},
		{"OR at end", "trucks OR", true},
		{"or lowercase ignored", "trucks or cars", false},
		{"compound dashed word doesn't trigger", "truck-driver on road", false},
		{"bare dash doesn't trigger", "red - truck", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksBoolean(tt.in); got != tt.want {
				t.Errorf("looksBoolean(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeRewrite(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"clean output", `"red truck" road -monochrome`, `"red truck" road -monochrome`},
		{"trailing whitespace", `"red truck" road    `, `"red truck" road`},
		{"code fence wrapper", "```\n\"red truck\" road\n```", `"red truck" road`},
		{"code fence text wrapper", "```text\n\"red truck\" road\n```", `"red truck" road`},
		{"Rewritten label prefix", `Rewritten: "red truck" road`, `"red truck" road`},
		{"lowercase rewritten label", `rewritten: "red truck"`, `"red truck"`},
		{"Output label prefix", `Output: trucks -monochrome`, "trucks -monochrome"},
		{"multi-line keeps first only", "trucks -monochrome\n\nthis is a comment from the model", "trucks -monochrome"},
		{"empty stays empty", "", ""},
		{"only commentary", "I'm not sure how to rewrite that.", "I'm not sure how to rewrite that."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeRewrite(tt.in); got != tt.want {
				t.Errorf("sanitizeRewrite(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
