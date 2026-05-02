package main

import "testing"

func TestParseThreshold(t *testing.T) {
	cases := []struct {
		raw      string
		fallback float64
		want     float64
	}{
		{"", 0.5, 0.5},        // empty → fallback
		{"0", 0.5, 0},         // zero is valid (no filter)
		{"1", 0.5, 1},         // max is valid
		{"0.42", 0.5, 0.42},   // typical value
		{"-0.1", 0.5, 0},      // negative clamps to 0
		{"5", 0.5, 1},         // > 1 clamps to 1
		{"garbage", 0.7, 0.7}, // bad parse → fallback
	}
	for _, tc := range cases {
		if got := parseThreshold(tc.raw, tc.fallback); got != tc.want {
			t.Errorf("parseThreshold(%q, %v) = %v, want %v", tc.raw, tc.fallback, got, tc.want)
		}
	}
}

func TestResolveMode(t *testing.T) {
	tests := []struct{ in, want string }{
		// Valid modes pass through.
		{"naive", "naive"},
		{"naive-verify", "naive-verify"},
		{"fts-vector", "fts-vector"},
		{"fts-vector-verify", "fts-vector-verify"},

		// Empty / unknown / retired LightRAG names fall back to vector.
		{"", "naive"},
		{"global", "naive"},
		{"NAIVE", "naive"},
		{"local", "naive"},  // retired
		{"hybrid", "naive"}, // retired
		{"garbage", "naive"},
	}
	for _, tt := range tests {
		if got := resolveMode(tt.in); got != tt.want {
			t.Errorf("resolveMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestResolveSort(t *testing.T) {
	tests := []struct{ in, want string }{
		{"relevance", "relevance"},
		{"date-desc", "date-desc"},
		{"date-asc", "date-asc"},

		{"", "relevance"},
		{"DATE-DESC", "relevance"}, // case-sensitive on purpose
		{"date_desc", "relevance"}, // wrong separator
		{"newest", "relevance"},    // pretty label, not the wire value
		{"garbage", "relevance"},
	}
	for _, tt := range tests {
		if got := resolveSort(tt.in); got != tt.want {
			t.Errorf("resolveSort(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSortByDate(t *testing.T) {
	// Retrieval order — what comes back from the cosine query.
	in := []result{
		{Name: "A"}, // 2024-04
		{Name: "B"}, // no date
		{Name: "C"}, // 2024-01
		{Name: "D"}, // 2025-06
		{Name: "E"}, // no date
	}
	dates := map[string]string{
		"A": "2024-04-15T10:00:00",
		"C": "2024-01-02T08:30:00",
		"D": "2025-06-21T17:45:00",
	}

	cases := []struct {
		sort string
		want []string
	}{
		// relevance leaves the slice untouched (caller short-circuits before
		// sortByDate runs, but the function is still defined for that case).
		{"relevance", []string{"A", "B", "C", "D", "E"}},

		// Newest first; B and E (no date) tail in retrieval order.
		{"date-desc", []string{"D", "A", "C", "B", "E"}},

		// Oldest first; B and E still tail (NULL is "unknown", not "old").
		{"date-asc", []string{"C", "A", "D", "B", "E"}},
	}
	for _, tc := range cases {
		got := sortByDate(in, dates, tc.sort)
		if len(got) != len(tc.want) {
			t.Errorf("sort=%s: len = %d, want %d", tc.sort, len(got), len(tc.want))
			continue
		}
		for i, name := range tc.want {
			if got[i].Name != name {
				gotNames := make([]string, len(got))
				for i, r := range got {
					gotNames[i] = r.Name
				}
				t.Errorf("sort=%s: order = %v, want %v", tc.sort, gotNames, tc.want)
				break
			}
		}
	}
}

func TestSortByDate_StableForTies(t *testing.T) {
	// Two photos with identical dates should keep retrieval order — the
	// stable sort guarantees the cosine-better candidate stays first.
	in := []result{{Name: "first"}, {Name: "second"}}
	dates := map[string]string{
		"first":  "2024-04-15T10:00:00",
		"second": "2024-04-15T10:00:00",
	}
	got := sortByDate(in, dates, "date-desc")
	if got[0].Name != "first" || got[1].Name != "second" {
		t.Errorf("stable sort broke tie order: got %v, want [first second]",
			[]string{got[0].Name, got[1].Name})
	}
}

func TestSortByDate_DoesNotMutateInput(t *testing.T) {
	in := []result{{Name: "A"}, {Name: "B"}}
	dates := map[string]string{
		"A": "2025-01-01T00:00:00",
		"B": "2024-01-01T00:00:00",
	}
	_ = sortByDate(in, dates, "date-desc")
	// applySort callers reuse the input slice — the function must not
	// reorder in place or downstream rendering would see surprising state.
	if in[0].Name != "A" || in[1].Name != "B" {
		t.Errorf("sortByDate mutated input: %v", []string{in[0].Name, in[1].Name})
	}
}
