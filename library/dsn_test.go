package library

import "testing"

func TestMaskDSN(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "neon-style hosted DSN with password",
			in:   "postgresql://neondb_owner:npg_secret@ep.neon.tech/neondb?sslmode=require",
			want: "postgresql://neondb_owner:",
		},
		{
			name: "vanilla URL form with password and port",
			in:   "postgres://alice:secret@db.example.com:5432/ragotogar",
			want: "postgres://alice:",
		},
		{
			name: "local socket default — only one colon, passes through",
			in:   "postgres:///ragotogar",
			want: "postgres:///ragotogar",
		},
		{
			name: "URL form without password — only one colon, passes through",
			in:   "postgres://alice@host/db",
			want: "postgres://alice@host/db",
		},
		{
			name: "URL form with port but no password — cut at port colon (no secret lost)",
			in:   "postgres://host:5432/db",
			want: "postgres://host:",
		},
		{
			name: "keyword form — no scheme colon, passes through",
			in:   "host=h user=alice password=secret dbname=db",
			want: "host=h user=alice password=secret dbname=db",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
		{
			name: "no colons at all",
			in:   "garbage-input",
			want: "garbage-input",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MaskDSN(tc.in); got != tc.want {
				t.Errorf("MaskDSN(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
