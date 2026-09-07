package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// EmbedDim is the default dimension for the original Qwen3-Embedding-4B
// schema. Runtime code uses EmbeddingDimensions to also support other models.
const EmbedDim = 2560

// ErrEmbeddingDimension is a permanent configuration mismatch. Index workers
// must stop instead of repeating the same failed request for every photo.
var ErrEmbeddingDimension = errors.New("embedding dimension mismatch")

// EmbeddingDimensions reads EMBED_DIM when explicitly set. Qwen3-Embedding
// 0.6B model IDs (including GGUF paths and MLX variants) default to 1024;
// other model IDs retain the original 2560 default. Custom aliases/dimensions
// can use EMBED_DIM. The upper bound matches pgvector's halfvec HNSW limit.
func EmbeddingDimensions() (int, error) {
	dim := EmbedDim
	if strings.Contains(strings.ToLower(EmbedModel()), "qwen3-embedding-0.6b") {
		dim = 1024
	}
	if raw := os.Getenv("EMBED_DIM"); raw != "" {
		var err error
		dim, err = strconv.Atoi(raw)
		if err != nil {
			return 0, fmt.Errorf("%w: EMBED_DIM must be an integer from 1 to 4000", ErrEmbeddingDimension)
		}
	}
	if dim < 1 || dim > 4000 {
		return 0, fmt.Errorf("%w: EMBED_DIM must be from 1 to 4000, got %d", ErrEmbeddingDimension, dim)
	}
	return dim, nil
}

// EmbedModel reads EMBED_MODEL with the text-embedding-qwen3-embedding-4b
// default. The selected model must support the server's /v1/embeddings API.
func EmbedModel() string {
	if v := os.Getenv("EMBED_MODEL"); v != "" {
		return v
	}
	return "text-embedding-qwen3-embedding-4b"
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
	Model    string           `json:"model"`
	Input    []string         `json:"input"`
	Provider *ProviderRouting `json:"provider,omitempty"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     *int      `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// EmbedTexts batches an OpenAI-shaped embedding request to the configured
// embed endpoint (EMBED_ENDPOINT → LM_STUDIO_BASE → localhost). Returns one
// float32 slice per input in input order, each of the configured dimension.
// Empty input yields an empty slice without hitting the network.
//
// Retries up to 5 times with exponential backoff on network errors, 429,
// and 5xx — same policy as LLMComplete via the shared postJSONWithRetry.
func EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	dim, err := EmbeddingDimensions()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(embedRequest{
		Model:    EmbedModel(),
		Input:    texts,
		Provider: DefaultProvider,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}

	raw, err := postJSONWithRetry(ctx,
		EmbedEndpoint()+"/v1/embeddings",
		body,
		map[string]string{"Authorization": "Bearer " + LLMAPIKey()},
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
	indexed := out.Data[0].Index != nil
	for i, d := range out.Data {
		if len(d.Embedding) != dim {
			return nil, fmt.Errorf("%w: embedding %d has dim %d, want %d for EMBED_MODEL=%q (check EMBED_MODEL and EMBED_DIM)", ErrEmbeddingDimension, i, len(d.Embedding), dim, EmbedModel())
		}
		// Providers may return batch results out of order. Honor their input
		// indexes; retain response order for endpoints that omit every index.
		if (d.Index != nil) != indexed {
			return nil, fmt.Errorf("embed response mixes indexed and unindexed vectors")
		}
		position := i
		if indexed {
			position = *d.Index
			if position < 0 || position >= len(results) {
				return nil, fmt.Errorf("embedding %d has invalid index %d", i, position)
			}
		}
		if results[position] != nil {
			return nil, fmt.Errorf("embed response has duplicate index %d", position)
		}
		results[position] = d.Embedding
	}
	return results, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
