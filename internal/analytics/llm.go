package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// llm is a minimal OpenAI chat-completions client that asks for a JSON object.
type llm struct {
	apiKey    string
	apiBase   string
	model     string
	reasoning string
	http      *http.Client
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// newLLM returns nil when no API key is set; prompt exploration is then off.
func newLLM(cfg Config) *llm {
	key := strings.TrimSpace(cfg.OpenAIAPIKey)
	if key == "" {
		return nil
	}
	timeout := 60 * time.Second
	if cfg.ModelReasoning != "" {
		timeout = 3 * time.Minute
	}
	return &llm{
		apiKey:    key,
		apiBase:   strings.TrimRight(cfg.OpenAIAPIBase, "/"),
		model:     cfg.Model,
		reasoning: cfg.ModelReasoning,
		http:      &http.Client{Timeout: timeout},
	}
}

func (c *llm) chatJSON(ctx context.Context, messages []chatMessage) (string, error) {
	body := map[string]any{
		"model":           c.model,
		"messages":        messages,
		"response_format": map[string]string{"type": "json_object"},
	}
	if c.reasoning != "" {
		body["reasoning_effort"] = c.reasoning
	} else {
		body["temperature"] = 0
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("openai: read response: %w", err)
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("openai: status %d: decode response", resp.StatusCode)
	}
	if out.Error != nil {
		return "", fmt.Errorf("openai: %s", out.Error.Message)
	}
	if resp.StatusCode >= 300 || len(out.Choices) == 0 {
		return "", fmt.Errorf("openai: status %d", resp.StatusCode)
	}
	content := strings.TrimSpace(out.Choices[0].Message.Content)
	if content == "" {
		return "", errors.New("openai: empty answer")
	}
	return content, nil
}
