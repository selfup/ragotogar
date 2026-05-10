package main

import "math"

// quantizeQueryInt8 mirrors cmd/edge_build/vectors.go quantizeInt8
// byte-exactly. Per locked decision #4 (reimplement, don't share),
// the duplication is intentional — the parity test next door verifies
// known-answer vectors so a future drift in either the build encoder
// or this runtime quantizer surfaces at test time rather than as a
// silent ranking drift.
//
// The asymmetric saturating clamp at ±127 (never -128) matches the
// build side's same choice; cosine = dot(query, row) / 127² requires
// symmetric range to avoid bias.
func quantizeQueryInt8(v []float32) []byte {
	dst := make([]byte, len(v))
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return dst
	}
	scale := 127.0 / math.Sqrt(sum)
	for i, x := range v {
		q := math.Round(float64(x) * scale)
		if q > 127 {
			q = 127
		} else if q < -127 {
			q = -127
		}
		dst[i] = byte(int8(q))
	}
	return dst
}
