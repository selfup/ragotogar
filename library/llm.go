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
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	MaxTokens      int             `json:"max_tokens"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

// responseFormat mirrors OpenAI's structured-outputs response_format field.
// Type "json_schema" enables grammar-constrained sampling: the runtime
// enforces that every emitted token keeps the output a valid instance of the
// supplied schema. The model can't drift to YAML / markdown / prose, can't
// invent fields, and can't emit enum values outside the allowed set —
// regardless of instruction-following quality. Supported by LM Studio,
// vLLM, OpenAI, and most OpenAI-compatible servers.
//
// LM Studio specifically rejects type "json_object" (the looser JSON-only
// constraint OpenAI also supports); only "json_schema" or "text" are valid.
type responseFormat struct {
	Type       string         `json:"type"`
	JSONSchema *jsonSchemaDef `json:"json_schema,omitempty"`
}

// jsonSchemaDef is the OpenAI strict-mode wrapper around an actual JSON
// schema. Strict mode requires every property appear in required, every
// object set additionalProperties=false, and no $ref / allOf / anyOf.
type jsonSchemaDef struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
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
	return llmComplete(ctx, model, prompt, nil)
}

// LLMCompleteSchema is LLMComplete with a JSON-schema constraint on the
// output. The runtime grammar-constrains every emitted token to keep the
// output a valid instance of the supplied schema in OpenAI strict mode —
// required keys present, scalar values within their enums, array members
// within their enums, no extra fields, no prose drift. Works reliably
// across model sizes (gemma-2B class through 30B+) because the constraint
// is enforced at the sampler, not via prompt instruction.
//
// schemaName must match the regex [a-zA-Z0-9_-]+ (OpenAI / LM Studio
// requirement). schema is the JSON schema as a map ready to marshal.
func LLMCompleteSchema(ctx context.Context, model, prompt, schemaName string, schema map[string]any) (string, error) {
	return llmComplete(ctx, model, prompt, &responseFormat{
		Type: "json_schema",
		JSONSchema: &jsonSchemaDef{
			Name:   schemaName,
			Strict: true,
			Schema: schema,
		},
	})
}

func llmComplete(ctx context.Context, model, prompt string, format *responseFormat) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model:       model,
		MaxTokens:   -1, // let the server pick (LM Studio: full context window)
		Temperature: 0.0,
		Messages: []chatMessage{
			{Role: "user", Content: prompt},
		},
		ResponseFormat: format,
	})
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}

	raw, err := postJSONWithRetry(ctx,
		TextEndpoint()+"/v1/chat/completions",
		body,
		map[string]string{"Authorization": "Bearer " + LLMAPIKey()},
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
