package library

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// Benchmarks for the hot search-math paths in the library package. Run
// with `go test -bench=. -benchmem ./library/`. Not exercised by
// ./test.sh because benchmarks are slow and noisy under CI;
// pre-merge benchstat comparison is the manual workflow for regression
// detection.

// genBenchResults produces N synthetic Results with a small overlap
// alphabet so RRF / merge functions see realistic name collisions
// across multiple input lists. Deterministic via the seeded rng.
func genBenchResults(rng *rand.Rand, n int) []Result {
	out := make([]Result, n)
	for i := range out {
		out[i] = Result{
			Name:       fmt.Sprintf("doc_%d", rng.IntN(n*2)),
			Similarity: rng.Float64(),
		}
	}
	return out
}

func BenchmarkRRFFuse_TwoArms_100Each(b *testing.B) {
	rng := rand.New(rand.NewPCG(1, 2))
	a := genBenchResults(rng, 100)
	c := genBenchResults(rng, 100)

	b.ResetTimer()
	for b.Loop() {
		_ = rrfFuse([][]Result{a, c}, RRFK, 0)
	}
}

func BenchmarkRRFFuse_TwoArms_1000Each(b *testing.B) {
	rng := rand.New(rand.NewPCG(3, 4))
	a := genBenchResults(rng, 1000)
	c := genBenchResults(rng, 1000)

	b.ResetTimer()
	for b.Loop() {
		_ = rrfFuse([][]Result{a, c}, RRFK, 0)
	}
}

// BenchmarkMergeUnion_ThreeStores_1000Each: the dominant cost of the
// merge step in SearchV2's hot path. mergeUnion is the default
// strategy, so its perf floor matters most.
func BenchmarkMergeUnion_ThreeStores_1000Each(b *testing.B) {
	rng := rand.New(rand.NewPCG(5, 6))
	stores := map[string][]Result{
		"descriptions": genBenchResults(rng, 1000),
		"metadata":     genBenchResults(rng, 1000),
		"queries":      genBenchResults(rng, 1000),
	}

	b.ResetTimer()
	for b.Loop() {
		_ = mergeUnion(stores)
	}
}

func BenchmarkMergeIntersect_ThreeStores_1000Each(b *testing.B) {
	rng := rand.New(rand.NewPCG(7, 8))
	stores := map[string][]Result{
		"descriptions": genBenchResults(rng, 1000),
		"metadata":     genBenchResults(rng, 1000),
		"queries":      genBenchResults(rng, 1000),
	}

	b.ResetTimer()
	for b.Loop() {
		_ = mergeIntersect(stores)
	}
}

func BenchmarkMergeWeighted_ThreeStores_1000Each(b *testing.B) {
	rng := rand.New(rand.NewPCG(9, 10))
	stores := map[string][]Result{
		"descriptions": genBenchResults(rng, 1000),
		"metadata":     genBenchResults(rng, 1000),
		"queries":      genBenchResults(rng, 1000),
	}
	opts := SearchOptionsV2{
		WeightDescriptions: 1.0,
		WeightMetadata:     0.5,
		WeightQueries:      2.0,
	}

	b.ResetTimer()
	for b.Loop() {
		_ = mergeWeighted(stores, opts)
	}
}

// BenchmarkStripNegation: called once per query by cmd/web on the hot
// path before retrieval. Optimized only as long as it's invisible in
// the per-request flame graph.
func BenchmarkStripNegation(b *testing.B) {
	q := `red "indoor cafe scene" -monochrome -"black and white" -grayscale -desaturated truck-driver`
	b.ResetTimer()
	for b.Loop() {
		_ = StripNegation(q)
	}
}

func BenchmarkExtractNegation(b *testing.B) {
	q := `red "indoor cafe scene" -monochrome -"black and white" -grayscale -desaturated truck-driver`
	b.ResetTimer()
	for b.Loop() {
		_ = ExtractNegation(q)
	}
}
