package library

import (
	"fmt"
	"strings"
)

// joinNonEmpty filters out empty entries before joining with sep. Used by
// BuildDescriptionDocument to render compact lines like "Camera vantage:
// from_plane, ground" when only some of the components are populated.
func joinNonEmpty(parts []string, sep string) string {
	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, sep)
}

// prefixed returns "" if v is empty, "<prefix><v>" otherwise — keeps the
// counts line above readable.
func prefixed(prefix, v string) string {
	if v == "" {
		return ""
	}
	return prefix + v
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

// BuildDescriptionDocument returns the scene-side text for the
// photo_descriptions vector store (v12). Includes the parsed prose-derived
// fields (Vantage / GroundTruth / Condition / Mood), the classifier verdicts,
// and the full LLM description. EXIF and capture-context tokens are NOT
// included here — those live in BuildMetadataDocument so the embedding can
// concentrate signal on scene content without prose-dilution from camera
// settings.
//
// Photo: and File: navigation lines are kept at the top — they're zero-
// signal for vector retrieval but cheap, and historically the v1↔v2 A/B
// comparison wanted byte-comparable scene framing.
//
// Mood is gated on p.Mood ≠ "" so photos described before the Step 3 prompt
// change (no mood field) just skip the line.
func BuildDescriptionDocument(p *Photo) string {
	var b strings.Builder
	w := func(s string) {
		b.WriteString(s)
		b.WriteByte('\n')
	}

	w(fmt.Sprintf("Photo: %s", p.Name))
	w(fmt.Sprintf("File: %s", p.FileBasename))

	if p.Vantage != "" {
		w("Vantage: " + p.Vantage)
	}
	if p.GroundTruth != "" {
		w("Ground truth: " + p.GroundTruth)
	}
	if p.Condition != "" {
		w("Condition: " + p.Condition)
	}
	if p.Mood != "" {
		w("Mood: " + p.Mood)
	}

	if pov := joinNonEmpty([]string{p.POVContainer, p.POVAltitude, p.POVAngle}, ", "); pov != "" {
		w("Camera vantage: " + pov)
	}
	if len(p.SubjectCategory) > 0 {
		w("Subject category: " + strings.Join(p.SubjectCategory, ", "))
	}
	if p.SubjectAltitude != "" {
		w("Subject altitude: " + p.SubjectAltitude)
	}
	if p.SubjectDistance != "" {
		w("Subject distance: " + p.SubjectDistance)
	}
	if counts := joinNonEmpty(
		[]string{prefixed("people=", p.SubjectCount), prefixed("animals=", p.AnimalCount)}, ", ",
	); counts != "" {
		w("Counts: " + counts)
	}
	if scene := joinNonEmpty(
		[]string{p.SceneTimeOfDay, p.SceneIndoorOutdoor, p.SceneWeather}, ", ",
	); scene != "" {
		w("Scene: " + scene)
	}
	if p.Motion != "" {
		w("Motion: " + p.Motion)
	}
	if p.ColorPalette != "" {
		w("Color palette: " + p.ColorPalette)
	}
	if len(p.Framing) > 0 {
		w("Framing: " + strings.Join(p.Framing, ", "))
	}

	if p.FullDescription != "" {
		w("")
		w(p.FullDescription)
	}

	return strings.TrimRight(b.String(), "\n")
}

// BuildMetadataDocument returns the capture-context tokens for the
// photo_metadata vector store (v12). Format is space-separated tokens
// per the locked decision (see ARCHITECTURE.md "v12 design decisions"):
//
//	NIKON Z 8 NIKKOR Z 24-120mm f/4 S 90mm 35mm-equiv f/8 1/8000s ISO 720 Manual Auto Software Artist 2024
//
// Empty fields are dropped. Stylized renders for f-number (f/X), shutter
// (1/Xs or Xs), ISO (ISO X), and 35mm equivalent (Xmm-equiv — hyphenated
// to avoid embedder confusion with the actual focal length token).
//
// FTS-side coverage of these same tokens lives in exif.fts (v8); this
// function is the vector-side counterpart so dense queries like "shot at
// fast shutter" or "portrait aperture" reach metadata without diluting
// the scene embedding.
func BuildMetadataDocument(p *Photo) string {
	var parts []string
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}

	add(p.CameraMake)
	add(p.CameraModel)
	if p.LensModel != "" {
		add(p.LensModel)
	} else {
		add(p.LensInfo)
	}
	if p.FocalLengthMM != nil {
		add(fmt.Sprintf("%gmm", *p.FocalLengthMM))
	}
	if p.FocalLength35mm != nil {
		add(fmt.Sprintf("%gmm-equiv", *p.FocalLength35mm))
	}
	if p.FNumber != nil {
		add(fmt.Sprintf("f/%g", *p.FNumber))
	}
	if p.ShutterSeconds != nil {
		add(shutterFractionSeconds(*p.ShutterSeconds))
	}
	if p.ISO != nil {
		add(fmt.Sprintf("ISO %d", *p.ISO))
	}
	add(p.ExposureMode)
	add(p.WhiteBalance)
	add(p.Flash)
	add(p.Software)
	add(p.Artist)
	if p.DateTaken != "" {
		raw := dateTakenToExifString(p.DateTaken)
		if year, _, ok := strings.Cut(raw, ":"); ok && year != "" {
			add(year)
		}
	}

	return strings.Join(parts, " ")
}

// BuildQueryDocuments returns the LLM-generated search phrasings for the
// photo_queries vector store (v12). One string per phrasing — the indexer
// embeds each into its own row. Returns nil when the photo hasn't been
// described under the v12 prompt yet (Photo.GeneratedQueries left zero
// by LoadPhoto's LEFT JOIN on query_generations).
func BuildQueryDocuments(p *Photo) []string {
	return p.GeneratedQueries
}
