package library

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ── endpoint env precedence ────────────────────────────────────────────

func TestEndpointForPrefersSpecific(t *testing.T) {
	t.Setenv("VISION_ENDPOINT", "https://vision.example/api")
	t.Setenv("LM_STUDIO_BASE", "https://fallback.example")
	if got := VisionEndpoint(); got != "https://vision.example/api" {
		t.Errorf("VisionEndpoint = %q, want vision.example", got)
	}
	if got := EmbedEndpoint(); got != "https://fallback.example" {
		t.Errorf("EmbedEndpoint should fall back to LM_STUDIO_BASE, got %q", got)
	}
}

func TestEndpointForFallsBackToLMStudioBase(t *testing.T) {
	t.Setenv("VISION_ENDPOINT", "")
	t.Setenv("TEXT_ENDPOINT", "")
	t.Setenv("EMBED_ENDPOINT", "")
	t.Setenv("LM_STUDIO_BASE", "https://shared.example")
	if got := TextEndpoint(); got != "https://shared.example" {
		t.Errorf("TextEndpoint = %q, want shared.example", got)
	}
}

func TestEndpointForFallsBackToLocalhost(t *testing.T) {
	t.Setenv("VISION_ENDPOINT", "")
	t.Setenv("TEXT_ENDPOINT", "")
	t.Setenv("EMBED_ENDPOINT", "")
	t.Setenv("LM_STUDIO_BASE", "")
	if got := TextEndpoint(); got != "http://localhost:1234" {
		t.Errorf("TextEndpoint = %q, want localhost", got)
	}
}

// ── api key env precedence ────────────────────────────────────────────

func TestLLMAPIKeyReadsEnv(t *testing.T) {
	t.Setenv("LLM_API_KEY", "sk-or-v1-cloudtoken")
	if got := LLMAPIKey(); got != "sk-or-v1-cloudtoken" {
		t.Errorf("LLMAPIKey = %q, want sk-or-v1-cloudtoken", got)
	}
}

func TestLLMAPIKeyDefaultsToLMStudioLiteral(t *testing.T) {
	// Empty env should fall back to the lm-studio literal so local
	// LM Studio (which ignores the token) keeps working without setup.
	t.Setenv("LLM_API_KEY", "")
	if got := LLMAPIKey(); got != "lm-studio" {
		t.Errorf("LLMAPIKey = %q, want lm-studio fallback", got)
	}
}

// ── retry behavior ─────────────────────────────────────────────────────

// fastRetry overrides the slow defaults so tests run in ms.
var fastRetry = retryConfig{
	maxAttempts: 5,
	baseDelay:   1 * time.Millisecond,
	maxDelay:    20 * time.Millisecond,
	timeout:     5 * time.Second,
}

func TestPostJSONWithRetrySuccessFirstTry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	body, err := postJSONWithRetryConfig(t.Context(), srv.URL, []byte(`{"x":1}`), nil, fastRetry)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q", string(body))
	}
}

func TestPostJSONWithRetryRetriesOn429ThenSucceeds(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":"rate limited"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	body, err := postJSONWithRetryConfig(t.Context(), srv.URL, []byte(`{}`), nil, fastRetry)
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q", string(body))
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("expected exactly 3 hits, got %d", got)
	}
}

func TestPostJSONWithRetryHonorsRetryAfterHeader(t *testing.T) {
	var times []time.Time
	var mu strings.Builder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		times = append(times, time.Now())
		_ = mu // keep imports tidy
		if len(times) < 2 {
			w.Header().Set("Retry-After", "1") // 1 second
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	cfg := fastRetry
	cfg.baseDelay = 1 * time.Millisecond
	cfg.maxDelay = 5 * time.Second
	start := time.Now()
	if _, err := postJSONWithRetryConfig(t.Context(), srv.URL, []byte(`{}`), nil, cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	elapsed := time.Since(start)
	// Server asked for 1s; we should have waited ~ that long, not the
	// ~2ms exponential backoff (but allow plenty of slack for CI noise).
	if elapsed < 800*time.Millisecond {
		t.Errorf("expected to honor Retry-After=1, only waited %v", elapsed)
	}
}

func TestPostJSONWithRetryRetriesOn5xx(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	if _, err := postJSONWithRetryConfig(t.Context(), srv.URL, []byte(`{}`), nil, fastRetry); err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
}

func TestPostJSONWithRetryDoesNotRetryOn400(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"bad model name"}`)
	}))
	defer srv.Close()

	_, err := postJSONWithRetryConfig(t.Context(), srv.URL, []byte(`{}`), nil, fastRetry)
	if err == nil {
		t.Fatal("expected error on 400, got nil")
	}
	if !errors.Is(err, ErrNonRetryable) {
		t.Errorf("expected ErrNonRetryable wrapping, got %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("expected exactly 1 hit (no retries on 4xx), got %d", got)
	}
}

func TestPostJSONWithRetryDoesNotRetryOn401(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := postJSONWithRetryConfig(t.Context(), srv.URL, []byte(`{}`), nil, fastRetry)
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("expected exactly 1 hit (auth failures don't retry), got %d", got)
	}
}

func TestPostJSONWithRetryGivesUpAfterMaxAttempts(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway) // permanent 5xx
	}))
	defer srv.Close()

	_, err := postJSONWithRetryConfig(t.Context(), srv.URL, []byte(`{}`), nil, fastRetry)
	if err == nil {
		t.Fatal("expected error after exhausted retries, got nil")
	}
	if got := hits.Load(); got != int32(fastRetry.maxAttempts) {
		t.Errorf("expected %d hits, got %d", fastRetry.maxAttempts, got)
	}
}

func TestPostJSONWithRetryRespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// always fail so retry keeps looping
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	// cancel after a short window — the retry loop should bail before
	// burning all attempts.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	cfg := retryConfig{
		maxAttempts: 100,
		baseDelay:   30 * time.Millisecond,
		maxDelay:    1 * time.Second,
		timeout:     5 * time.Second,
	}
	_, err := postJSONWithRetryConfig(ctx, srv.URL, []byte(`{}`), nil, cfg)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

// ── backoff math ───────────────────────────────────────────────────────

func TestBackoffIncreasesAndCaps(t *testing.T) {
	base := 100 * time.Millisecond
	maxD := 500 * time.Millisecond
	prev := time.Duration(0)
	for attempt := 1; attempt <= 5; attempt++ {
		got := backoff(attempt, base, maxD)
		// jitter is up to 25%, so just check the floor (no jitter) and ceiling
		floor := min(base*(1<<attempt), maxD)
		ceiling := floor + floor/4
		if got < floor || got > ceiling {
			t.Errorf("attempt %d: got %v, want between %v and %v", attempt, got, floor, ceiling)
		}
		// non-decreasing while we haven't hit maxD
		if attempt > 1 && prev > 0 && got < prev/2 && prev < maxD {
			t.Errorf("attempt %d: got %v unexpectedly less than previous %v", attempt, got, prev)
		}
		prev = got
	}
}
