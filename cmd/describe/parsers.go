package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// EXIF parsers — Go ports of the parsers from tools/sql_sync.py. They turn
// exiftool's string output into typed values (or `nil` for SQL NULL) before
// the INSERT step in cmd/describe.

// nullIfEmpty returns nil for empty/whitespace-only strings so they land as
// SQL NULL instead of empty TEXT — schema relies on this for COUNT and
// GROUP BY queries to skip absent fields.
func nullIfEmpty(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}

// parseFloatLoose parses '23.0', '5.6', 'f/2', '' → float64 or nil.
// Tolerates a leading 'f/' prefix (FNumber on some bodies).
func parseFloatLoose(v string) any {
	s := strings.TrimSpace(v)
	if s == "" {
		return nil
	}
	s = strings.TrimLeft(s, "fF/")
	s = strings.TrimSpace(s)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return f
}

// parseIntLoose parses '500', '12800.0', '' → int64 or nil. Tolerates a
// fractional form because some EXIF fields come through as numeric strings.
func parseIntLoose(v string) any {
	s := strings.TrimSpace(v)
	if s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return int64(f)
}

var trailingMM = regexp.MustCompile(`(?i)\s*mm\s*$`)

// parseDimensionMM parses '23.0 mm', '23.0', '' → float64 or nil.
func parseDimensionMM(v string) any {
	s := strings.TrimSpace(v)
	if s == "" {
		return nil
	}
	s = trailingMM.ReplaceAllString(s, "")
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return nil
	}
	return f
}

// parseExposureTime parses '1/250' → 0.004, '0.5' → 0.5, '' → nil.
// Returns nil on divide-by-zero.
func parseExposureTime(v string) any {
	s := strings.TrimSpace(v)
	if s == "" {
		return nil
	}
	if i := strings.Index(s, "/"); i >= 0 {
		num, err1 := strconv.ParseFloat(strings.TrimSpace(s[:i]), 64)
		denom, err2 := strconv.ParseFloat(strings.TrimSpace(s[i+1:]), 64)
		if err1 != nil || err2 != nil || denom == 0 {
			return nil
		}
		return num / denom
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return f
}

// parseExifDate parses '2024:04:21 16:27:54' → ('2024-04-21T16:27:54', 2024, 4).
// Returns (nil, nil, nil) on any parse failure or out-of-range month.
func parseExifDate(v string) (any, any, any) {
	s := strings.TrimSpace(v)
	if s == "" {
		return nil, nil, nil
	}
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return nil, nil, nil
	}
	dateParts := strings.Split(parts[0], ":")
	if len(dateParts) != 3 {
		return nil, nil, nil
	}
	y, err1 := strconv.Atoi(dateParts[0])
	m, err2 := strconv.Atoi(dateParts[1])
	d, err3 := strconv.Atoi(dateParts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, nil, nil
	}
	if m < 1 || m > 12 {
		return nil, nil, nil
	}
	iso := fmt.Sprintf("%04d-%02d-%02d", y, m, d)
	if len(parts) > 1 {
		iso += "T" + parts[1]
	}
	return iso, y, m
}
