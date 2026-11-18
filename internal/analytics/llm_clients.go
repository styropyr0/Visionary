package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// ---------- OpenAI chat completions: OpenAI, Azure and compatible APIs ----------

type openAIClient struct{ *llmBase }

func (c *openAIClient) headers() map[string]string {
	h := map[string]string{}
	switch {
	case c.cfg.Azure:
		h["api-key"] = c.cfg.Key
	case c.cfg.Key != "":
		h["Authorization"] = "Bearer " + c.cfg.Key
	}
	if c.cfg.Provider == "openrouter" {
		h["X-Title"] = "Visionary"
	}
	return h
}

func (c *openAIClient) chatJSON(ctx context.Context, messages []chatMessage) (string, error) {
	model, err := c.useModel(ctx, c.listModels)
	if err != nil {
		return "", err
	}
	body := map[string]any{"model": model, "messages": messages}
	switch c.cfg.JSONMode {
	case "schema":
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "answer", "schema": map[string]any{"type": "object"}}}
	default:
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	if c.cfg.Reasoning != "" {
		body["reasoning_effort"] = c.cfg.Reasoning
	} else {
		body["temperature"] = 0
	}
	u := c.cfg.Base + "/chat/completions"
	if c.cfg.Azure {
		u = c.cfg.Base + "/openai/deployments/" + url.PathEscape(model) + "/chat/completions?api-version=" + url.QueryEscape(c.cfg.APIVersion)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := c.call(ctx, http.MethodPost, u, body, c.headers(), &out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", c.fail(0, "%s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", c.fail(0, "no answer")
	}
	content := strings.TrimSpace(out.Choices[0].Message.Content)
	if content == "" {
		if out.Choices[0].FinishReason == "content_filter" {
			return "", c.fail(0, "the answer was withheld by its content filter")
		}
		return "", c.fail(0, "empty answer")
	}
	return jsonObject(content), nil
}

// notChat are OpenAI models that can't chat.
var notChat = []string{"embedding", "tts", "whisper", "dall-e", "moderation", "image", "audio", "realtime", "transcribe", "davinci", "babbage", "search", "computer-use", "sora"}

func (c *openAIClient) listModels(ctx context.Context) ([]string, error) {
	if c.cfg.Azure {
		// Deployments are listed only by Azure's management API.
		return nil, nil
	}
	var raw json.RawMessage
	if err := c.call(ctx, http.MethodGet, c.cfg.Base+"/models", nil, c.headers(), &raw); err != nil {
		return nil, err
	}
	type model struct {
		ID string `json:"id"`
	}
	var list []model
	var wrapped struct {
		Data []model `json:"data"`
	}
	if json.Unmarshal(raw, &wrapped) == nil && wrapped.Data != nil {
		list = wrapped.Data
	} else {
		_ = json.Unmarshal(raw, &list) // Together sends a bare array
	}
	var ids []string
next:
	for _, m := range list {
		if m.ID == "" {
			continue
		}
		if c.cfg.Provider == "openai" {
			for _, w := range notChat {
				if strings.Contains(m.ID, w) {
					continue next
				}
			}
		}
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

// ---------- Anthropic Messages API ----------

type anthropicClient struct{ *llmBase }

func (c *anthropicClient) headers() map[string]string {
	return map[string]string{"x-api-key": c.cfg.Key, "anthropic-version": "2023-06-01"}
}

// The answer comes back as the input of a forced tool call, which holds a
// JSON object by construction.
func (c *anthropicClient) chatJSON(ctx context.Context, messages []chatMessage) (string, error) {
	var system []string
	var turns []chatMessage
	for _, m := range messages {
		if m.Role == "system" {
			system = append(system, m.Content)
			continue
		}
		turns = appendTurn(turns, m.Role, m.Content)
	}
	if len(turns) == 0 || turns[0].Role != "user" {
		turns = append([]chatMessage{{Role: "user", Content: "Answer as instructed."}}, turns...)
	}
	body := map[string]any{
		"model":      c.modelName(),
		"max_tokens": 16384,
		"messages":   turns,
		"tools": []any{map[string]any{
			"name":         "answer",
			"description":  "Give your answer: its input is the JSON object the instructions ask for.",
			"input_schema": map[string]any{"type": "object", "additionalProperties": true},
		}},
		"tool_choice": map[string]string{"type": "tool", "name": "answer"},
	}
	if len(system) > 0 {
		body["system"] = strings.Join(system, "\n\n")
	}
	var out struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := c.call(ctx, http.MethodPost, c.cfg.Base+"/messages", body, c.headers(), &out); err != nil {
		return "", err
	}
	if out.StopReason == "max_tokens" {
		return "", c.fail(0, "the answer was cut off at its length limit")
	}
	if out.StopReason == "refusal" {
		return "", c.fail(0, "the model declined to answer")
	}
	var text strings.Builder
	for _, b := range out.Content {
		switch b.Type {
		case "tool_use":
			if len(b.Input) > 0 && string(b.Input) != "null" {
				return string(b.Input), nil
			}
		case "text":
			text.WriteString(b.Text)
		}
	}
	if t := strings.TrimSpace(text.String()); t != "" {
		return jsonObject(t), nil
	}
	return "", c.fail(0, "empty answer")
}

func (c *anthropicClient) listModels(ctx context.Context) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.call(ctx, http.MethodGet, c.cfg.Base+"/models?limit=1000", nil, c.headers(), &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// appendTurn adds a message, joining it to the last one of the same role:
// Anthropic and Gemini want turns to alternate.
func appendTurn(turns []chatMessage, role, content string) []chatMessage {
	if role != "assistant" {
		role = "user"
	}
	if n := len(turns); n > 0 && turns[n-1].Role == role {
		turns[n-1].Content += "\n\n" + content
		return turns
	}
	return append(turns, chatMessage{Role: role, Content: content})
}

// ---------- Google Gemini API ----------

type geminiClient struct{ *llmBase }

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

func (c *geminiClient) headers() map[string]string {
	return map[string]string{"x-goog-api-key": c.cfg.Key}
}

func (c *geminiClient) chatJSON(ctx context.Context, messages []chatMessage) (string, error) {
	var system []string
	var turns []chatMessage
	for _, m := range messages {
		if m.Role == "system" {
			system = append(system, m.Content)
			continue
		}
		turns = appendTurn(turns, m.Role, m.Content)
	}
	contents := make([]geminiContent, 0, len(turns))
	for _, t := range turns {
		role := "user"
		if t.Role == "assistant" {
			role = "model"
		}
		contents = append(contents, geminiContent{Role: role, Parts: []geminiPart{{Text: t.Content}}})
	}
	body := map[string]any{
		"contents":         contents,
		"generationConfig": map[string]any{"responseMimeType": "application/json", "temperature": 0},
	}
	if len(system) > 0 {
		body["systemInstruction"] = geminiContent{Parts: []geminiPart{{Text: strings.Join(system, "\n\n")}}}
	}
	model := strings.TrimPrefix(c.modelName(), "models/")
	var out struct {
		Candidates []struct {
			Content      geminiContent `json:"content"`
			FinishReason string        `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	u := c.cfg.Base + "/models/" + url.PathEscape(model) + ":generateContent"
	if err := c.call(ctx, http.MethodPost, u, body, c.headers(), &out); err != nil {
		return "", err
	}
	if r := out.PromptFeedback.BlockReason; r != "" {
		return "", c.fail(0, "the question was blocked (%s)", r)
	}
	if len(out.Candidates) == 0 {
		return "", c.fail(0, "no answer")
	}
	var text strings.Builder
	for _, p := range out.Candidates[0].Content.Parts {
		text.WriteString(p.Text)
	}
	t := strings.TrimSpace(text.String())
	if t == "" {
		if r := out.Candidates[0].FinishReason; r != "" && r != "STOP" {
			return "", c.fail(0, "no answer (%s)", r)
		}
		return "", c.fail(0, "empty answer")
	}
	if out.Candidates[0].FinishReason == "MAX_TOKENS" {
		return "", c.fail(0, "the answer was cut off at its length limit")
	}
	return jsonObject(t), nil
}

func (c *geminiClient) listModels(ctx context.Context) ([]string, error) {
	var out struct {
		Models []struct {
			Name    string   `json:"name"`
			Methods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := c.call(ctx, http.MethodGet, c.cfg.Base+"/models?pageSize=1000", nil, c.headers(), &out); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range out.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		chat := false
		for _, g := range m.Methods {
			chat = chat || g == "generateContent"
		}
		if chat && strings.HasPrefix(id, "gemini") && !strings.Contains(id, "embedding") {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// ---------- Ollama ----------

type ollamaClient struct{ *llmBase }

func (c *ollamaClient) chatJSON(ctx context.Context, messages []chatMessage) (string, error) {
	model, err := c.useModel(ctx, c.listModels)
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   false,
		"format":   "json",
		"options":  map[string]any{"temperature": 0},
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Error string `json:"error"`
	}
	if err := c.call(ctx, http.MethodPost, c.cfg.Base+"/api/chat", body, nil, &out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", c.fail(0, "%s", out.Error)
	}
	t := strings.TrimSpace(out.Message.Content)
	if t == "" {
		return "", c.fail(0, "empty answer")
	}
	return jsonObject(t), nil
}

func (c *ollamaClient) listModels(ctx context.Context) ([]string, error) {
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := c.call(ctx, http.MethodGet, c.cfg.Base+"/api/tags", nil, nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		ids = append(ids, m.Name)
	}
	sort.Strings(ids)
	return ids, nil
}
