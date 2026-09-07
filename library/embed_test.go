package library

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestEmbedTextsResponseIndexes(t *testing.T) {
	index := func(n int) *int { return &n }
	for _, tc := range []struct {
		name    string
		indexes []*int
		want    []float32
		wantErr string
	}{
		{"ordered", []*int{index(0), index(1), index(2)}, []float32{1, 2, 3}, ""},
		{"reordered", []*int{index(2), index(0), index(1)}, []float32{2, 3, 1}, ""},
		{"unindexed_compatibility", []*int{nil, nil, nil}, []float32{1, 2, 3}, ""},
		{"duplicate", []*int{index(0), index(0), index(2)}, nil, "duplicate index"},
		{"negative", []*int{index(-1), index(0), index(1)}, nil, "invalid index"},
		{"out_of_range", []*int{index(0), index(1), index(3)}, nil, "invalid index"},
		{"partially_indexed", []*int{nil, index(1), index(2)}, nil, "mixes indexed and unindexed"},
		{"partially_unindexed", []*int{index(0), nil, index(2)}, nil, "mixes indexed and unindexed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req embedRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if !reflect.DeepEqual(req.Input, []string{"first", "second", "third"}) {
					t.Errorf("inputs changed: %v", req.Input)
				}
				data := make([]map[string]any, len(tc.indexes))
				for i, position := range tc.indexes {
					vector := make([]float32, EmbedDim)
					vector[0] = float32(i + 1)
					data[i] = map[string]any{"embedding": vector}
					if position != nil {
						data[i]["index"] = *position
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer srv.Close()
			t.Setenv("EMBED_ENDPOINT", srv.URL)
			vectors, err := EmbedTexts(context.Background(), []string{"first", "second", "third"})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for i, want := range tc.want {
				if vectors[i][0] != want {
					t.Errorf("input %d has vector marker %g, want %g", i, vectors[i][0], want)
				}
			}
		})
	}
}
