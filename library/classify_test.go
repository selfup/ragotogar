package library

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildClassifyPromptIncludesAllFieldsAndDescription(t *testing.T) {
	desc := "Subject: a man at a gate\nVantage: handheld inside the terminal"
	got := BuildClassifyPrompt(desc)

	// every field name must appear so the model has the schema
	for _, k := range []string{
		"pov_container", "pov_altitude", "pov_angle",
		"subject_altitude", "subject_category", "subject_distance",
		"subject_count", "animal_count",
		"scene_time_of_day", "scene_indoor_outdoor", "scene_weather",
		"framing", "motion", "color_palette",
	} {
		if !strings.Contains(got, k) {
			t.Errorf("prompt missing field %q", k)
		}
	}
	if !strings.Contains(got, desc) {
		t.Errorf("prompt missing description body")
	}
}

func TestParseClassifyResponse(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
		check   func(t *testing.T, c Classification)
	}{
		{
			name: "clean json",
			raw: `{
				"pov_container": "from_plane",
				"pov_altitude": "ground",
				"pov_angle": "looking_down",
				"subject_altitude": "on_ground",
				"subject_category": ["architecture", "landscape"],
				"subject_distance": "wide",
				"subject_count": "0",
				"animal_count": "0",
				"scene_time_of_day": "day",
				"scene_indoor_outdoor": "outdoor",
				"scene_weather": "clear",
				"framing": ["through_window"],
				"motion": "static",
				"color_palette": "cool"
			}`,
			check: func(t *testing.T, c Classification) {
				if c.POVContainer == nil || *c.POVContainer != "from_plane" {
					t.Errorf("pov_container: got %v, want from_plane", c.POVContainer)
				}
				if !reflect.DeepEqual(c.SubjectCategory, []string{"architecture", "landscape"}) {
					t.Errorf("subject_category: got %v", c.SubjectCategory)
				}
				if !reflect.DeepEqual(c.Framing, []string{"through_window"}) {
					t.Errorf("framing: got %v", c.Framing)
				}
			},
		},
		{
			name: "json wrapped in code fence",
			raw:  "Sure, here it is:\n```json\n{\"motion\": \"static\"}\n```",
			check: func(t *testing.T, c Classification) {
				if c.Motion == nil || *c.Motion != "static" {
					t.Errorf("motion: got %v, want static", c.Motion)
				}
			},
		},
		{
			name: "json with trailing prose",
			raw:  `{"pov_container": "handheld"} I hope this helps!`,
			check: func(t *testing.T, c Classification) {
				if c.POVContainer == nil || *c.POVContainer != "handheld" {
					t.Errorf("pov_container: got %v", c.POVContainer)
				}
			},
		},
		{
			name:    "no json at all",
			raw:     "I'm not sure how to answer this.",
			wantErr: true,
		},
		{
			name:    "malformed json",
			raw:     `{"pov_container": "handheld", "pov_altitude": }`,
			wantErr: true,
		},
		{
			name: "missing fields are nil pointers",
			raw:  `{"motion": "static"}`,
			check: func(t *testing.T, c Classification) {
				if c.Motion == nil || *c.Motion != "static" {
					t.Errorf("motion: got %v", c.Motion)
				}
				if c.POVContainer != nil {
					t.Errorf("pov_container should be nil, got %v", c.POVContainer)
				}
				if c.SubjectCategory != nil {
					t.Errorf("subject_category should be nil, got %v", c.SubjectCategory)
				}
			},
		},
		{
			name: "scalar emitted as number — coerce",
			raw:  `{"animal_count": 0, "subject_count": 2}`,
			check: func(t *testing.T, c Classification) {
				if c.AnimalCount == nil || *c.AnimalCount != "0" {
					t.Errorf("animal_count: got %v, want '0'", c.AnimalCount)
				}
				if c.SubjectCount == nil || *c.SubjectCount != "2" {
					t.Errorf("subject_count: got %v, want '2'", c.SubjectCount)
				}
			},
		},
		{
			name: "scalar emitted as single-element array — take first",
			raw:  `{"color_palette": ["cool"], "motion": ["static"]}`,
			check: func(t *testing.T, c Classification) {
				if c.ColorPalette == nil || *c.ColorPalette != "cool" {
					t.Errorf("color_palette: got %v, want 'cool'", c.ColorPalette)
				}
				if c.Motion == nil || *c.Motion != "static" {
					t.Errorf("motion: got %v, want 'static'", c.Motion)
				}
			},
		},
		{
			name: "array field emitted as bare string — wrap",
			raw:  `{"subject_category": "person", "framing": "through_window"}`,
			check: func(t *testing.T, c Classification) {
				if !reflect.DeepEqual(c.SubjectCategory, []string{"person"}) {
					t.Errorf("subject_category: got %v", c.SubjectCategory)
				}
				if !reflect.DeepEqual(c.Framing, []string{"through_window"}) {
					t.Errorf("framing: got %v", c.Framing)
				}
			},
		},
		{
			name: "explicit null fields stay nil",
			raw:  `{"motion": null, "subject_category": null}`,
			check: func(t *testing.T, c Classification) {
				if c.Motion != nil {
					t.Errorf("motion: got %v, want nil", c.Motion)
				}
				if c.SubjectCategory != nil {
					t.Errorf("subject_category: got %v, want nil", c.SubjectCategory)
				}
			},
		},
		{
			name: "empty array yields nil",
			raw:  `{"subject_category": []}`,
			check: func(t *testing.T, c Classification) {
				if c.SubjectCategory != nil {
					t.Errorf("empty array should yield nil, got %v", c.SubjectCategory)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseClassifyResponse(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, c)
			}
		})
	}
}

func TestValidateDropsInvalidScalars(t *testing.T) {
	bogus := "from_helicopter"
	good := "from_plane"
	c := Classification{
		POVContainer: &bogus,
		POVAltitude:  &good, // scalar valid for pov_container but not pov_altitude — should drop
	}
	got := ValidateClassification(c)

	if got.POVContainer != nil {
		t.Errorf("POVContainer should be nil after dropping bogus value, got %v", *got.POVContainer)
	}
	// "from_plane" isn't in pov_altitude's allowed set — should drop
	if got.POVAltitude != nil {
		t.Errorf("POVAltitude should be nil (from_plane not in altitude enum), got %v", *got.POVAltitude)
	}
}

func TestValidatePreservesValidScalars(t *testing.T) {
	g := "ground"
	c := Classification{POVAltitude: &g}
	got := ValidateClassification(c)
	if got.POVAltitude == nil || *got.POVAltitude != "ground" {
		t.Errorf("POVAltitude should remain 'ground', got %v", got.POVAltitude)
	}
}

func TestValidatePreservesUnclear(t *testing.T) {
	u := "unclear"
	c := Classification{
		POVContainer: &u,
		POVAltitude:  &u,
		Motion:       &u,
	}
	got := ValidateClassification(c)
	for name, v := range map[string]*string{
		"POVContainer": got.POVContainer,
		"POVAltitude":  got.POVAltitude,
		"Motion":       got.Motion,
	} {
		if v == nil || *v != "unclear" {
			t.Errorf("%s should remain 'unclear', got %v", name, v)
		}
	}
}

func TestValidateFiltersArrayKeepingValid(t *testing.T) {
	c := Classification{
		SubjectCategory: []string{"person", "spaceship", "landscape"},
		Framing:         []string{"through_window", "made_up_value"},
	}
	got := ValidateClassification(c)
	want := []string{"person", "landscape"}
	if !reflect.DeepEqual(got.SubjectCategory, want) {
		t.Errorf("SubjectCategory: got %v, want %v", got.SubjectCategory, want)
	}
	if !reflect.DeepEqual(got.Framing, []string{"through_window"}) {
		t.Errorf("Framing: got %v", got.Framing)
	}
}

func TestValidateAllInvalidArrayBecomesNil(t *testing.T) {
	c := Classification{
		SubjectCategory: []string{"spaceship", "alien"},
	}
	got := ValidateClassification(c)
	if got.SubjectCategory != nil {
		t.Errorf("all-invalid array should become nil, got %v", got.SubjectCategory)
	}
}

func TestStripLineComments(t *testing.T) {
	cases := []struct {
		name     string
		in, want string
	}{
		{
			name: "trailing comment after value",
			in:   "{\n  \"a\": 1,  // explanation here\n  \"b\": 2\n}",
			want: "{\n  \"a\": 1,  \n  \"b\": 2\n}",
		},
		{
			name: "comment after array",
			in:   "{\"framing\": [\"x\"], // unsure if this applies\n \"motion\": \"static\"}",
			want: "{\"framing\": [\"x\"], \n \"motion\": \"static\"}",
		},
		{
			name: "double-slash inside string is preserved",
			in:   `{"url": "http://example.com/x"}`,
			want: `{"url": "http://example.com/x"}`,
		},
		{
			name: "escaped quote then comment-like text in string preserved",
			in:   `{"x": "she said \"//\""}`,
			want: `{"x": "she said \"//\""}`,
		},
		{
			name: "no comments — passthrough",
			in:   `{"a": 1, "b": [1, 2]}`,
			want: `{"a": 1, "b": [1, 2]}`,
		},
		{
			name: "comment at end of string with no trailing newline",
			in:   `{"a": 1} // tail`,
			want: `{"a": 1} `,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripLineComments(tc.in); got != tc.want {
				t.Errorf("\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestParseClassifyResponseStripsCommentsBeforeDecode(t *testing.T) {
	// Real failing payload shape from Ministral
	raw := "```json\n{\n" +
		`  "pov_container": "handheld",` + "\n" +
		`  "framing": ["unobstructed"],` + "\n" +
		`  "motion": "camera_moving",  // shallow DOF implies slight movement` + "\n" +
		`  "color_palette": "warm"` + "\n" +
		"}\n```"
	c, err := ParseClassifyResponse(raw)
	if err != nil {
		t.Fatalf("expected ParseClassifyResponse to succeed after stripping //, got: %v", err)
	}
	if c.POVContainer == nil || *c.POVContainer != "handheld" {
		t.Errorf("pov_container: got %v", c.POVContainer)
	}
	if c.Motion == nil || *c.Motion != "camera_moving" {
		t.Errorf("motion: got %v", c.Motion)
	}
}

func TestParseClassifyResponseShowsByteOffsetContextOnSyntaxError(t *testing.T) {
	// Mid-JSON garbage character — exactly the shape we saw from Ministral
	// in the wild (a stray "?" between key/value pairs).
	raw := `{
  "pov_container": "fixed_camera",
  "pov_altitude": "ground" ?
  "pov_angle": "eye_level"
}`
	_, err := ParseClassifyResponse(raw)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "at byte ") {
		t.Errorf("error should include byte offset:\n%s", msg)
	}
	if !strings.Contains(msg, "?") {
		t.Errorf("error context should include the offending '?' character:\n%s", msg)
	}
	if !strings.Contains(msg, "^") {
		t.Errorf("error should include a ^ pointer line:\n%s", msg)
	}
}

func TestExtractJSONObjectVariants(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", `{"a":1}`, `{"a":1}`},
		{"with prefix", "Sure! {\"a\":1}", `{"a":1}`},
		{"with suffix", `{"a":1} that's it`, `{"a":1}`},
		{"code fence", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"no braces", "no json here", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractJSONObject(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
