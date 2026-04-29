package library

import (
	"strings"
	"testing"
)

func TestHumanizeExifDate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2024:04:21 16:27:54", "21 April 2024 at 16:27:54"},
		{"2024:04:21", "21 April 2024"},
		{"", ""},
		{"garbage", ""},
		{"2024:13:21 00:00:00", ""},  // out-of-range month
		{"2024:00:21 00:00:00", ""},  // zero month
	}
	for _, tc := range cases {
		if got := HumanizeExifDate(tc.in); got != tc.want {
			t.Errorf("HumanizeExifDate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildDocumentFullPhoto(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	i := func(v int64) *int64 { return &v }

	p := &Photo{
		Name:                 "test_photo",
		FileBasename:         "DSCF0086.JPG",
		CameraMake:           "FUJIFILM",
		CameraModel:          "X100VI",
		LensModel:            "23mm f/2",
		DateTaken:            "2024-04-21T16:27:54",
		FocalLengthMM:        f(23.0),
		FocalLength35mm:      f(35.0),
		FNumber:              f(5.6),
		ShutterSeconds:       f(1.0 / 250),
		ISO:                  i(500),
		ExposureCompensation: f(-0.67),
		ExposureMode:         "Auto",
		WhiteBalance:         "Auto",
		Flash:                "No Flash",
		Software:             "X100VI Ver1.01",
		Subject:              "a man in a gray shirt",
		Setting:              "indoor scene with trees visible",
		Light:                "natural daylight",
		Colors:               "muted greens",
		Composition:          "shallow depth of field",
		FullDescription:      "Full description of the scene with red truck and trees.",
	}

	doc := BuildDocument(p)

	for _, want := range []string{
		"Photo: test_photo",
		"File: DSCF0086.JPG",
		"Camera: FUJIFILM X100VI",
		"Lens: 23mm f/2",
		"Date: 2024:04:21 16:27:54",          // re-converted to legacy EXIF form
		"Captured on 21 April 2024 at 16:27:54", // human form
		"23 mm",
		"35 mm (35mm equivalent)",
		"f/5.6",
		"1/250s",
		"ISO 500",
		"Auto exposure",
		"Auto white balance",
		"Flash: No Flash",
		"Software: X100VI Ver1.01",
		"Full description of the scene with red truck and trees.",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("BuildDocument missing %q\n--- doc ---\n%s\n", want, doc)
		}
	}
}

func TestBuildDocumentTypedClassification(t *testing.T) {
	// Photo classified by cmd/classify — the typed enum fields should
	// surface as canonical text in the document for vector embedding.
	p := &Photo{
		Name:               "typed",
		FileBasename:       "x.JPG",
		POVContainer:       "from_plane",
		POVAltitude:        "ground",
		POVAngle:           "looking_down",
		SubjectAltitude:    "on_ground",
		SubjectCategory:    []string{"architecture", "landscape"},
		SubjectDistance:    "wide",
		SubjectCount:       "0",
		AnimalCount:        "0",
		SceneTimeOfDay:     "day",
		SceneIndoorOutdoor: "outdoor",
		SceneWeather:       "clear",
		Framing:            []string{"through_window"},
		Motion:             "static",
		ColorPalette:       "cool",
	}
	doc := BuildDocument(p)
	for _, want := range []string{
		"Camera vantage: from_plane, ground, looking_down",
		"Subject category: architecture, landscape",
		"Subject altitude: on_ground",
		"Subject distance: wide",
		"Counts: people=0, animals=0",
		"Scene: day, outdoor, clear",
		"Motion: static",
		"Color palette: cool",
		"Framing: through_window",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("BuildDocument missing %q\n--- doc ---\n%s\n", want, doc)
		}
	}
}

func TestBuildDocumentTypedClassificationPartial(t *testing.T) {
	// Only some fields populated — the others should not produce empty
	// or mis-rendered lines.
	p := &Photo{
		Name:         "partial",
		FileBasename: "x.JPG",
		POVContainer: "handheld",
		// POVAltitude + POVAngle unset → "Camera vantage: handheld" only
		Motion: "static",
	}
	doc := BuildDocument(p)
	if !strings.Contains(doc, "Camera vantage: handheld\n") {
		t.Errorf("missing camera vantage with single value: %q", doc)
	}
	if strings.Contains(doc, "Camera vantage: handheld,") {
		t.Errorf("trailing comma in vantage: %q", doc)
	}
	if !strings.Contains(doc, "Motion: static") {
		t.Errorf("missing motion: %q", doc)
	}
	// no Counts line should appear when both counts are absent
	if strings.Contains(doc, "Counts:") {
		t.Errorf("Counts line should be omitted when both empty: %q", doc)
	}
}

func TestBuildDocumentMinimal(t *testing.T) {
	p := &Photo{
		Name:         "min",
		FileBasename: "MIN.JPG",
	}
	doc := BuildDocument(p)
	want := "Photo: min\nFile: MIN.JPG"
	if doc != want {
		t.Errorf("minimal photo doc = %q, want %q", doc, want)
	}
}

func TestBuildDocumentLensFallback(t *testing.T) {
	// LensInfo used when LensModel is absent
	p := &Photo{Name: "x", FileBasename: "x.JPG", LensInfo: "23.0 mm f/2"}
	doc := BuildDocument(p)
	if !strings.Contains(doc, "Lens: 23.0 mm f/2") {
		t.Errorf("lens_info fallback missing: %q", doc)
	}
	// LensModel takes priority over LensInfo
	p.LensModel = "Fujinon 23mm"
	doc = BuildDocument(p)
	if !strings.Contains(doc, "Lens: Fujinon 23mm") {
		t.Errorf("lens_model not preferred: %q", doc)
	}
	if strings.Contains(doc, "Lens: 23.0 mm f/2") {
		t.Errorf("both lens lines emitted: %q", doc)
	}
}

func TestBuildDocumentDateOnly(t *testing.T) {
	p := &Photo{
		Name:         "x",
		FileBasename: "x.JPG",
		DateTaken:    "2024-04-21", // no time component
	}
	doc := BuildDocument(p)
	if !strings.Contains(doc, "Date: 2024:04:21") {
		t.Errorf("missing legacy EXIF date form: %q", doc)
	}
	if !strings.Contains(doc, "Captured on 21 April 2024") {
		t.Errorf("missing date-only humanization: %q", doc)
	}
	if strings.Contains(doc, "Captured on 21 April 2024 at") {
		t.Errorf("date-only should not append a time: %q", doc)
	}
}
