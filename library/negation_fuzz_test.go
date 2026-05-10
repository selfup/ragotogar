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

		// Positive output contains no token that the parser would have
		// classified as negation: leading-dash with len>1 (covers `-foo`
		// and `-"foo`-prefix tokens both).
		for _, tok := range posFields {
			if strings.HasPrefix(tok, "-") && len(tok) > 1 && tok[1] != '-' {
				// Allow `--foo` style — the parser sends it to negative
				// but a future change might land it in positive; assert
				// only the `-foo` shape that we know goes negative.
				t.Errorf("positive contains negation-shaped token %q (q=%q, pos=%q)", tok, q, pos)
			}
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
		"long-exposure portrait",
		"truck-driver -monochrome",
		"red-shift truck-driver",
		`"truck-driver" red`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, q string) {
		pos := StripNegation(q)
		// Every input token without a LEADING dash must still appear in
		// the positive output. This catches a regression where the parser
		// might over-aggressively split internal dashes.
		for tok := range strings.FieldsSeq(q) {
			if strings.HasPrefix(tok, "-") {
				continue // negation token; legitimately stripped
			}
			// Token has no leading dash → must survive in positive.
			if !strings.Contains(pos, tok) {
				t.Errorf("non-negation token %q dropped from positive (q=%q pos=%q)", tok, q, pos)
			}
		}
	})
}
