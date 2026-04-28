package main

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"
)

var monthNames = []string{
	"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December",
}

// humanDate turns ISO 8601 ("2024-04-21T16:27:54") or date-only
// ("2024-04-21") into "21 April 2024 at 16:27:54". Mirrors
// tools/rag_common.py humanize_exif_date so the indexed text and the
// rendered page agree.
func humanDate(iso string) string {
	if iso == "" {
		return ""
	}
	parts := strings.SplitN(iso, "T", 2)
	dateParts := strings.Split(parts[0], "-")
	if len(dateParts) != 3 {
		return iso
	}
	y, err1 := strconv.Atoi(dateParts[0])
	m, err2 := strconv.Atoi(dateParts[1])
	d, err3 := strconv.Atoi(dateParts[2])
	if err1 != nil || err2 != nil || err3 != nil || m < 1 || m > 12 {
		return iso
	}
	base := fmt.Sprintf("%d %s %d", d, monthNames[m-1], y)
	if len(parts) > 1 && parts[1] != "" {
		return base + " at " + parts[1]
	}
	return base
}

// nl2br renders newline-bearing prose as HTML, escaping unsafe chars and
// converting \n to <br>. Returns template.HTML so the template doesn't
// re-escape the resulting <br> tags.
func nl2br(s string) template.HTML {
	escaped := template.HTMLEscapeString(s)
	return template.HTML(strings.ReplaceAll(escaped, "\n", "<br>"))
}

// shutterFraction renders an exposure time in seconds back to the human
// "1/250" form for sub-second exposures, or "Ns" for >=1s.
func shutterFraction(seconds float64) string {
	if seconds <= 0 {
		return ""
	}
	if seconds >= 1 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	return fmt.Sprintf("1/%d", int(0.5+1.0/seconds))
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefInt(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// humanDateOnly drops the time-of-day portion: "2024-04-21T16:27:54" →
// "21 April 2024". Used in the cashier hero header where the date alone
// is shown alongside the photo.
func humanDateOnly(iso string) string {
	if i := strings.Index(iso, "T"); i >= 0 {
		iso = iso[:i]
	}
	return humanDate(iso)
}

// stem strips the file extension: "DSCF0001.JPG" → "DSCF0001". Used as
// the cashier hero title.
func stem(filename string) string {
	if filename == "" {
		return ""
	}
	if i := strings.LastIndex(filename, "."); i > 0 {
		return filename[:i]
	}
	return filename
}

// msToSeconds renders an int64 ms value as e.g. "10.394s" — matches the
// cashier hero-sub format.
func msToSeconds(ms *int64) string {
	if ms == nil {
		return ""
	}
	return fmt.Sprintf("%.3fs", float64(*ms)/1000.0)
}

func templateFuncMap() template.FuncMap {
	return template.FuncMap{
		"humanDate":       humanDate,
		"humanDateOnly":   humanDateOnly,
		"nl2br":           nl2br,
		"shutterFraction": shutterFraction,
		"stem":            stem,
		"msToSeconds":     msToSeconds,
		"deref":           deref,
		"derefInt":        derefInt,
	}
}
