package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// llmClient is the analyst's model: any provider that answers a chat with a
// JSON object.
type llmClient interface {
	chatJSON(ctx context.Context, messages []chatMessage) (string, error)
	// listModels asks the provider which models it offers.
	listModels(ctx context.Context) ([]string, error)
	modelName() string
	providerLabel() string
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

var errAnalystOff = errors.New("the AI analyst is off: choose a model provider in Settings → Server → AI analyst")

// providerError is a failure the provider reported, named after it.
type providerError struct {
	Provider string
	Status   int
	Msg      string
}

func (e *providerError) Error() string { return e.Provider + ": " + e.Msg }

// llmSlot is the client in force with the config it was built from.
type llmSlot struct {
	cfg llmConfig
	c   llmClient
}

// newLLM builds the analyst's client from Settings → Server, or nil while
// the chosen provider lacks what it needs.
func (s *Server) newLLM() *llmSlot {
	cfg, ok := resolveLLM(s.conf)
	if !ok {
		return nil
	}
	return &llmSlot{cfg: cfg, c: newLLMClient(cfg, nil)}
}

// newLLMClient makes the client for cfg; hc nil uses one sized to the
// reasoning effort.
func newLLMClient(cfg llmConfig, hc *http.Client) llmClient {
	if hc == nil {
		timeout := 60 * time.Second
		if cfg.Reasoning != "" || cfg.Shape == shapeOllama {
			timeout = 3 * time.Minute
		}
		hc = &http.Client{Timeout: timeout}
	}
	b := &llmBase{cfg: cfg, http: hc, model: cfg.Model}
	switch cfg.Shape {
	case shapeAnthropic:
		return &anthropicClient{b}
	case shapeGemini:
		return &geminiClient{b}
	case shapeOllama:
		return &ollamaClient{b}
	}
	return &openAIClient{b}
}

// llm is the analyst's client in force, nil while it is off. Callers keep
// the value they got: a settings change swaps it, never mutates it.
func (s *Server) llm() llmClient {
	if p := s.llmP.Load(); p != nil {
		return p.c
	}
	return nil
}

// setLLM puts c in force; nil turns the analyst off.
func (s *Server) setLLM(c llmClient) {
	if c == nil {
		s.llmP.Store(nil)
		return
	}
	s.llmP.Store(&llmSlot{c: c})
}

// chatJSON asks the client in force; the provider can be removed in
// Settings between a caller's check and its call.
func (s *Server) chatJSON(ctx context.Context, messages []chatMessage) (string, error) {
	c := s.llm()
	if c == nil {
		return "", errAnalystOff
	}
	return c.chatJSON(ctx, messages)
}

// modelName is the model in use, "" while the analyst is off.
func (s *Server) modelName() string {
	if c := s.llm(); c != nil {
		return c.modelName()
	}
	return ""
}

// llmBase is what every provider's client shares.
type llmBase struct {
	cfg  llmConfig
	http *http.Client

	mu    sync.Mutex
	model string
}

func (b *llmBase) providerLabel() string { return b.cfg.Label }

func (b *llmBase) modelName() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.model
}

func (b *llmBase) fail(status int, format string, a ...any) error {
	msg := fmt.Sprintf(format, a...)
	if b.cfg.Key != "" {
		msg = strings.ReplaceAll(msg, b.cfg.Key, "[key]")
	}
	return &providerError{Provider: b.cfg.Label, Status: status, Msg: msg}
}

// useModel is the model to ask: a local server with none chosen uses the
// first one it has.
func (b *llmBase) useModel(ctx context.Context, list func(context.Context) ([]string, error)) (string, error) {
	if m := b.modelName(); m != "" {
		return m, nil
	}
	models, err := list(ctx)
	if err != nil {
		return "", err
	}
	if len(models) == 0 {
		return "", b.fail(0, "it has no models yet; download or load one, then choose it in Settings → Server → AI analyst")
	}
	b.mu.Lock()
	b.model = models[0]
	b.mu.Unlock()
	return models[0], nil
}

// call sends one request and decodes a successful answer into out.
func (b *llmBase) call(ctx context.Context, method, u string, body any, headers map[string]string, out any) error {
	var rd io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return b.fail(0, "bad address %q", redactURL(u))
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return b.fail(0, "cannot reach %s: %v", req.URL.Host, unwrapURLError(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return b.fail(resp.StatusCode, "read response: %v", err)
	}
	if resp.StatusCode >= 300 {
		return b.statusError(resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return b.fail(resp.StatusCode, "status %d: the answer is not JSON; is the address right?", resp.StatusCode)
	}
	return nil
}

func (b *llmBase) statusError(status int, raw []byte) error {
	msg := providerMessage(raw)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		if msg == "" {
			msg = "check the API key"
		}
		return b.fail(status, "the API key was refused (status %d): %s", status, msg)
	case status == http.StatusNotFound:
		if msg == "" {
			msg = "check the model name and the address"
		}
		return b.fail(status, "not found (status 404): %s", msg)
	case status == http.StatusTooManyRequests:
		if msg == "" {
			msg = "try again in a moment, or check the account's credit"
		}
		return b.fail(status, "rate limited (status 429): %s", msg)
	}
	if msg == "" {
		return b.fail(status, "status %d", status)
	}
	return b.fail(status, "status %d: %s", status, msg)
}

// providerMessage digs the error text out of the shapes providers use.
func providerMessage(raw []byte) string {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(raw, &v) != nil {
		s := strings.TrimSpace(string(raw))
		if len(s) > 200 || strings.HasPrefix(s, "<") {
			return ""
		}
		return s
	}
	if len(v.Error) > 0 {
		var e struct {
			Message string `json:"message"`
		}
		var str string
		if json.Unmarshal(v.Error, &e) == nil && e.Message != "" {
			return e.Message
		}
		if json.Unmarshal(v.Error, &str) == nil && str != "" {
			return str
		}
	}
	if v.Message != "" {
		return v.Message
	}
	var str string
	if json.Unmarshal(v.Detail, &str) == nil {
		return str
	}
	return ""
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// jsonObject cuts the outermost object out of text a model wrapped in prose
// or a code fence.
func jsonObject(s string) string {
	s = strings.TrimSpace(s)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}
