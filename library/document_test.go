package library

import (
	"strings"
	"testing"
)

// v12 three-store split. BuildDescriptionDocument carries the scene-side
// fields (prose-derived + classifier verdicts + full description + new
// mood field) and explicitly excludes EXIF/capture-context tokens — those
// move to BuildMetadataDocument.

func TestBuildDescriptionDocumentSceneFieldsOnly(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	i := func(v int64) *int64 { return &v }

	p := &Photo{
		Name:               "scene",
		FileBasename:       "S.JPG",
		CameraMake:         "FUJIFILM", // metadata — must NOT appear
		CameraModel:        "X100VI",
		LensModel:          "23mm f/2",
		FocalLengthMM:      f(23.0),
		FNumber:            f(2.8),
		ShutterSeconds:     f(1.0 / 250),
		ISO:                i(800),
		Software:           "X100VI Ver1.01",
		Vantage:            "low handheld",
		GroundTruth:        "two people visible",
		Condition:          "pristine",
		Mood:               "warm, nostalgic, intimate",
		POVContainer:       "handheld",
		SceneIndoorOutdoor: "indoor",
		FullDescription:    "A quiet indoor scene with two people at a table.",
	}
	doc := BuildDescriptionDocument(p)

	for _, want := range []string{
		"Photo: scene",
		"File: S.JPG",
		"Vantage: low handheld",
		"Ground truth: two people visible",
		"Condition: pristine",
		"Mood: warm, nostalgic, intimate",
		"Camera vantage: handheld",
		"Scene: indoor",
		"A quiet indoor scene with two people at a table.",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("description missing %q\n--- doc ---\n%s\n", want, doc)
		}
	}
	for _, banned := range []string{
		"FUJIFILM", "X100VI", "23mm f/2", "f/2.8", "1/250s", "ISO 800",
		"Camera:", "Lens:", "Settings:", "Software:",
	} {
		if strings.Contains(doc, banned) {
			t.Errorf("description leaked metadata token %q\n--- doc ---\n%s\n", banned, doc)
		}
	}
}

func TestBuildDescriptionDocumentSkipsMoodWhenAbsent(t *testing.T) {
	// Photos described before the v12 prompt change have no Mood set.
	// The line should be omitted entirely, not appear as "Mood: ".
	p := &Photo{Name: "n", FileBasename: "n.JPG", Vantage: "high"}
	doc := BuildDescriptionDocument(p)
	if strings.Contains(doc, "Mood:") {
		t.Errorf("Mood line should be omitted when absent: %q", doc)
	}
	if !strings.Contains(doc, "Vantage: high") {
		t.Errorf("missing other scene field: %q", doc)
	}
}

// BuildMetadataDocument returns space-separated tokens (locked decision —
// see ARCHITECTURE.md "v12 design decisions"). Stylized renders for
// f-number, shutter, ISO, 35mm-equiv. Empty fields are dropped.

func TestBuildMetadataDocumentTokenForm(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	i := func(v int64) *int64 { return &v }

	p := &Photo{
		CameraMake:      "NIKON",
		CameraModel:     "Z 8",
		LensModel:       "NIKKOR Z 24-120mm f/4 S",
		FocalLengthMM:   f(90.0),
		FocalLength35mm: f(90.0),
		FNumber:         f(8.0),
		ShutterSeconds:  f(1.0 / 8000),
		ISO:             i(720),
		ExposureMode:    "Manual",
		DateTaken:       "2024-04-21T16:27:54",
	}
	got := BuildMetadataDocument(p)
	want := "NIKON Z 8 NIKKOR Z 24-120mm f/4 S 90mm 90mm-equiv f/8 1/8000s ISO 720 Manual 2024"
	if got != want {
		t.Errorf("metadata mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestBuildMetadataDocumentDropsEmptyFields(t *testing.T) {
	p := &Photo{
		CameraMake:  "FUJIFILM",
		CameraModel: "X100VI",
	}
	got := BuildMetadataDocument(p)
	if got != "FUJIFILM X100VI" {
		t.Errorf("empty-field metadata should be %q, got %q", "FUJIFILM X100VI", got)
	}
}

func TestBuildMetadataDocumentLensInfoFallback(t *testing.T) {
	p := &Photo{
		CameraMake: "FUJIFILM",
		LensInfo:   "23.0 mm f/2",
		// LensModel absent — LensInfo should be used
	}
	got := BuildMetadataDocument(p)
	if !strings.Contains(got, "23.0 mm f/2") {
		t.Errorf("LensInfo fallback missing: %q", got)
	}
	// And LensModel takes priority
	p.LensModel = "Fujinon 23mm"
	got = BuildMetadataDocument(p)
	if !strings.Contains(got, "Fujinon 23mm") {
		t.Errorf("LensModel not preferred: %q", got)
	}
	if strings.Contains(got, "23.0 mm f/2") {
		t.Errorf("both lens tokens emitted: %q", got)
	}
}

func TestBuildMetadataDocumentLongShutter(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	p := &Photo{ShutterSeconds: f(2.5)}
	got := BuildMetadataDocument(p)
	if got != "2.5s" {
		t.Errorf("long-shutter token = %q, want %q", got, "2.5s")
	}
}

// BuildQueryDocuments is a passthrough on Photo.GeneratedQueries — one
// string per phrasing, no concatenation.

func TestBuildQueryDocumentsReturnsSlice(t *testing.T) {
	p := &Photo{
		GeneratedQueries: []string{
			"warm sunset on a quiet beach",
			"low-angle handheld portrait",
			"two friends at a wooden table",
		},
	}
	got := BuildQueryDocuments(p)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0] != "warm sunset on a quiet beach" {
		t.Errorf("got[0] = %q", got[0])
	}
}

func TestBuildQueryDocumentsNilWhenAbsent(t *testing.T) {
	p := &Photo{Name: "n"}
	if got := BuildQueryDocuments(p); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}
