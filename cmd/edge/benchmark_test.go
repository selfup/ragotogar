package main

import (
	"encoding/binary"
	"math/rand/v2"
	"testing"
)

// Benchmarks for the cmd/edge query-time hot paths. ScanLane is the
// dominant cost (per EDGE.md, flat int8 over 25K vectors × 2560 dim is
// ~12-30ms on M-series); the rest are micro-optimizations. Run with
// `go test -bench=. -benchmem ./cmd/edge/`.

// BenchmarkQuantizeQueryInt8: called once per /search request. Should
// be sub-microsecond — any regression to milliseconds would show up in
// query latency tail.
func BenchmarkQuantizeQueryInt8_2560Dim(b *testing.B) {
	rng := rand.New(rand.NewPCG(1, 2))
	v := make([]float32, 2560)
	for i := range v {
		v[i] = rng.Float32()*2 - 1
	}
	b.SetBytes(int64(len(v) * 4))
	b.ResetTimer()
	for b.Loop() {
		_ = quantizeQueryInt8(v)
	}
}

// BenchmarkScanLane is the search-time critical path. 10K rows ×
// 2560 dim mirrors a mid-size corpus; the 25K-row real corpus scales
// linearly so this benchmark predicts production cost. b.SetBytes
// reports MB/s — useful for spotting a SIMD opportunity if (e.g.) a
// future Go version inlines the int8 dot.
func BenchmarkScanLane_10kRows_2560Dim(b *testing.B) {
	const rows = 10000
	const dim = 2560
	rng := rand.New(rand.NewPCG(3, 4))

	vectors := make([]byte, rows*dim)
	for i := range vectors {
		vectors[i] = byte(int8(rng.IntN(255) - 127))
	}
	rowMap := make([]uint32, rows)
	for i := range rowMap {
		rowMap[i] = uint32(i) // one row per cid — no MAX-collapse churn
	}
	a := &Artifacts{
		Manifest: &Manifest{Dim: dim},
		Lanes: map[string]*VectorLane{
			"descriptions": {
				Name: "descriptions", Vectors: vectors, RowMap: rowMap, Rows: rows,
			},
		},
	}

	query := make([]byte, dim)
	for i := range query {
		query[i] = byte(int8(rng.IntN(255) - 127))
	}

	b.SetBytes(int64(rows * dim))
	b.ResetTimer()
	for b.Loop() {
		_, err := a.ScanLane("descriptions", query, 0.0)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodePosting_LongList: many cmd/edge queries land on
// high-frequency lexemes whose posting lists span hundreds of cids.
// Decode is varint-driven, so its cost is proportional to list length.
func BenchmarkDecodePosting_1000Cids(b *testing.B) {
	const n = 1000
	// Build a synthetic posting: varint(count) + count×varint(delta=1).
	buf := make([]byte, 0, 16+n*5)
	scratch := make([]byte, binary.MaxVarintLen64)
	sz := binary.PutUvarint(scratch, uint64(n))
	buf = append(buf, scratch[:sz]...)
	for range n {
		sz = binary.PutUvarint(scratch, 1) // delta=1 each step
		buf = append(buf, scratch[:sz]...)
	}
	a := &Artifacts{Postings: buf}

	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for b.Loop() {
		_, err := a.DecodePosting(0)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodePayload: called once per result hit during response
// hydration. 200 results × this benchmark = the payload portion of
// per-request latency.
func BenchmarkDecodePayload(b *testing.B) {
	caption := "a single-engine propeller airplane in flight, climbing"
	tags := [5]string{"in_air", "outdoor", "afternoon", "fair", "from_ground"}

	// Hand-build a single-record payload.bin: count=1, one offset
	// pointing past the 12-byte header, then the record bytes.
	rec := encodePayloadRecord(caption, tags)
	body := make([]byte, 4+8+len(rec))
	binary.LittleEndian.PutUint32(body[:4], 1)
	binary.LittleEndian.PutUint64(body[4:12], 12) // record starts after header
	copy(body[12:], rec)

	a := &Artifacts{
		Manifest: &Manifest{
			Payload: PayloadEntry{Tags: []string{"a", "b", "c", "d", "e"}},
		},
		PayloadBytes:   body,
		PayloadOffsets: []uint64{12},
	}

	b.ResetTimer()
	for b.Loop() {
		_, _, err := a.DecodePayload(0)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEncodePayloadRecord helper — encodePayloadRecord is
// defined in the parallel cmd/edge_build package, but we need it here
// to construct the test payload above. Wrapping the locally-needed
// encoder rather than introducing cross-package coupling.
func encodePayloadRecord(caption string, tags [5]string) []byte {
	scratch := make([]byte, binary.MaxVarintLen64)
	var buf []byte
	write := func(s string) {
		n := binary.PutUvarint(scratch, uint64(len(s)))
		buf = append(buf, scratch[:n]...)
		buf = append(buf, s...)
	}
	write(caption)
	for _, t := range tags {
		write(t)
	}
	return buf
}

// BenchmarkTokenizeQuery: called once per /search request, twice if
// the query has negation. Porter2 stemming is the dominant cost.
func BenchmarkTokenizeQuery(b *testing.B) {
	q := "single-engine propeller airplane flying climbing through scattered clouds at golden hour"
	b.ResetTimer()
	for b.Loop() {
		_ = tokenizeQuery(q)
	}
}

// BenchmarkRRFFuse: cmd/edge has its own RRF implementation distinct
// from library.rrfFuse (cmd/edge fuses LaneHit lists, library fuses
// Result lists). Both deserve their own perf floor.
func BenchmarkRRFFuse_TwoArms_1000Each(b *testing.B) {
	rng := rand.New(rand.NewPCG(5, 6))
	a := make([]LaneHit, 1000)
	c := make([]LaneHit, 1000)
	for i := range a {
		a[i] = LaneHit{CompactID: uint32(rng.IntN(2000)), Similarity: rng.Float64()}
		c[i] = LaneHit{CompactID: uint32(rng.IntN(2000)), Similarity: rng.Float64()}
	}

	b.ResetTimer()
	for b.Loop() {
		_ = RRFFuse(a, c)
	}
}
