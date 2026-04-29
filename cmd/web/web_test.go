package main

import "testing"

func TestResolveMode(t *testing.T) {
	tests := []struct{ in, want string }{
		{"naive", "naive"},
		{"naive-verify", "naive-verify"}, // composes -retrieve -verify against the library
		{"local", "local"},
		{"hybrid", "hybrid"},
		{"", "naive"},        // empty falls back to default
		{"global", "naive"},  // global is rejected (synthesizes summaries, useless for grid)
		{"NAIVE", "naive"},   // case-sensitive
		{"garbage", "naive"}, // unknown rejected
	}
	for _, tt := range tests {
		if got := resolveMode(tt.in); got != tt.want {
			t.Errorf("resolveMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
