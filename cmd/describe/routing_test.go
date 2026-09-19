package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"ragotogar/library"
)

type routingTransport func(*http.Request) (*http.Response, error)

func (f routingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDescribeImageRoutesByVisionEndpoint(t *testing.T) {
	t.Setenv("LLM_API_KEY", "test-key")
	for _, tc := range []struct{ name, specific, fallback, model, want string }{
		{"openrouter", "https://openrouter.ai/api", "http://localhost:1234", "vendor/vision", "vendor/vision:nitro"},
		{"fallback", "", "https://openrouter.ai/api", "vendor/vision", "vendor/vision:nitro"},
		{"already nitro", "https://openrouter.ai/api", "", "vendor/vision:nitro", "vendor/vision:nitro"},
		{"local override", "http://localhost:1234", "https://openrouter.ai/api", "local/vision", "local/vision"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VISION_ENDPOINT", tc.specific)
			t.Setenv("LM_STUDIO_BASE", tc.fallback)
			cfg := config{lmBase: library.VisionEndpoint(), model: tc.model}
			old := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = old })
			calls := 0
			http.DefaultTransport = routingTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				var body chatRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				r.Body.Close()
				if body.Model != tc.want || body.Provider == nil || *body.Provider != *library.DefaultProvider {
					t.Fatalf("unexpected model/provider: %q, %+v", body.Model, body.Provider)
				}
				if r.URL.String() != cfg.lmBase+"/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Fatal("unexpected endpoint or authorization")
				}
				if len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 || body.Messages[0].Content[0].ImageURL.URL != "data:image/jpeg;base64,aW1hZ2U=" {
					t.Fatal("vision content changed")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"Subject: a photo"}}]}`))}, nil
			})
			got, err := describeImage(cfg, "aW1hZ2U=", "camera")
			if err != nil || got != "Subject: a photo" || calls != 1 || cfg.model != tc.model {
				t.Fatalf("got=%q, err=%v, calls=%d, model=%q", got, err, calls, cfg.model)
			}
		})
	}
}
