package main

import "testing"

func TestParseFloatLoose(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"5.6", 5.6},
		{"f/2", 2.0},
		{"F/2.8", 2.8},
		{"  2.0  ", 2.0},
		{"", nil},
		{"   ", nil},
		{"garbage", nil},
	}
	for _, tc := range cases {
		got := parseFloatLoose(tc.in)
		if !sameAny(got, tc.want) {
			t.Errorf("parseFloatLoose(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseIntLoose(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"500", int64(500)},
		{"12800", int64(12800)},
		{"3200.0", int64(3200)},
		{"", nil},
		{"garbage", nil},
	}
	for _, tc := range cases {
		got := parseIntLoose(tc.in)
		if !sameAny(got, tc.want) {
			t.Errorf("parseIntLoose(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseDimensionMM(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"23.0 mm", 23.0},
		{"23.0", 23.0},
		{"23 MM", 23.0},
		{"50mm", 50.0},
		{"", nil},
		{"garbage mm", nil},
	}
	for _, tc := range cases {
		got := parseDimensionMM(tc.in)
		if !sameAny(got, tc.want) {
			t.Errorf("parseDimensionMM(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseExposureTime(t *testing.T) {
	const eps = 1e-9
	if got := parseExposureTime("1/250"); !approxEq(got, 1.0/250, eps) {
		t.Errorf("parseExposureTime(1/250) = %v", got)
	}
	if got := parseExposureTime("1/4000"); !approxEq(got, 1.0/4000, eps) {
		t.Errorf("parseExposureTime(1/4000) = %v", got)
	}
	if got := parseExposureTime("0.5"); got != 0.5 {
		t.Errorf("parseExposureTime(0.5) = %v, want 0.5", got)
	}
	if got := parseExposureTime("1/0"); got != nil {
		t.Errorf("parseExposureTime(1/0) = %v, want nil (divide by zero)", got)
	}
	if got := parseExposureTime(""); got != nil {
		t.Errorf("parseExposureTime(empty) = %v, want nil", got)
	}
	if got := parseExposureTime("garbage"); got != nil {
		t.Errorf("parseExposureTime(garbage) = %v, want nil", got)
	}
}

func TestParseExifDate(t *testing.T) {
	iso, year, month := parseExifDate("2024:04:21 16:27:54")
	if iso != "2024-04-21T16:27:54" {
		t.Errorf("iso = %v, want 2024-04-21T16:27:54", iso)
	}
	if year != 2024 || month != 4 {
		t.Errorf("year=%v month=%v, want 2024/4", year, month)
	}

	iso, year, month = parseExifDate("2024:04:21")
	if iso != "2024-04-21" || year != 2024 || month != 4 {
		t.Errorf("date-only: iso=%v year=%v month=%v", iso, year, month)
	}

	for _, bad := range []string{"", "garbage", "2024:13:21 00:00:00", "not:a:date"} {
		iso, year, month := parseExifDate(bad)
		if iso != nil || year != nil || month != nil {
			t.Errorf("parseExifDate(%q) = (%v, %v, %v), want all nil", bad, iso, year, month)
		}
	}
}

func TestNullIfEmpty(t *testing.T) {
	if nullIfEmpty("") != nil {
		t.Errorf("empty string should yield nil")
	}
	if nullIfEmpty("   ") != nil {
		t.Errorf("whitespace-only should yield nil")
	}
	if nullIfEmpty("hello") != "hello" {
		t.Errorf("non-empty should pass through")
	}
}

// helpers

func sameAny(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a == b
}

func approxEq(a any, want float64, eps float64) bool {
	f, ok := a.(float64)
	if !ok {
		return false
	}
	d := f - want
	if d < 0 {
		d = -d
	}
	return d < eps
}
