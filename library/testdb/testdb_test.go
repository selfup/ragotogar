package testdb

import "testing"

func TestRewriteDBName(t *testing.T) {
	tests := []struct {
		name, dsn, newDB, want string
	}{
		{
			name:  "path style",
			dsn:   "postgres:///ragotogar",
			newDB: "postgres",
			want:  "postgres:///postgres",
		},
		{
			name:  "url style with host and port",
			dsn:   "postgres://localhost:5432/ragotogar",
			newDB: "postgres",
			want:  "postgres://localhost:5432/postgres",
		},
		{
			name:  "url style with user and password",
			dsn:   "postgres://user:pass@localhost:5432/olddb",
			newDB: "newdb",
			want:  "postgres://user:pass@localhost:5432/newdb",
		},
		{
			name:  "trailing slash appends",
			dsn:   "postgres:///",
			newDB: "newdb",
			want:  "postgres:///newdb",
		},
		{
			name:  "no slash appends verbatim (edge)",
			dsn:   "postgres:",
			newDB: "newdb",
			want:  "postgres:newdb",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RewriteDBName(tc.dsn, tc.newDB)
			if got != tc.want {
				t.Errorf("RewriteDBName(%q, %q) = %q, want %q", tc.dsn, tc.newDB, got, tc.want)
			}
		})
	}
}
