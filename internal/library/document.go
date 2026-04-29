package library

import (
	"fmt"
	"strconv"
	"strings"
)

// Months drives both BuildDocument's "Captured on …" line and the cmd/web
// template's humanDate helper so the two stay in sync.
var Months = []string{
	"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December",
}

// HumanizeExifDate parses '2024:04:21 16:27:54' → '21 April 2024 at 16:27:54'.
// Returns empty string on any parse failure. Same shape as the Python
// rag_common.humanize_exif_date so the indexed text matches what the verify
// pass sees.
func HumanizeExifDate(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return ""
	}
	dateParts := strings.Split(parts[0], ":")
	if len(dateParts) != 3 {
		return ""
	}
	year, e1 := strconv.Atoi(dateParts[0])
	month, e2 := strconv.Atoi(dateParts[1])
	day, e3 := strconv.Atoi(dateParts[2])
	if e1 != nil || e2 != nil || e3 != nil {
		return ""
	}
	if month < 1 || month > 12 {
		return ""
	}
	base := fmt.Sprintf("%d %s %d", day, Months[month-1], year)
	if len(parts) > 1 {
		return base + " at " + parts[1]
	}
	return base
}

// BuildDocument turns a Photo into the single text blob that gets chunked +
// embedded by cmd/index, and re-built by cmd/search to feed the verify LLM.
// Both paths must produce byte-identical text — that's why the indexer and
// verifier share this function.
func BuildDocument(p *Photo) string {
	var b strings.Builder
	w := func(s string) {
		b.WriteString(s)
		b.WriteByte('\n')
	}

	w(fmt.Sprintf("Photo: %s", p.Name))
	w(fmt.Sprintf("File: %s", p.FileBasename))

	if p.CameraMake != "" || p.CameraModel != "" {
		w(fmt.Sprintf("Camera: %s", strings.TrimSpace(p.CameraMake+" "+p.CameraModel)))
	}

	if p.LensModel != "" {
		w("Lens: " + p.LensModel)
	} else if p.LensInfo != "" {
		w("Lens: " + p.LensInfo)
	}

	if p.DateTaken != "" {
		raw := dateTakenToExifString(p.DateTaken)
		w("Date: " + raw)
		if human := HumanizeExifDate(raw); human != "" {
			w("Captured on " + human)
		}
	}

	var settings []string
	if p.FocalLengthMM != nil {
		settings = append(settings, fmt.Sprintf("%g mm", *p.FocalLengthMM))
	}
	if p.FocalLength35mm != nil {
		settings = append(settings, fmt.Sprintf("%g mm (35mm equivalent)", *p.FocalLength35mm))
	}
	if p.FNumber != nil {
		settings = append(settings, fmt.Sprintf("f/%g", *p.FNumber))
	}
	if p.ShutterSeconds != nil {
		settings = append(settings, shutterFractionSeconds(*p.ShutterSeconds))
	}
	if p.ISO != nil {
		settings = append(settings, fmt.Sprintf("ISO %d", *p.ISO))
	}
	if p.ExposureMode != "" {
		settings = append(settings, p.ExposureMode+" exposure")
	}
	if p.WhiteBalance != "" {
		settings = append(settings, p.WhiteBalance+" white balance")
	}
	if len(settings) > 0 {
		w("Settings: " + strings.Join(settings, ", "))
	}

	if p.Flash != "" {
		w("Flash: " + p.Flash)
	}
	if p.Software != "" {
		w("Software: " + p.Software)
	}
	if p.Artist != "" {
		w("Photographer: " + p.Artist)
	}

	if p.FullDescription != "" {
		w("")
		w(p.FullDescription)
	}

	// Trailing newline trimmed — matches the Python "\n".join behavior.
	return strings.TrimRight(b.String(), "\n")
}

// shutterFractionSeconds renders shutter speed for the document body using
// the same convention as the Python builder (e.g. "1/250s"). Uses int
// rounding for sub-second exposures so a stored 0.004 (1/250) re-renders
// as "1/250s" rather than "0.004s".
func shutterFractionSeconds(seconds float64) string {
	if seconds <= 0 {
		return ""
	}
	if seconds >= 1 {
		return fmt.Sprintf("%gs", seconds)
	}
	return fmt.Sprintf("1/%ds", int(0.5+1.0/seconds))
}
