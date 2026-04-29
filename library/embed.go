package library

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// EmbedDim is the embedding dimension produced by nomic-embed-text-v1.5
// (and what cmd/describe/schema.go declares for vector(N)). Changing the
// embedding model requires re-indexing AND swapping this constant.
const EmbedDim = 768

// LMStudioBase reads LM_STUDIO_BASE with the canonical localhost default.
//
// Deprecated: prefer VisionEndpoint / TextEndpoint / EmbedEndpoint, which
// fall back to LM_STUDIO_BASE but allow per-stage overrides for cloud
// deployments where each model lives behind a different URL. Kept for
// display strings in cmd/index, cmd/classify that print the active host.
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

// EmbedTexts batches an OpenAI-shaped embedding request to the configured
// embed endpoint (EMBED_ENDPOINT → LM_STUDIO_BASE → localhost). Returns one
// float32 slice per input, each of length EmbedDim. Empty input yields an
// empty slice without hitting the network.
//
// Retries up to 5 times with exponential backoff on network errors, 429,
// and 5xx — same policy as LLMComplete via the shared postJSONWithRetry.
func EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Model: EmbedModel(), Input: texts})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}

	raw, err := postJSONWithRetry(ctx,
		EmbedEndpoint()+"/v1/embeddings",
		body,
		map[string]string{"Authorization": "Bearer lm-studio"},
	)
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}

	var out embedResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("embed API error: %s", out.Error.Message)
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
