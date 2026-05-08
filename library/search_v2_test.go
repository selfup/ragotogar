package library

import (
	"reflect"
	"strings"
	"testing"
)

// TestMergeUnion: a photo's similarity is the max across the stores it
// appeared in; ordering is similarity DESC.
func TestMergeUnion(t *testing.T) {
	in := map[string][]Result{
		"descriptions": {
			{Name: "p1", Similarity: 0.9},
			{Name: "p2", Similarity: 0.5},
		},
		"metadata": {
			{Name: "p2", Similarity: 0.8}, // p2 best match is in metadata
			{Name: "p3", Similarity: 0.6},
		},
		"queries": {
			{Name: "p1", Similarity: 0.4},
			{Name: "p4", Similarity: 0.7},
		},
	}
	got := mergeUnion(in)

	wantOrder := []string{"p1", "p2", "p4", "p3"}
	if names := resultNames(got); !reflect.DeepEqual(names, wantOrder) {
		t.Errorf("union order: got %v, want %v", names, wantOrder)
	}
	wantSims := map[string]float64{"p1": 0.9, "p2": 0.8, "p3": 0.6, "p4": 0.7}
	for _, r := range got {
		if r.Similarity != wantSims[r.Name] {
			t.Errorf("union %s sim = %v, want %v (max-across-stores)", r.Name, r.Similarity, wantSims[r.Name])
		}
	}
}

// TestMergeIntersect: only photos appearing in every enabled store
// survive; score is the per-store mean similarity.
func TestMergeIntersect(t *testing.T) {
	in := map[string][]Result{
		"descriptions": {
			{Name: "p1", Similarity: 0.9},
			{Name: "p2", Similarity: 0.5},
			{Name: "p3", Similarity: 0.8}, // p3 not in queries → drops
		},
		"metadata": {
			{Name: "p1", Similarity: 0.6},
			{Name: "p2", Similarity: 0.7},
		},
		"queries": {
			{Name: "p1", Similarity: 0.3},
			{Name: "p2", Similarity: 0.9},
		},
	}
	got := mergeIntersect(in)

	wantOrder := []string{"p2", "p1"} // p2 mean=(0.5+0.7+0.9)/3=0.7; p1 mean=(0.9+0.6+0.3)/3=0.6
	if names := resultNames(got); !reflect.DeepEqual(names, wantOrder) {
		t.Errorf("intersect order: got %v, want %v", names, wantOrder)
	}
	if g, w := got[0].Similarity, 0.7; !approxEqual(g, w) {
		t.Errorf("intersect p2 sim = %v, want %v", g, w)
	}
	if g, w := got[1].Similarity, 0.6; !approxEqual(g, w) {
		t.Errorf("intersect p1 sim = %v, want %v", g, w)
	}
}

// TestMergeIntersectDropsPhotosMissingFromAnyStore guards the contract.
func TestMergeIntersectDropsPhotosMissingFromAnyStore(t *testing.T) {
	in := map[string][]Result{
		"descriptions": {{Name: "only_in_desc", Similarity: 1.0}},
		"metadata":     {},
		"queries":      {{Name: "only_in_desc", Similarity: 1.0}},
	}
	got := mergeIntersect(in)
	if len(got) != 0 {
		t.Errorf("expected empty intersect (photo missing from metadata), got %v", got)
	}
}

// TestMergeWeighted: each store contributes (similarity * weight); photos
// appearing in multiple stores naturally rise via the sum.
func TestMergeWeighted(t *testing.T) {
	in := map[string][]Result{
		"descriptions": {
			{Name: "p1", Similarity: 0.5},
			{Name: "p2", Similarity: 0.8},
		},
		"metadata": {
			{Name: "p1", Similarity: 0.5},
			{Name: "p3", Similarity: 0.9},
		},
		"queries": {
			{Name: "p1", Similarity: 0.5},
			{Name: "p4", Similarity: 0.95},
		},
	}
	opts := SearchOptionsV2{
		WeightDescriptions: 1.0,
		WeightMetadata:     1.0,
		WeightQueries:      1.0,
	}
	got := mergeWeighted(in, opts)

	// p1 in all three at 0.5 each → 1.5
	// p4 in queries only at 0.95 → 0.95
	// p3 in metadata only at 0.9 → 0.9
	// p2 in descriptions only at 0.8 → 0.8
	wantOrder := []string{"p1", "p4", "p3", "p2"}
	if names := resultNames(got); !reflect.DeepEqual(names, wantOrder) {
		t.Errorf("weighted order: got %v, want %v", names, wantOrder)
	}
	if !approxEqual(got[0].Similarity, 1.5) {
		t.Errorf("weighted p1 score = %v, want 1.5", got[0].Similarity)
	}
}

// TestMergeWeightedRespectsWeights: skewing toward queries promotes a
// queries-only photo above one that scored higher in descriptions.
func TestMergeWeightedRespectsWeights(t *testing.T) {
	in := map[string][]Result{
		"descriptions": {{Name: "desc_strong", Similarity: 0.9}},
		"queries":      {{Name: "q_strong", Similarity: 0.6}},
	}
	opts := SearchOptionsV2{
		WeightDescriptions: 0.5,
		WeightQueries:      2.0,
	}
	got := mergeWeighted(in, opts)
	wantOrder := []string{"q_strong", "desc_strong"} // 0.6*2.0=1.2 vs 0.9*0.5=0.45
	if names := resultNames(got); !reflect.DeepEqual(names, wantOrder) {
		t.Errorf("weighted-with-bias order: got %v, want %v", names, wantOrder)
	}
}

// TestMergeStoresUnknownStrategyFallsBackToUnion: mergeStores treats an
// unknown MergeStrategy as MergeUnion to keep the search path forgiving
// against malformed UI input.
func TestMergeStoresUnknownStrategyFallsBackToUnion(t *testing.T) {
	in := map[string][]Result{
		"descriptions": {{Name: "p1", Similarity: 0.7}},
		"metadata":     {{Name: "p2", Similarity: 0.9}},
	}
	opts := SearchOptionsV2{MergeStrategy: "bogus-strategy"}
	got := mergeStores(in, opts)
	if names := resultNames(got); !reflect.DeepEqual(names, []string{"p2", "p1"}) {
		t.Errorf("unknown strategy should fall back to union, got %v", names)
	}
}

// TestBuildVerifyTextV2 covers the verifier's text composition under
// each toggle combination. Queries are always excluded per locked
// decision (verifier never sees its own training-target text).
func TestBuildVerifyTextV2(t *testing.T) {
	p := &Photo{
		Name:            "v",
		FileBasename:    "v.JPG",
		Vantage:         "low handheld",
		FullDescription: "a photo of a kitten",
		CameraMake:      "FUJIFILM",
		CameraModel:     "X100VI",
		GeneratedQueries: []string{
			"this should never appear",
			"in the verifier text",
		},
	}
	cases := []struct {
		name   string
		opts   SearchOptionsV2
		mustHave []string
		mustNotHave []string
	}{
		{
			name:        "descriptions only",
			opts:        SearchOptionsV2{UseDescriptions: true},
			mustHave:    []string{"Vantage: low handheld", "a photo of a kitten"},
			mustNotHave: []string{"FUJIFILM", "X100VI", "this should never appear"},
		},
		{
			name:        "metadata only",
			opts:        SearchOptionsV2{UseMetadata: true},
			mustHave:    []string{"FUJIFILM", "X100VI"},
			mustNotHave: []string{"Vantage:", "kitten", "this should never appear"},
		},
		{
			name:        "descriptions + metadata",
			opts:        SearchOptionsV2{UseDescriptions: true, UseMetadata: true},
			mustHave:    []string{"Vantage: low handheld", "FUJIFILM", "X100VI", "kitten"},
			mustNotHave: []string{"this should never appear"},
		},
		{
			name:        "queries toggle is ignored — never appears in verifier text",
			opts:        SearchOptionsV2{UseDescriptions: true, UseQueries: true},
			mustHave:    []string{"kitten"},
			mustNotHave: []string{"this should never appear", "FUJIFILM"},
		},
		{
			name: "all off — empty doc",
			opts: SearchOptionsV2{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildVerifyTextV2(p, tc.opts)
			for _, want := range tc.mustHave {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q\n--- doc ---\n%s\n", want, got)
				}
			}
			for _, banned := range tc.mustNotHave {
				if strings.Contains(got, banned) {
					t.Errorf("leaked %q\n--- doc ---\n%s\n", banned, got)
				}
			}
		})
	}
}

func TestDefaultSearchOptionsV2(t *testing.T) {
	opts := DefaultSearchOptionsV2()
	if !opts.UseDescriptions || !opts.UseMetadata || !opts.UseQueries {
		t.Errorf("defaults should enable all stores: %+v", opts)
	}
	if opts.MergeStrategy != MergeUnion {
		t.Errorf("default strategy = %q, want %q", opts.MergeStrategy, MergeUnion)
	}
	if opts.WeightDescriptions != 1.0 || opts.WeightMetadata != 1.0 || opts.WeightQueries != 1.0 {
		t.Errorf("default weights should be 1.0 each: %+v", opts)
	}
	if opts.TopK != DefaultTopK {
		t.Errorf("default TopK = %d, want %d", opts.TopK, DefaultTopK)
	}
}

// helpers

func resultNames(rs []Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

func approxEqual(a, b float64) bool {
	const eps = 1e-9
	d := a - b
	return d < eps && d > -eps
}

