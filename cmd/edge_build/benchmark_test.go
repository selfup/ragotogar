package main

import (
	"math/rand/v2"
	"testing"
)

// BenchmarkQuantizeInt8 is the build-side hot path: every halfvec(2560)
// row gets quantized once. A 25K-corpus build calls this 25K times per
// lane × 3 lanes = 75K calls. b.SetBytes lets `go test -bench` report
// throughput so a regression shows up as a smaller MB/s number.
func BenchmarkQuantizeInt8_2560Dim(b *testing.B) {
	rng := rand.New(rand.NewPCG(1, 2))
	v := make([]float32, 2560)
	for i := range v {
		v[i] = rng.Float32()*2 - 1
	}
	dst := make([]byte, 2560)

	b.SetBytes(int64(len(v) * 4)) // 4 bytes per float32 input
	b.ResetTimer()
	for b.Loop() {
		quantizeInt8(v, dst)
	}
}

// BenchmarkEncodePayloadRecord: called once per photo in buildPayload.
// Not as hot as quantizeInt8 (one call per photo, not per chunk), but
// the encoding shape is the wire contract — drift here is silent.
func BenchmarkEncodePayloadRecord(b *testing.B) {
	caption := "a single-engine propeller airplane in flight, climbing through scattered clouds"
	tags := [5]string{"in_air", "outdoor", "afternoon", "fair", "from_ground"}

	b.ResetTimer()
	for b.Loop() {
		_ = encodePayloadRecord(caption, tags)
	}
}
