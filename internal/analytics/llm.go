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
var errAnalystOff = errors.New("the AI analyst is off: add an OpenAI API key in Settings → Server")

// newLLM builds the analyst's client from Settings → Server, or nil while
// no API key is set.
func (s *Server) newLLM() *llm {
	key := strings.TrimSpace(s.conf("OPENAI_API_KEY"))
	if key == "" {
		return nil
	}
	reasoning := strings.ToLower(s.conf("VISIONARY_REASONING"))
	timeout := 60 * time.Second
	if reasoning != "" {
		timeout = 3 * time.Minute
	}
	return &llm{
		apiKey:    key,
		apiBase:   strings.TrimRight(s.conf("OPENAI_API_BASE"), "/"),
		model:     s.conf("VISIONARY_MODEL"),
		reasoning: reasoning,
		http:      &http.Client{Timeout: timeout},
	}
}

// llm is the analyst's client in force, nil while it is off. Callers keep
// the value they got: a settings change swaps it, never mutates it.
func (s *Server) llm() *llm { return s.llmP.Load() }

// modelName is the model in use, "" while the analyst is off.
func (c *llm) modelName() string {
	if c == nil {
		return ""
	}
	return c.model
}

func (c *llm) chatJSON(ctx context.Context, messages []chatMessage) (string, error) {
	// The key can be removed in Settings while a caller holds no client.
	if c == nil {
		return "", errAnalystOff
	}
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
