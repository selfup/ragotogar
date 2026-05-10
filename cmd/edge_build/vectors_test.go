package main

import (
	"math"
	"math/rand"
	"testing"
)

// L2-normalized cosine on raw fp32 — reference for the int8 fidelity
// comparison.
func cosineFloat32(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	return dot / math.Sqrt(na*nb)
}

// Integer dot of two int8-encoded byte slices, divided by 127² to
// recover an approximation of cosine (assuming both were L2-normalized
// before quantization).
func dotInt8AsCosine(a, b []byte) float64 {
	var sum int64
	for i := range a {
		sum += int64(int8(a[i])) * int64(int8(b[i]))
	}
	return float64(sum) / (127.0 * 127.0)
}

func TestQuantizeInt8_Zero(t *testing.T) {
	v := make([]float32, expectedDim)
	dst := make([]byte, expectedDim)
	quantizeInt8(v, dst)
	for i, b := range dst {
		if b != 0 {
			t.Fatalf("zero vector produced non-zero byte at %d: %d", i, b)
		}
	}
}

func TestQuantizeInt8_Determinism(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	v := make([]float32, expectedDim)
	for i := range v {
		v[i] = rng.Float32()*2 - 1
	}
	a := make([]byte, expectedDim)
	b := make([]byte, expectedDim)
	quantizeInt8(v, a)
	quantizeInt8(v, b)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic at %d: %d vs %d", i, a[i], b[i])
		}
	}
}

// The asymmetric saturating clamp at ±127 is documented behavior — int8
// can technically hold -128, but using it would skew dot products on
// vectors with extreme components (no positive counterpart). Verify
// the function never emits -128 across a range of adversarial inputs.
func TestQuantizeInt8_NeverProducesMinusOneTwentyEight(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	v := make([]float32, expectedDim)
	dst := make([]byte, expectedDim)
	for trial := range 100 {
		for i := range v {
			// Wide range to provoke saturation. Bias toward negative
			// half so we exercise the negative clamp path.
			v[i] = rng.Float32()*200 - 150
		}
		quantizeInt8(v, dst)
		for i, b := range dst {
			if int8(b) == -128 {
				t.Fatalf("trial %d component %d produced -128 (asymmetric clamp violated)", trial, i)
			}
		}
	}
}

// Output bytes interpret as int8 ≥ -127 for any input. int8 is
// inherently ≤ 127 by type so the upper bound is structural; the
// asymmetric -127 floor is the contract worth checking, even on
// pathologically large inputs.
func TestQuantizeInt8_StaysInRange(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	v := make([]float32, expectedDim)
	dst := make([]byte, expectedDim)
	for trial := range 50 {
		for i := range v {
			v[i] = (rng.Float32()*2 - 1) * 1e6
		}
		quantizeInt8(v, dst)
		for i, b := range dst {
			if int8(b) < -127 {
				t.Fatalf("trial %d component %d below -127 floor: %d", trial, i, int8(b))
			}
		}
	}
}

// Cosine-fidelity is the central correctness invariant: int8 dot
// product divided by 127² must approximate fp32 cosine. The threshold
// 0.012 is the realistic worst-case error for symmetric int8
// quantization of 2560-dim uniform-random vectors (max observed across
// 100 trials is ~0.009). Loosen only with evidence — a sudden jump
// here would indicate a bug in the L2-normalization or scaling.
func TestQuantizeInt8_CosineFidelity(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	const trials = 100
	const threshold = 0.012

	var maxErr float64
	for trial := range trials {
		a := make([]float32, expectedDim)
		b := make([]float32, expectedDim)
		for i := range a {
			a[i] = rng.Float32()*2 - 1
			b[i] = rng.Float32()*2 - 1
		}
		cosFP := cosineFloat32(a, b)

		aq := make([]byte, expectedDim)
		bq := make([]byte, expectedDim)
		quantizeInt8(a, aq)
		quantizeInt8(b, bq)
		cosInt := dotInt8AsCosine(aq, bq)

		diff := math.Abs(cosFP - cosInt)
		if diff > maxErr {
			maxErr = diff
		}
		if diff > threshold {
			t.Errorf("trial %d: fp32 cos=%.6f int8 cos=%.6f diff=%.6f", trial, cosFP, cosInt, diff)
		}
	}
	t.Logf("max cosine error across %d trials: %.6f (threshold %.4f)", trials, maxErr, threshold)
}

// Sign preservation: a strictly positive normalized vector must
// quantize to all non-negative bytes; strictly negative to all
// non-positive. Catches sign-bit handling regressions in the int8
// cast.
func TestQuantizeInt8_SignPreservation(t *testing.T) {
	pos := make([]float32, expectedDim)
	neg := make([]float32, expectedDim)
	for i := range pos {
		pos[i] = float32(i + 1)
		neg[i] = -float32(i + 1)
	}
	dst := make([]byte, expectedDim)

	quantizeInt8(pos, dst)
	for i, b := range dst {
		if int8(b) < 0 {
			t.Fatalf("positive vector produced negative byte at %d: %d", i, int8(b))
		}
	}
	quantizeInt8(neg, dst)
	for i, b := range dst {
		if int8(b) > 0 {
			t.Fatalf("negative vector produced positive byte at %d: %d", i, int8(b))
		}
	}
}
