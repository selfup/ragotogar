package main

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

// buildPostingBlob writes one term's posting record at the start of a
// fresh buffer in the exact shape cmd/edge_build/fst.go writes: varint
// count + count×varint deltas where deltas accumulate from 0. Used to
// drive DecodePosting tests without depending on a live build artifact.
func buildPostingBlob(t *testing.T, ids []uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	scratch := make([]byte, binary.MaxVarintLen64)

	n := binary.PutUvarint(scratch, uint64(len(ids)))
	buf.Write(scratch[:n])

	var prev uint32
	for _, id := range ids {
		delta := id - prev
		n := binary.PutUvarint(scratch, uint64(delta))
		buf.Write(scratch[:n])
		prev = id
	}
	return buf.Bytes()
}

func TestDecodePosting_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		ids  []uint32
	}{
		{"empty", nil},
		{"single small", []uint32{0}},
		{"single large", []uint32{1_000_000}},
		{"sequential", []uint32{0, 5, 100}},
		{"sparse", []uint32{42, 99, 1_500, 25_000, 1_000_000}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			blob := buildPostingBlob(t, c.ids)
			a := &Artifacts{Postings: blob}
			got, err := a.DecodePosting(0)
			if err != nil {
				t.Fatalf("DecodePosting: %v", err)
			}
			// nil and []uint32{} are equivalent for the empty case;
			// reflect.DeepEqual distinguishes them.
			if len(c.ids) == 0 && len(got) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.ids) {
				t.Fatalf("got %v, want %v", got, c.ids)
			}
		})
	}
}

func TestDecodePosting_OffsetBeyondFile(t *testing.T) {
	a := &Artifacts{Postings: []byte{0x00}}
	if _, err := a.DecodePosting(99); err == nil {
		t.Fatal("expected error for offset beyond postings length")
	}
}

func TestDecodePosting_MalformedCount(t *testing.T) {
	// All-high-bit varint never terminates within the buffer.
	blob := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	a := &Artifacts{Postings: blob}
	if _, err := a.DecodePosting(0); err == nil {
		t.Fatal("expected error for malformed count varint")
	}
}

func TestDecodePosting_TruncatedDeltas(t *testing.T) {
	// count=3 but only one valid delta byte follows.
	blob := append(buildPostingBlob(t, []uint32{0, 5, 100})[:1], 0xff)
	// Replace count byte to claim 3 deltas, then truncate.
	blob[0] = 0x03
	a := &Artifacts{Postings: blob}
	if _, err := a.DecodePosting(0); err == nil {
		t.Fatal("expected error for truncated deltas")
	}
}

// buildPayloadBlob writes a 1-record payload.bin in the exact shape
// cmd/edge_build/payload.go writes: uint32 count + count×uint64 offset
// table + records (varint+bytes per field). Drives DecodePayload tests
// without depending on a live build artifact. Records is a slice of
// (caption, tags) pairs in compact-id order.
func buildPayloadBlob(t *testing.T, records []struct {
	caption string
	tags    [5]string
}) []byte {
	t.Helper()
	scratch := make([]byte, binary.MaxVarintLen64)

	encoded := make([][]byte, len(records))
	for i, r := range records {
		var buf bytes.Buffer
		writeLP := func(s string) {
			n := binary.PutUvarint(scratch, uint64(len(s)))
			buf.Write(scratch[:n])
			buf.WriteString(s)
		}
		writeLP(r.caption)
		for _, tg := range r.tags {
			writeLP(tg)
		}
		encoded[i] = buf.Bytes()
	}

	count := uint32(len(records))
	headerBytes := int64(4) + int64(count)*8
	offsets := make([]uint64, count)
	pos := headerBytes
	for i, rec := range encoded {
		offsets[i] = uint64(pos)
		pos += int64(len(rec))
	}

	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, count)
	for _, off := range offsets {
		binary.Write(&buf, binary.LittleEndian, off)
	}
	for _, rec := range encoded {
		buf.Write(rec)
	}
	return buf.Bytes()
}

func TestDecodePayload_RoundTrip(t *testing.T) {
	type rec = struct {
		caption string
		tags    [5]string
	}
	records := []rec{
		{"first photo subject", [5]string{"in_air", "outdoor", "day", "clear", "ground"}},
		{"", [5]string{}},
		{"unicode — café propeller ✈", [5]string{"a", "b", "c", "d", "e"}},
		{strings.Repeat("a", 500), [5]string{"x", "", "", "", ""}},
	}
	blob := buildPayloadBlob(t, records)

	a := &Artifacts{
		Manifest: &Manifest{
			Payload: PayloadEntry{
				Tags: []string{"subject_altitude", "scene_indoor_outdoor", "scene_time_of_day", "scene_weather", "pov_container"},
			},
		},
		PayloadBytes:   blob,
		PayloadOffsets: make([]uint64, len(records)),
	}
	// Reparse the offset table the way openArtifacts does.
	for i := range a.PayloadOffsets {
		a.PayloadOffsets[i] = binary.LittleEndian.Uint64(blob[4+i*8 : 4+(i+1)*8])
	}

	for i, want := range records {
		gotCap, gotTags, err := a.DecodePayload(uint32(i))
		if err != nil {
			t.Fatalf("DecodePayload(%d): %v", i, err)
		}
		if gotCap != want.caption {
			t.Errorf("cid=%d caption: got %q, want %q", i, gotCap, want.caption)
		}
		if len(gotTags) != 5 {
			t.Fatalf("cid=%d tag count = %d, want 5", i, len(gotTags))
		}
		for j, w := range want.tags {
			if gotTags[j] != w {
				t.Errorf("cid=%d tag[%d]: got %q, want %q", i, j, gotTags[j], w)
			}
		}
	}
}

func TestDecodePayload_OutOfRange(t *testing.T) {
	a := &Artifacts{
		Manifest:       &Manifest{Payload: PayloadEntry{Tags: []string{"a", "b", "c", "d", "e"}}},
		PayloadBytes:   []byte{},
		PayloadOffsets: []uint64{},
	}
	if _, _, err := a.DecodePayload(0); err == nil {
		t.Fatal("expected error for out-of-range cid on empty payload")
	}
}

func TestDecodePayload_OffsetBeyondFile(t *testing.T) {
	a := &Artifacts{
		Manifest:       &Manifest{Payload: PayloadEntry{Tags: []string{"a", "b", "c", "d", "e"}}},
		PayloadBytes:   []byte{0x00},
		PayloadOffsets: []uint64{99},
	}
	if _, _, err := a.DecodePayload(0); err == nil {
		t.Fatal("expected error for offset beyond payload bytes")
	}
}
