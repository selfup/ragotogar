package main

import (
	"math/rand/v2"
	"testing"
)

// quantizeQueryInt8 mirrors cmd/edge_build's quantizeInt8 byte-for-byte;
// the property tests mirror too. See cmd/edge_build/quantize_property_test.go
// for the rationale.

func TestProperty_QuantizeQueryInt8_ScaleInvariance(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	const dim = 2560
	v := make([]float32, dim)
	for trial := range 30 {
		for i := range v {
			v[i] = rng.Float32()*2 - 1
		}
		base := quantizeQueryInt8(v)

		for _, scale := range []float32{0.1, 1.0, 100.0, 12345.6} {
			scaled := make([]float32, dim)
			for i, x := range v {
				scaled[i] = x * scale
			}
			got := quantizeQueryInt8(scaled)
			for i := range got {
				if got[i] != base[i] {
					t.Fatalf("trial=%d scale=%v: byte %d diverged (base=%d scaled=%d)",
						trial, scale, i, int8(base[i]), int8(got[i]))
				}
			}
		}
	}
}

func TestProperty_QuantizeQueryInt8_SignFlipSymmetry(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	const dim = 2560
	v := make([]float32, dim)
	neg := make([]float32, dim)
	for trial := range 30 {
		for i := range v {
			v[i] = rng.Float32()*2 - 1
			neg[i] = -v[i]
		}
		dst := quantizeQueryInt8(v)
		dstNeg := quantizeQueryInt8(neg)
		for i := range dst {
			if int8(dst[i]) != -int8(dstNeg[i]) {
				t.Fatalf("trial=%d byte %d: quantize(v)=%d, quantize(-v)=%d",
					trial, i, int8(dst[i]), int8(dstNeg[i]))
			}
		}
	}
}
