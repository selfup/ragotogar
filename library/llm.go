package library

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
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

// LLMComplete sends a single-message chat completion to LM Studio and
// returns the trimmed response with any <think>…</think> reasoning blocks
// stripped. Used by cmd/search's verify pass.
func LLMComplete(ctx context.Context, model, prompt string) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model:       model,
		MaxTokens:   -1, // let LM Studio use the full context window
		Temperature: 0.0,
		Messages: []chatMessage{
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		LMStudioBase()+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer lm-studio")

	client := &http.Client{Timeout: 600 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("chat request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read chat response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("chat HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("LM Studio chat: %s", out.Error.Message)
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
