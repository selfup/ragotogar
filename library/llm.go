package library

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var thinkBlockRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// LLMComplete sends a single-message chat completion to the configured text
// endpoint (TEXT_ENDPOINT → LM_STUDIO_BASE → localhost) and returns the
// trimmed response with any <think>…</think> reasoning blocks stripped.
//
// Retries up to 5 times with exponential backoff on network errors, 429,
// and 5xx — see postJSONWithRetry for the full policy. Honors Retry-After
// on 429 responses, which is what cloud APIs (OpenAI, Anthropic, Together,
// Groq) all use to signal rate limits.
func LLMComplete(ctx context.Context, model, prompt string) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model:       model,
		MaxTokens:   -1, // let the server pick (LM Studio: full context window)
		Temperature: 0.0,
		Messages: []chatMessage{
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}

	raw, err := postJSONWithRetry(ctx,
		TextEndpoint()+"/v1/chat/completions",
		body,
		map[string]string{"Authorization": "Bearer lm-studio"},
	)
	if err != nil {
		return "", fmt.Errorf("chat request: %w", err)
	}

	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("chat API error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("no choices in chat response")
	}
	content := strings.TrimSpace(thinkBlockRe.ReplaceAllString(out.Choices[0].Message.Content, ""))
	if content == "" {
		return "", fmt.Errorf("LLM returned empty content")
	}
	return content, nil
}
