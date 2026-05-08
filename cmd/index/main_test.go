package main

import (
	"strings"
	"testing"
)

// TestParseReindex covers the v12 -reindex flag's comma-separated subset
// parser. Replaces v11's bool flag — the new shape lets prompt/model
// changes touching one store invalidate just that store's rows without
// re-embedding the others. Unknown store names are a hard error so a
// typo (-reindex=desciptions) doesn't silently no-op.
func TestParseReindex(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		want      reindexSet
		wantErr   bool
		errSubstr string
	}{
		{
			name: "empty — no reindex",
			in:   "",
			want: reindexSet{},
		},
		{
			name: "whitespace only — treated as empty",
			in:   "   ",
			want: reindexSet{},
		},
		{
			name: "single store",
			in:   "descriptions",
			want: reindexSet{descriptions: true},
		},
		{
			name: "two stores",
			in:   "descriptions,queries",
			want: reindexSet{descriptions: true, queries: true},
		},
		{
			name: "all three v2 stores",
			in:   "descriptions,metadata,queries",
			want: reindexSet{descriptions: true, metadata: true, queries: true},
		},
		{
			name:      "legacy chunks token rejected post-v14",
			in:        "descriptions,chunks",
			wantErr:   true,
			errSubstr: `unknown store "chunks"`,
		},
		{
			name: "trailing comma is tolerated",
			in:   "descriptions,",
			want: reindexSet{descriptions: true},
		},
		{
			name: "internal whitespace around tokens",
			in:   " descriptions , queries ",
			want: reindexSet{descriptions: true, queries: true},
		},
		{
			name: "duplicate token is idempotent",
			in:   "descriptions,descriptions",
			want: reindexSet{descriptions: true},
		},
		{
			name:      "unknown token is an error",
			in:        "desciptions",
			wantErr:   true,
			errSubstr: `unknown store "desciptions"`,
		},
		{
			name:      "valid token alongside unknown still errors",
			in:        "descriptions,bogus",
			wantErr:   true,
			errSubstr: `unknown store "bogus"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseReindex(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (got=%+v)", tc.errSubstr, got)
				}
				if !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("error %q missing substring %q", err.Error(), tc.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
