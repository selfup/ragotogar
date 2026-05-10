package main

import (
	"math/rand/v2"
	"testing"
)

// quantizeInt8 L2-normalizes before scaling, so multiplying the input by
// any positive scalar must not change the output bytes — magnitude
// information is intentionally discarded. A regression that skips the
// normalization step would fail this property even though the
// example-based tests can't see it.
func TestProperty_QuantizeInt8_ScaleInvariance(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	v := make([]float32, expectedDim)
	for trial := range 30 {
		for i := range v {
			v[i] = rng.Float32()*2 - 1
		}
		base := make([]byte, expectedDim)
		quantizeInt8(v, base)

		// Try a handful of positive scalings.
		for _, scale := range []float32{0.1, 1.0, 100.0, 12345.6} {
			scaled := make([]float32, expectedDim)
			for i, x := range v {
				scaled[i] = x * scale
			}
			out := make([]byte, expectedDim)
			quantizeInt8(scaled, out)
			for i := range out {
				if out[i] != base[i] {
					t.Fatalf("trial=%d scale=%v: byte %d diverged (base=%d scaled=%d)",
						trial, scale, i, int8(base[i]), int8(out[i]))
				}
			}
		}
	}
}

// Negating the input must produce the byte-wise negation of the output
// (modulo the asymmetric clamp at ±127 — which the function provably
// never reaches at -128 by TestQuantizeInt8_NeverProducesMinusOneTwentyEight).
// Catches sign-bit handling bugs in the int8 cast.
func TestProperty_QuantizeInt8_SignFlipSymmetry(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	v := make([]float32, expectedDim)
	neg := make([]float32, expectedDim)
	for trial := range 30 {
		for i := range v {
			v[i] = rng.Float32()*2 - 1
			neg[i] = -v[i]
		}
		dst := make([]byte, expectedDim)
		quantizeInt8(v, dst)
		dstNeg := make([]byte, expectedDim)
		quantizeInt8(neg, dstNeg)

		for i := range dst {
			if int8(dst[i]) != -int8(dstNeg[i]) {
				t.Fatalf("trial=%d byte %d: quantize(v)=%d, quantize(-v)=%d (negation broken)",
					trial, i, int8(dst[i]), int8(dstNeg[i]))
			}
		}
	}
}
