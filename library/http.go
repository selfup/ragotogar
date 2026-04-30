package library

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Per-stage endpoint helpers. Each prefers its dedicated env var, falls back
// to LM_STUDIO_BASE (the legacy single-endpoint shape), then to localhost.
// Lets vision / text / embedding traffic land on different providers when
// the workload moves to cloud APIs (OpenAI-compatible everywhere).
//
//	VISION_ENDPOINT  → cmd/describe (heavy multimodal)
//	TEXT_ENDPOINT    → cmd/classify, cmd/search verify (cheap text)
//	EMBED_ENDPOINT   → cmd/index (embeddings)
//	LM_STUDIO_BASE   → fallback when the above aren't set; default for local LM Studio
func VisionEndpoint() string { return endpointFor("VISION_ENDPOINT") }
func TextEndpoint() string   { return endpointFor("TEXT_ENDPOINT") }
func EmbedEndpoint() string  { return endpointFor("EMBED_ENDPOINT") }

func endpointFor(specificVar string) string {
	if v := os.Getenv(specificVar); v != "" {
		return v
	}
	if v := os.Getenv("LM_STUDIO_BASE"); v != "" {
		return v
	}
	return "http://localhost:1234"
}

// retryConfig controls the exponential-backoff behavior of postJSONWithRetry.
// Defaults are tuned for local LM Studio (where transient errors are rare and
// retries should resolve quickly) but stretch comfortably for cloud APIs that
// rate-limit or 5xx under load.
type retryConfig struct {
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
	timeout     time.Duration
}

var defaultRetry = retryConfig{
	maxAttempts: 5,
	baseDelay:   1 * time.Second,
	maxDelay:    30 * time.Second,
	timeout:     600 * time.Second,
}

// ErrNonRetryable signals that the response was a 4xx other than 429 — the
// request shape was wrong (auth, model name, malformed body) and retrying
// won't help. Wrapped into the returned error with %w so callers can detect
// it via errors.Is and abort batch loops instead of grinding through N
// identical failures.
var ErrNonRetryable = errors.New("non-retryable HTTP error")

// postJSONWithRetry POSTs body to url with up to cfg.maxAttempts attempts,
// retrying on network errors, 429, and 5xx. Honors Retry-After (seconds) on
// 429 responses. Returns the response body bytes on success or the last
// error otherwise.
//
// Body is replayable across attempts because we hold the original byte slice
// and construct a fresh request per try (http.Request bodies aren't
// rewindable in general).
func postJSONWithRetry(ctx context.Context, url string, body []byte, headers map[string]string) ([]byte, error) {
	return postJSONWithRetryConfig(ctx, url, body, headers, defaultRetry)
}

func postJSONWithRetryConfig(ctx context.Context, url string, body []byte, headers map[string]string, cfg retryConfig) ([]byte, error) {
	client := &http.Client{Timeout: cfg.timeout}
	var lastErr error
	for attempt := range cfg.maxAttempts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if attempt > 0 {
			wait := backoff(attempt, cfg.baseDelay, cfg.maxDelay)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err // construction errors aren't retryable
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			// network/dial error — retryable
			lastErr = fmt.Errorf("attempt %d: %w", attempt+1, err)
			continue
		}

		raw, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("attempt %d: read body: %w", attempt+1, readErr)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			return raw, nil
		}

		// 429 Too Many Requests — honor Retry-After if the server tells us
		// how long to wait, otherwise fall through to standard backoff.
		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("attempt %d: HTTP 429: %s", attempt+1, truncate(string(raw), 200))
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
					wait := min(time.Duration(secs)*time.Second, cfg.maxDelay)
					select {
					case <-time.After(wait):
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
			}
			continue
		}

		// 5xx is retryable
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("attempt %d: HTTP %d: %s", attempt+1, resp.StatusCode, truncate(string(raw), 200))
			continue
		}

		// 4xx (other than 429): non-retryable
		return nil, fmt.Errorf("HTTP %d: %s: %w",
			resp.StatusCode, truncate(string(raw), 200), ErrNonRetryable)
	}
	return nil, fmt.Errorf("after %d attempts: %w", cfg.maxAttempts, lastErr)
}

// backoff returns the wait time before attempt N (1-indexed for the second
// attempt onward), capped at maxDelay, with random jitter up to 25% of the
// computed delay added to spread retries across concurrent workers.
func backoff(attempt int, base, maxDelay time.Duration) time.Duration {
	d := min(base*(1<<attempt), maxDelay)
	jitter := time.Duration(rand.Int64N(int64(d) / 4))
	return d + jitter
}
