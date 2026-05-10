package main

import (
	"encoding/binary"
	"strings"
	"testing"
)

func decodePayloadRecord(t *testing.T, blob []byte) (string, [5]string) {
	t.Helper()
	readLP := func(p []byte) (string, []byte) {
		n, sz := binary.Uvarint(p)
		if sz <= 0 {
			t.Fatalf("bad varint")
		}
		p = p[sz:]
		if uint64(len(p)) < n {
			t.Fatalf("varint claims len %d but only %d bytes remain", n, len(p))
		}
		return string(p[:n]), p[n:]
	}
	cap, p := readLP(blob)
	var tags [5]string
	for i := range tags {
		tags[i], p = readLP(p)
	}
	if len(p) != 0 {
		t.Fatalf("residual %d bytes after decode", len(p))
	}
	return cap, tags
}

func TestEncodePayloadRecord_RoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		caption string
		tags    [5]string
	}{
		{"all empty", "", [5]string{"", "", "", "", ""}},
		{"caption only", "single-engine propeller airplane in flight, climbing", [5]string{"", "", "", "", ""}},
		{"tags only", "", [5]string{"in_air", "outdoor", "day", "clear", "aerial"}},
		{"all populated", "red truck on road", [5]string{"on_ground", "outdoor", "day", "clear", "ground_level"}},
		{"unicode caption", "café — propeller airplane in flight ✈", [5]string{"naïve", "outdoor", "", "", ""}},
		{"long caption forces multi-byte varint", strings.Repeat("a", 500), [5]string{"a", "b", "c", "d", "e"}},
		{"very long caption", strings.Repeat("token ", 5000), [5]string{"x", "", "", "", ""}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			blob := encodePayloadRecord(c.caption, c.tags)
			gotCap, gotTags := decodePayloadRecord(t, blob)
			if gotCap != c.caption {
				t.Errorf("caption: got %q, want %q", gotCap, c.caption)
			}
			if gotTags != c.tags {
				t.Errorf("tags: got %v, want %v", gotTags, c.tags)
			}
		})
	}
}

// Two records with empty fields produce a known minimum size: 6 varints
// of value 0, each one byte → 6 bytes total. Locks the empty-record
// shape so a future varint-format change is caught.
func TestEncodePayloadRecord_EmptyShape(t *testing.T) {
	blob := encodePayloadRecord("", [5]string{})
	if len(blob) != 6 {
		t.Fatalf("empty record size = %d, want 6 (1 caption + 5 tags, each varint(0))", len(blob))
	}
	for i, b := range blob {
		if b != 0 {
			t.Fatalf("empty record byte %d = %d, want 0", i, b)
		}
	}
}

// payloadTagFields is the on-disk contract — readers index by position,
// so the slice order must be append-only. Lock the count and order
// here so a future reorder breaks the test.
func TestPayloadTagFields_Contract(t *testing.T) {
	want := []string{
		"subject_altitude",
		"scene_indoor_outdoor",
		"scene_time_of_day",
		"scene_weather",
		"pov_container",
	}
	if len(payloadTagFields) != len(want) {
		t.Fatalf("payloadTagFields len = %d, want %d", len(payloadTagFields), len(want))
	}
	for i, w := range want {
		if payloadTagFields[i] != w {
			t.Errorf("payloadTagFields[%d] = %q, want %q", i, payloadTagFields[i], w)
		}
	}
}
