package library

import (
	"strings"
	"testing"
)

// FuzzStripExtractNegationPartition exercises the StripNegation /
// ExtractNegation pair as a partition: every whitespace-split token in
// the input must land in exactly one of the two halves (positive or
// negative), with no overlap and no loss.
//
// Without -fuzz, this runs only the seed corpus below — fast, deterministic.
// With `go test -fuzz=FuzzStripExtractNegationPartition`, the fuzzer
// generates random strings to look for partition violations.
func FuzzStripExtractNegationPartition(f *testing.F) {
	seeds := []string{
		"",
		"   ",
		"red",
		"red truck",
		"-red",
		`-"red truck"`,
		"red -monochrome",
		"highway - truck",
		"highway -\t\n truck",
		`highway - "red truck"`,
		`"road - truck" -car`,
		`"road -truck" -car`,
		`red -monochrome -"black and white" -grayscale`,
		`"phrase only"`,
		"truck-driver", // compound — must NOT be split
		"-",            // bare dash stays positive (len==1)
		"--double",     // double-dash treated as negation
		"OR",           // operator left in place
		`"unclosed`,    // unmatched opening quote
		`-"unclosed`,   // unmatched negation quote
		`-`,
		"a b c d e",
		"-a -b -c",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, q string) {
		pos := StripNegation(q)
		neg := ExtractNegation(q)

		inputFields := strings.Fields(q)
		posFields := strings.Fields(pos)
		negFields := strings.Fields(neg)

		// Partition: token count preserved across positive + negative.
		// Note: Fields(input) collapses runs of whitespace, but pos and
		// neg are produced by Join with single spaces. The counts are
		// what Fields sees in each — see the case analysis in
		// search.go:splitNegation for the unmatched-quote edge case.
		if got, want := len(posFields)+len(negFields), len(inputFields); got != want {
			t.Errorf("partition count: pos=%d + neg=%d = %d, want %d (input=%q)",
				len(posFields), len(negFields), got, want, q)
		}

		// Positive phrases can contain literal dashes, but no exclusions
		// should remain outside those phrases.
		if got := ExtractNegation(pos); got != "" {
			t.Errorf("positive contains exclusions %q (q=%q, pos=%q)", got, q, pos)
		}

		// Idempotence: applying StripNegation twice equals applying it
		// once. The positive output has no negation tokens left to strip.
		if got := StripNegation(pos); got != pos {
			t.Errorf("StripNegation not idempotent: q=%q pos=%q strip(pos)=%q", q, pos, got)
		}
		// Same for ExtractNegation on a negation-only string.
		if got := ExtractNegation(neg); got != neg {
			t.Errorf("ExtractNegation not idempotent: q=%q neg=%q extract(neg)=%q", q, neg, got)
		}
	})
}

// FuzzStripNegationDoesNotMangleCompounds: a token like `truck-driver`
// or `X100VI-2` should pass through StripNegation unchanged. The dash is
// internal, not leading, so the parser leaves it alone. This is the
// load-bearing carve-out from commit a876a56's negation work.
func FuzzStripNegationDoesNotMangleCompounds(f *testing.F) {
	seeds := []string{
		"truck-driver",
		"X100VI-2",
		"black-and-white",
		"long-exposure",
		"red-shift",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, word string) {
		// Build a single positive compound followed by a spaced exclusion.
		// Arbitrary queries cannot promise that every non-dash token survives:
		// a term following a standalone dash now belongs to the exclusion.
		compound := "photo-" + word
		if strings.Contains(compound, `"`) || len(strings.Fields(compound)) != 1 || strings.TrimSpace(compound) != compound {
			t.Skip()
		}
		if got := StripNegation(compound + " - truck"); got != compound {
			t.Errorf("compound changed: got %q, want %q", got, compound)
		}
	})
}
