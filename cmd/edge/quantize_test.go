package main

import (
	"math"
	"math/rand"
	"testing"
)

// Known-answer test vectors. The expected outputs were derived from
// the locked contract: int8 = round(x * 127 / |v|₂), asymmetric clamp
// at ±127, byte(int8(q)) for storage. Mirrors cmd/edge_build's
// quantizeInt8 exactly — a future drift in EITHER function fails this
// (provided the matching test in cmd/edge_build/vectors_test.go is
// also run; both binaries' tests share the same contract).
func TestQuantizeQueryInt8_KnownVectors(t *testing.T) {
	cases := []struct {
		name string
		in   []float32
		want []int8
	}{
		{"unit x", []float32{1, 0, 0, 0}, []int8{127, 0, 0, 0}},
		{"unit -x", []float32{-1, 0, 0, 0}, []int8{-127, 0, 0, 0}},
		{"unit y", []float32{0, 1, 0, 0}, []int8{0, 127, 0, 0}},
		{"all zeros stays zero", []float32{0, 0, 0, 0}, []int8{0, 0, 0, 0}},
		{"non-unit scaled", []float32{100, 0, 0, 0}, []int8{127, 0, 0, 0}}, // any single-axis positive normalizes to ±1 → ±127
		{"two axes", []float32{1, 1, 0, 0}, []int8{90, 90, 0, 0}},          // 1/sqrt(2) ≈ 0.7071 × 127 ≈ 89.8 → round to 90
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := quantizeQueryInt8(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("len got=%d want=%d", len(got), len(c.want))
			}
			for i := range got {
				if int8(got[i]) != c.want[i] {
					t.Errorf("[%d] got %d, want %d (input %g)", i, int8(got[i]), c.want[i], c.in[i])
				}
			}
		})
	}
}

// Saturating clamp must never produce -128 for any input — would skew
// dot products on extreme components. Sweep over many random inputs.
func TestQuantizeQueryInt8_NeverProducesMinusOneTwentyEight(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	v := make([]float32, 2560)
	for trial := range 50 {
		for i := range v {
			v[i] = rng.Float32()*200 - 150
		}
		got := quantizeQueryInt8(v)
		for i, b := range got {
			if int8(b) == -128 {
				t.Fatalf("trial %d component %d produced -128", trial, i)
			}
		}
	}
}

// Determinism: same input twice produces identical bytes.
func TestQuantizeQueryInt8_Determinism(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	v := make([]float32, 2560)
	for i := range v {
		v[i] = rng.Float32()*2 - 1
	}
	a := quantizeQueryInt8(v)
	b := quantizeQueryInt8(v)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic at %d: %d vs %d", i, a[i], b[i])
		}
	}
}

// Sanity check the math: a vector quantized then dot-producted with
// itself should yield ~127² total. After L2-normalization the
// fp32 sum-of-squares is 1; scaling each component by 127 makes the
// int8 sum-of-squares ≈ 127² × 1 = 16129. Quantization rounding
// introduces a small error; 10% slack catches a real regression
// without flaking on RNG variance.
func TestQuantizeQueryInt8_SelfDot(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	v := make([]float32, 2560)
	for i := range v {
		v[i] = rng.Float32()*2 - 1
	}
	q := quantizeQueryInt8(v)

	var dot int64
	for i := range q {
		x := int64(int8(q[i]))
		dot += x * x
	}
	expected := 127.0 * 127.0
	got := float64(dot)
	if math.Abs(got-expected)/expected > 0.10 {
		t.Errorf("self-dot = %.0f, expected ~%.0f (within 10%%)", got, expected)
	}
}
