package main

import (
	"strings"
	"testing"
)

func TestHumanDate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2024-04-21T16:27:54", "21 April 2024 at 16:27:54"},
		{"2024-04-21", "21 April 2024"},
		{"2026-01-05T09:00:00", "5 January 2026 at 09:00:00"},
		{"", ""},
		{"garbage", "garbage"},
		{"2024-13-01", "2024-13-01"}, // out-of-range month falls through unchanged
	}
	for _, tc := range cases {
		if got := humanDate(tc.in); got != tc.want {
			t.Errorf("humanDate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNl2br(t *testing.T) {
	got := string(nl2br("line1\nline2"))
	if !strings.Contains(got, "<br>") {
		t.Errorf("nl2br missing <br>: %q", got)
	}

	// HTML metacharacters in input must be escaped before <br> insertion.
	got = string(nl2br("<script>x</script>\nhello"))
	if strings.Contains(got, "<script>") {
		t.Errorf("nl2br left raw <script>: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("nl2br did not HTML-escape: %q", got)
	}
}

func TestShutterFraction(t *testing.T) {
	cases := []struct {
		seconds float64
		want    string
	}{
		{1.0 / 250, "1/250"},
		{1.0 / 4000, "1/4000"},
		{0.5, "1/2"},
		{1.0, "1.0s"},
		{2.5, "2.5s"},
		{0, ""},
	}
	for _, tc := range cases {
		if got := shutterFraction(tc.seconds); got != tc.want {
			t.Errorf("shutterFraction(%g) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

func TestHumanDateOnly(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2024-04-21T16:27:54", "21 April 2024"},
		{"2024-04-21", "21 April 2024"},
		{"", ""},
		{"garbage", "garbage"},
	}
	for _, tc := range cases {
		if got := humanDateOnly(tc.in); got != tc.want {
			t.Errorf("humanDateOnly(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStem(t *testing.T) {
	cases := []struct{ in, want string }{
		{"DSCF0001.JPG", "DSCF0001"},
		{"photo.tar.gz", "photo.tar"},
		{"noext", "noext"},
		{".hidden", ".hidden"}, // leading dot only — no extension to drop
		{"", ""},
	}
	for _, tc := range cases {
		if got := stem(tc.in); got != tc.want {
			t.Errorf("stem(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMsToSeconds(t *testing.T) {
	if got := msToSeconds(nil); got != "" {
		t.Errorf("msToSeconds(nil) = %q, want empty", got)
	}
	n := int64(10394)
	if got := msToSeconds(&n); got != "10.394s" {
		t.Errorf("msToSeconds(10394) = %q, want 10.394s", got)
	}
	z := int64(0)
	if got := msToSeconds(&z); got != "0.000s" {
		t.Errorf("msToSeconds(0) = %q", got)
	}
}

func TestDerefHelpers(t *testing.T) {
	if deref(nil) != 0 {
		t.Errorf("deref(nil) should be 0")
	}
	x := 1.5
	if deref(&x) != 1.5 {
		t.Errorf("deref unwrap")
	}
	if derefInt(nil) != 0 {
		t.Errorf("derefInt(nil)")
	}
	n := int64(42)
	if derefInt(&n) != 42 {
		t.Errorf("derefInt unwrap")
	}
}
