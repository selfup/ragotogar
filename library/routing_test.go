package library

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestModelForEndpoint(t *testing.T) {
	for _, tc := range []struct{ endpoint, model, want string }{
		{"https://openrouter.ai/api", "vendor/model", "vendor/model:nitro"},
		{"https://OPENROUTER.AI:443/api", "vendor/model", "vendor/model:nitro"},
		{"https://api.openrouter.ai/api", "vendor/model", "vendor/model:nitro"},
		{"https://openrouter.ai./api", "vendor/model", "vendor/model:nitro"},
		{"https://openrouter.ai/api", "vendor/model:nitro", "vendor/model:nitro"},
		{"https://openrouter.ai/api", "vendor/model:free", "vendor/model:free:nitro"},
		{"https://openrouter.ai/api", "vendor/model:nitro:floor", "vendor/model:floor:nitro"},
		{"https://openrouter.ai/api", "vendor/model:nitro:nitro", "vendor/model:nitro"},
		{"https://openrouter.ai/api", "", ""},
		{"http://localhost:1234", "local/model:Q4_K_M", "local/model:Q4_K_M"},
		{"https://api.openai.com", "vendor/model", "vendor/model"},
		{"https://openrouter.ai.example/api", "vendor/model", "vendor/model"},
		{"https://notopenrouter.ai/api", "vendor/model", "vendor/model"},
		{"https://example.com/openrouter.ai?target=openrouter.ai", "vendor/model", "vendor/model"},
		{"https://openrouter.ai@example.com/api", "vendor/model", "vendor/model"},
		{"openrouter.ai/api", "vendor/model", "vendor/model"},
		{"https://[invalid", "vendor/model", "vendor/model"},
	} {
		t.Run(tc.endpoint+"/"+tc.model, func(t *testing.T) {
			if got := ModelForEndpoint(tc.endpoint, tc.model); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if got := ModelForEndpoint(tc.endpoint, tc.want); got != tc.want {
				t.Fatalf("not idempotent: %q", got)
			}
		})
	}
}

type routingTransport func(*http.Request) (*http.Response, error)

func (f routingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Intercept the real request paths without contacting OpenRouter. These tests
// cannot run in parallel because the production clients use DefaultTransport.
func TestInferenceRequestsRouteByResolvedEndpoint(t *testing.T) {
	t.Setenv("EMBED_MODEL", "vendor/model")
	t.Setenv("EMBED_DIM", "2")
	t.Setenv("LLM_API_KEY", "test-key")
	for _, tc := range []struct{ name, specific, fallback, endpoint, model string }{
		{"openrouter", "https://openrouter.ai/api", "http://localhost:1234", "https://openrouter.ai/api", "vendor/model:nitro"},
		{"fallback", "", "https://openrouter.ai/api", "https://openrouter.ai/api", "vendor/model:nitro"},
		{"local override", "http://localhost:1234", "https://openrouter.ai/api", "http://localhost:1234", "vendor/model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TEXT_ENDPOINT", tc.specific)
			t.Setenv("EMBED_ENDPOINT", tc.specific)
			t.Setenv("LM_STUDIO_BASE", tc.fallback)
			old := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = old })
			calls := 0
			http.DefaultTransport = routingTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				var body struct {
					Model    string          `json:"model"`
					Provider ProviderRouting `json:"provider"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				r.Body.Close()
				if body.Model != tc.model || body.Provider != *DefaultProvider {
					t.Fatalf("unexpected routing body: %+v", body)
				}
				path := "/v1/chat/completions"
				response := `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`
				if strings.HasSuffix(r.URL.Path, "/embeddings") {
					path = "/v1/embeddings"
					response = `{"data":[{"index":0,"embedding":[0.1,0.2]}]}`
				}
				if r.URL.String() != tc.endpoint+path || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Fatalf("unexpected endpoint or authorization")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
			})
			if _, err := LLMComplete(t.Context(), "vendor/model", "hello"); err != nil {
				t.Fatal(err)
			}
			if _, err := LLMCompleteSchema(t.Context(), "vendor/model", "hello", "test", map[string]any{"type": "object"}); err != nil {
				t.Fatal(err)
			}
			if _, err := llmCompleteWithLimit(t.Context(), "vendor/model", "hello", nil, 256); err != nil {
				t.Fatal(err)
			}
			if _, err := EmbedTexts(t.Context(), []string{"hello"}); err != nil {
				t.Fatal(err)
			}
			if calls != 4 || EmbedModel() != "vendor/model" {
				t.Fatalf("calls=%d, configured embedding identity=%q", calls, EmbedModel())
			}
		})
	}
}
