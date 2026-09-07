package library

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbeddingDimensions(t *testing.T) {
	for _, tc := range []struct {
		model, override string
		want            int
	}{
		{"", "", 2560},
		{"text-embedding-qwen3-embedding-4b", "", 2560},
		{"text-embedding-qwen3-embedding-0.6b", "", 1024},
		{"qwen3-embedding-0.6b-dwq", "", 1024},
		{"/models/Qwen3-Embedding-0.6B-GGUF/model.gguf", "", 1024},
		{"custom-alias", "1024", 1024},
		{"custom-alias", "768", 768},
		{"qwen3-embedding-0.6b", "512", 512},
		{"model", "0", 0},
		{"model", "-1", 0},
		{"model", "4001", 0},
		{"model", "abc", 0},
	} {
		t.Run(tc.model+"/"+tc.override, func(t *testing.T) {
			t.Setenv("EMBED_MODEL", tc.model)
			t.Setenv("EMBED_DIM", tc.override)
			got, err := EmbeddingDimensions()
			if tc.want == 0 {
				if !errors.Is(err, ErrEmbeddingDimension) {
					t.Errorf("expected configuration error, got %v", err)
				}
			} else if err != nil || got != tc.want {
				t.Errorf("dim=%d err=%v, want %d", got, err, tc.want)
			}
		})
	}
}

func TestEmbedTextsValidatesConfiguredDimension(t *testing.T) {
	for _, dim := range []int{1024, 2560} {
		t.Setenv("EMBED_MODEL", "text-embedding-qwen3-embedding-0.6b")
		t.Setenv("EMBED_DIM", "")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": make([]float32, dim)}}})
		}))
		t.Setenv("EMBED_ENDPOINT", srv.URL)
		vectors, err := EmbedTexts(t.Context(), []string{"a photo"})
		srv.Close()
		if dim == 1024 {
			if err != nil || len(vectors) != 1 || len(vectors[0]) != 1024 {
				t.Fatalf("0.6B embedding failed: %v", err)
			}
		} else if !errors.Is(err, ErrEmbeddingDimension) {
			t.Errorf("wrong dimension should be fatal, got %v", err)
		}
	}
}
