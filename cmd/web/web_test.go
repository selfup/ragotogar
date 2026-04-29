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
