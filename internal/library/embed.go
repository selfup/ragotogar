package library

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// EmbedDim is the embedding dimension produced by nomic-embed-text-v1.5
// (and what cmd/describe/schema.go declares for vector(N)). Changing the
// embedding model requires re-indexing AND swapping this constant.
const EmbedDim = 768

// LMStudioBase reads LM_STUDIO_BASE with the canonical localhost default.
func LMStudioBase() string {
	if v := os.Getenv("LM_STUDIO_BASE"); v != "" {
		return v
	}
	return "http://localhost:1234"
}

// EmbedModel reads EMBED_MODEL with the nomic default.
func EmbedModel() string {
	if v := os.Getenv("EMBED_MODEL"); v != "" {
		return v
	}
	return "text-embedding-nomic-embed-text-v1.5"
}

// SearchModel reads SEARCH_MODEL with the ministral default — used by the
// LLM verify pass in cmd/search.
func SearchModel() string {
	if v := os.Getenv("SEARCH_MODEL"); v != "" {
		return v
	}
	return "mistralai/ministral-3-3b"
}

// ClassifyModel reads CLASSIFY_MODEL with the ministral default — used by
// cmd/classify to map description prose into typed enum fields. Sharing the
// 3B with the verify pass means LM Studio keeps a single text model loaded
// alongside the vision describer.
func ClassifyModel() string {
	if v := os.Getenv("CLASSIFY_MODEL"); v != "" {
		return v
	}
	return "mistralai/ministral-3-3b"
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// EmbedTexts batches an OpenAI-shaped embedding request to LM Studio.
// Returns one float32 slice per input, each of length EmbedDim. Empty
// input yields an empty slice without hitting the network.
func EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Model: EmbedModel(), Input: texts})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		LMStudioBase()+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer lm-studio")

	client := &http.Client{Timeout: 600 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read embed response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var out embedResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("LM Studio embed: %s", out.Error.Message)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embed returned %d vectors for %d inputs", len(out.Data), len(texts))
	}
	results := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		if len(d.Embedding) != EmbedDim {
			return nil, fmt.Errorf("embedding %d has dim %d, want %d", i, len(d.Embedding), EmbedDim)
		}
		results[i] = d.Embedding
	}
	return results, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
