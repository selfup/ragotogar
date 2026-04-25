package main

import (
	"fmt"
	"regexp"
	"strings"
)

type exifData struct {
	FileName             string `json:"file_name"`
	DateTimeOriginal     string `json:"date_time_original"`
	Make                 string `json:"make"`
	Model                string `json:"model"`
	LensModel            string `json:"lens_model"`
	LensInfo             string `json:"lens_info"`
	FocalLength          string `json:"focal_length"`
	FocalLengthIn35mm    string `json:"focal_length_in_35mm"`
	FNumber              string `json:"f_number"`
	ExposureTime         string `json:"exposure_time"`
	ISO                  string `json:"iso"`
	ExposureCompensation string `json:"exposure_compensation"`
	WhiteBalance         string `json:"white_balance"`
	MeteringMode         string `json:"metering_mode"`
	ExposureMode         string `json:"exposure_mode"`
	Flash                string `json:"flash"`
	ImageWidth           string `json:"image_width"`
	ImageHeight          string `json:"image_height"`
	GPSLatitude          string `json:"gps_latitude,omitempty"`
	GPSLongitude         string `json:"gps_longitude,omitempty"`
	Artist               string `json:"artist,omitempty"`
	Copyright            string `json:"copyright,omitempty"`
	Software             string `json:"software,omitempty"`
}

type photoFields struct {
	Subject     string `json:"subject"`
	Setting     string `json:"setting"`
	Light       string `json:"light"`
	Colors      string `json:"colors"`
	Composition string `json:"composition"`
}

type PhotoData struct {
	Name        string      `json:"name"`
	File        string      `json:"file"`
	Path        string      `json:"path"`
	Preview     string      `json:"preview"`
	PreviewMs   int64       `json:"preview_ms"`
	Inference   string      `json:"inference"`
	InferenceMs int64       `json:"inference_ms"`
	Metadata    exifData    `json:"metadata"`
	Fields      photoFields `json:"fields"`
}

var months = []string{
	"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December",
}

func formatDate(dt string) string {
	// "YYYY:MM:DD HH:MM:SS" → "D Month YYYY"
	parts := strings.Fields(dt)
	if len(parts) == 0 {
		return dt
	}
	dateParts := strings.Split(parts[0], ":")
	if len(dateParts) != 3 {
		return dt
	}
	y, mo, d := dateParts[0], dateParts[1], dateParts[2]
	moIdx := 0
	fmt.Sscanf(mo, "%d", &moIdx)
	day := strings.TrimLeft(d, "0")
	if day == "" {
		day = "0"
	}
	if moIdx < 1 || moIdx > 12 {
		return dt
	}
	return fmt.Sprintf("%s %s %s", day, months[moIdx-1], y)
}

var reBullet = regexp.MustCompile(`(?m)^[-*]\s+`)

func stripBullets(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	for _, l := range lines {
		l = reBullet.ReplaceAllString(strings.TrimSpace(l), "")
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, " ")
}

var reSentence = regexp.MustCompile(`^[^.!?]+[.!?]`)

func firstSentence(text string) string {
	s := stripBullets(text)
	if m := reSentence.FindString(s); m != "" {
		return strings.TrimSpace(m)
	}
	return strings.TrimSpace(s)
}

func buildMarkdown(data PhotoData) string {
	m := data.Metadata
	f := data.Fields

	date := formatDate(m.DateTimeOriginal)
	timePart := ""
	if parts := strings.Fields(m.DateTimeOriginal); len(parts) > 1 {
		timePart = parts[1]
	}
	camera := m.Make + " " + m.Model
	fileStem := strings.TrimSuffix(data.File, func() string {
		idx := strings.LastIndex(data.File, ".")
		if idx < 0 {
			return ""
		}
		return data.File[idx:]
	}())
	year := ""
	if len(m.DateTimeOriginal) >= 4 {
		year = m.DateTimeOriginal[:4]
	}

	subject := stripBullets(f.Subject)
	setting := stripBullets(f.Setting)
	light := stripBullets(f.Light)
	colors := stripBullets(f.Colors)
	composition := stripBullets(f.Composition)

	// metadata list — filter empty values
	type kv struct{ k, v string }
	allMeta := []kv{
		{"file_name", m.FileName},
		{"name", data.Name},
		{"path", data.Path},
		{"make", m.Make},
		{"model", m.Model},
		{"date_time_original", m.DateTimeOriginal},
		{"focal_length", m.FocalLength},
		{"f_number", "f/" + m.FNumber},
		{"exposure_time", m.ExposureTime + "s"},
		{"iso", m.ISO},
		{"exposure_compensation", m.ExposureCompensation},
		{"white_balance", m.WhiteBalance},
		{"metering_mode", m.MeteringMode},
		{"exposure_mode", m.ExposureMode},
		{"flash", m.Flash},
		{"image_width", m.ImageWidth},
		{"image_height", m.ImageHeight},
		{"artist", m.Artist},
		{"copyright", m.Copyright},
		{"software", m.Software},
		{"preview", data.Preview},
		{"preview_ms", fmt.Sprintf("%d", data.PreviewMs)},
		{"inference", data.Inference},
		{"inference_ms", fmt.Sprintf("%d", data.InferenceMs)},
	}
	var metaLines []string
	n := 1
	for _, pair := range allMeta {
		v := strings.TrimPrefix(strings.TrimPrefix(pair.v, "f/"), "s")
		_ = v
		// filter: skip if the value (ignoring our prefix additions) is empty
		raw := pair.v
		switch pair.k {
		case "f_number":
			raw = m.FNumber
		case "exposure_time":
			raw = m.ExposureTime
		}
		if raw == "" {
			continue
		}
		metaLines = append(metaLines, fmt.Sprintf("%d. %s: %s", n, pair.k, pair.v))
		n++
	}
	metaList := strings.Join(metaLines, "\n")

	return fmt.Sprintf(`---
title: %s — %s
---

:::hero { "masthead": "Photograph Analysis", "meta": "%s", "image": "file://%s" }
%s · DxO-processed still · %s

# %s / *%s.*

---

Captured on %s at %s, processed through %s. Preview generated in %s; inference completed in %s.

---

Shot on %s — %s, f/%s, %ss, ISO %s, %s exposure with %s metering, %s white balance, %s. Image dimensions: %s × %s.
:::

:::dual-pillars { "num": "II.", "label": "Visual Analysis" }
# Five fields. *Subject. Setting. Light. Colors. Composition.*

---

# Subject & Setting
- Subject. — %s
- Setting. — %s

---

# Light, Colors & Composition
- Light. — %s
- Colors. — %s
- Composition. — %s
:::

:::photo-meta { "num": "III.", "label": "Camera Settings" }
# All metadata for *this frame:*

%s

---

*All settings recorded at the moment of capture — %s, %s.*
:::

:::close { "num": "IV.", "label": "In Closing", "mark": "%s", "meta": "%s · %s" }
*%s* **%s**

---

File: %s · Original: %s · Processed in %s · Preview: %s · Inference: %s
:::
`,
		fileStem, camera,
		date, data.Path,
		camera, m.Artist,
		fileStem, camera,
		date, timePart, m.Software, data.Preview, data.Inference,
		camera, m.FocalLength, m.FNumber, m.ExposureTime, m.ISO,
		m.ExposureMode, m.MeteringMode, m.WhiteBalance, strings.ToLower(m.Flash),
		m.ImageWidth, m.ImageHeight,
		subject, setting,
		light, colors, composition,
		metaList,
		timePart, date,
		m.Artist, fileStem, year,
		firstSentence(f.Subject), firstSentence(f.Setting),
		data.File, m.DateTimeOriginal, m.Software, data.Preview, data.Inference,
	)
}
