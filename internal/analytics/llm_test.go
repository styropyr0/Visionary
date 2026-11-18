package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type seenRequest struct {
	method, path, query string
	header              http.Header
	body                map[string]any
}

// fakeProvider answers every request with reply and records what it got.
func fakeProvider(t *testing.T, status int, reply string) (*httptest.Server, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		seen = append(seen, seenRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body})
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func testClient(t *testing.T, provider, base string) llmClient {
	t.Helper()
	get := func(k string) string {
		switch k {
		case "ai_provider":
			return provider
		case "model", "reasoning", "openai_api_base":
			return ""
		case "azure_openai_endpoint", "ollama_host", "lmstudio_host", "vllm_host":
			return base
		case "azure_openai_api_version":
			return "2024-10-21"
		}
		return "secret-key"
	}
	cfg, ok := resolveLLM(get)
	if provider == "azure" {
		cfg.Model = "my-deploy"
		ok = true
	}
	if !ok {
		t.Fatalf("%s: not resolved", provider)
	}
	if cfg.Shape != shapeOllama && aiProviderByID[provider].HostKey == "" && !cfg.Azure {
		cfg.Base = base
	}
	return newLLMClient(cfg, nil)
}

var testMessages = []chatMessage{
	{Role: "system", Content: "Answer in JSON."},
	{Role: "user", Content: "first"},
	{Role: "assistant", Content: `{"a":1}`},
	{Role: "user", Content: "second"},
}

func TestOpenAIShape(t *testing.T) {
	srv, seen := fakeProvider(t, 200, `{"choices":[{"message":{"content":"`+"```json\\n{\\\"x\\\":1}\\n```"+`"}}]}`)
	c := testClient(t, "openai", srv.URL)
	got, err := c.chatJSON(context.Background(), testMessages)
	if err != nil || got != `{"x":1}` {
		t.Fatalf("%q %v", got, err)
	}
	r := (*seen)[0]
	if r.path != "/chat/completions" || r.header.Get("Authorization") != "Bearer secret-key" || r.body["model"] != "gpt-4.1" {
		t.Fatalf("%+v", r)
	}
	if rf, _ := r.body["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Fatalf("response_format %v", r.body["response_format"])
	}
}

func TestAzureShape(t *testing.T) {
	srv, seen := fakeProvider(t, 200, `{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`)
	c := testClient(t, "azure", srv.URL)
	if _, err := c.chatJSON(context.Background(), testMessages); err != nil {
		t.Fatal(err)
	}
	r := (*seen)[0]
	if r.path != "/openai/deployments/my-deploy/chat/completions" || r.query != "api-version=2024-10-21" || r.header.Get("api-key") != "secret-key" || r.header.Get("Authorization") != "" {
		t.Fatalf("%+v", r)
	}
}

func TestLMStudioAsksForSchema(t *testing.T) {
	srv, seen := fakeProvider(t, 200, `{"choices":[{"message":{"content":"{}"}}]}`)
	c := testClient(t, "lmstudio", srv.URL)
	c.(*openAIClient).model = "qwen"
	if _, err := c.chatJSON(context.Background(), testMessages); err != nil {
		t.Fatal(err)
	}
	r := (*seen)[0]
	if rf, _ := r.body["response_format"].(map[string]any); r.path != "/v1/chat/completions" || rf["type"] != "json_schema" || r.header.Get("Authorization") != "" {
		t.Fatalf("%+v", r)
	}
}

func TestAnthropicShape(t *testing.T) {
	srv, seen := fakeProvider(t, 200, `{"content":[{"type":"tool_use","name":"answer","input":{"x":1}}],"stop_reason":"tool_use"}`)
	c := testClient(t, "anthropic", srv.URL)
	got, err := c.chatJSON(context.Background(), testMessages)
	if err != nil || got != `{"x":1}` {
		t.Fatalf("%q %v", got, err)
	}
	r := (*seen)[0]
	if r.path != "/messages" || r.header.Get("x-api-key") != "secret-key" || r.header.Get("anthropic-version") == "" || r.body["model"] != "claude-opus-5-5" {
		t.Fatalf("%+v", r)
	}
	if r.body["system"] != "Answer in JSON." {
		t.Fatalf("system %v", r.body["system"])
	}
	if msgs, _ := r.body["messages"].([]any); len(msgs) != 3 {
		t.Fatalf("messages %v", r.body["messages"])
	}
	if tc, _ := r.body["tool_choice"].(map[string]any); tc["name"] != "answer" {
		t.Fatalf("tool_choice %v", r.body["tool_choice"])
	}
}

func TestAnthropicTextFallbackAndCutOff(t *testing.T) {
	srv, _ := fakeProvider(t, 200, `{"content":[{"type":"text","text":"Here: {\"y\":2}"}],"stop_reason":"end_turn"}`)
	if got, err := testClient(t, "anthropic", srv.URL).chatJSON(context.Background(), testMessages); err != nil || got != `{"y":2}` {
		t.Fatalf("%q %v", got, err)
	}
	srv2, _ := fakeProvider(t, 200, `{"content":[],"stop_reason":"max_tokens"}`)
	if _, err := testClient(t, "anthropic", srv2.URL).chatJSON(context.Background(), testMessages); err == nil || !strings.Contains(err.Error(), "cut off") {
		t.Fatal(err)
	}
}

func TestGeminiShape(t *testing.T) {
	srv, seen := fakeProvider(t, 200, `{"candidates":[{"content":{"role":"model","parts":[{"text":"{\"x\":"},{"text":"1}"}]},"finishReason":"STOP"}]}`)
	c := testClient(t, "gemini", srv.URL)
	got, err := c.chatJSON(context.Background(), testMessages)
	if err != nil || got != `{"x":1}` {
		t.Fatalf("%q %v", got, err)
	}
	r := (*seen)[0]
	if r.path != "/models/gemini-2.5-flash:generateContent" || r.header.Get("x-goog-api-key") != "secret-key" || strings.Contains(r.query, "key=") {
		t.Fatalf("%+v", r)
	}
	gc, _ := r.body["generationConfig"].(map[string]any)
	contents, _ := r.body["contents"].([]any)
	if gc["responseMimeType"] != "application/json" || len(contents) != 3 || r.body["systemInstruction"] == nil {
		t.Fatalf("%v", r.body)
	}
	if second, _ := contents[1].(map[string]any); second["role"] != "model" {
		t.Fatalf("roles %v", contents)
	}
}

func TestGeminiBlocked(t *testing.T) {
	srv, _ := fakeProvider(t, 200, `{"promptFeedback":{"blockReason":"SAFETY"}}`)
	if _, err := testClient(t, "gemini", srv.URL).chatJSON(context.Background(), testMessages); err == nil || !strings.Contains(err.Error(), "Google Gemini: the question was blocked (SAFETY)") {
		t.Fatal(err)
	}
}

func TestOllamaShapeAndFirstModel(t *testing.T) {
	var paths []string
	var chat map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/api/tags" {
			_, _ = io.WriteString(w, `{"models":[{"name":"qwen3:8b"},{"name":"llama3.2:latest"}]}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&chat)
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"{\"ok\":true}"},"done":true}`)
	}))
	defer srv.Close()
	c := testClient(t, "ollama", strings.TrimPrefix(srv.URL, "http://"))
	got, err := c.chatJSON(context.Background(), testMessages)
	if err != nil || got != `{"ok":true}` {
		t.Fatalf("%q %v", got, err)
	}
	if strings.Join(paths, ",") != "/api/tags,/api/chat" || chat["format"] != "json" || chat["stream"] != false || chat["model"] != "llama3.2:latest" {
		t.Fatalf("%v %v", paths, chat)
	}
	if c.modelName() != "llama3.2:latest" {
		t.Fatal(c.modelName())
	}
}

func TestProviderErrors(t *testing.T) {
	cases := []struct {
		provider string
		status   int
		body     string
		want     string
	}{
		{"openai", 401, `{"error":{"message":"Incorrect API key provided: secret-key"}}`, "OpenAI: the API key was refused (status 401): Incorrect API key provided: [key]"},
		{"anthropic", 404, `{"type":"error","error":{"type":"not_found_error","message":"model: nope"}}`, "Anthropic (Claude): not found (status 404): model: nope"},
		{"gemini", 429, `{"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, "Google Gemini: rate limited (status 429): Quota exceeded"},
		{"groq", 500, `<html>oops</html>`, "Groq: status 500"},
	}
	for _, c := range cases {
		srv, _ := fakeProvider(t, c.status, c.body)
		_, err := testClient(t, c.provider, srv.URL).chatJSON(context.Background(), testMessages)
		var pe *providerError
		if err == nil || err.Error() != c.want || !errors.As(err, &pe) || pe.Status != c.status {
			t.Errorf("%s: %v", c.provider, err)
		}
	}
	// Nothing listening: the error names the provider and the host.
	srv, _ := fakeProvider(t, 200, "")
	srv.Close()
	if _, err := testClient(t, "ollama", srv.URL).chatJSON(context.Background(), testMessages); err == nil || !strings.HasPrefix(err.Error(), "Ollama (local): cannot reach") {
		t.Fatal(err)
	}
}

func TestListModels(t *testing.T) {
	srv, _ := fakeProvider(t, 200, `{"data":[{"id":"gpt-4.1"},{"id":"text-embedding-3-small"},{"id":"gpt-4.1-mini"},{"id":"whisper-1"}]}`)
	got, err := testClient(t, "openai", srv.URL).listModels(context.Background())
	if err != nil || strings.Join(got, ",") != "gpt-4.1,gpt-4.1-mini" {
		t.Fatalf("%v %v", got, err)
	}
	srv2, _ := fakeProvider(t, 200, `[{"id":"b/model"},{"id":"a/model"}]`)
	got, err = testClient(t, "together", srv2.URL).listModels(context.Background())
	if err != nil || strings.Join(got, ",") != "a/model,b/model" {
		t.Fatalf("%v %v", got, err)
	}
	srv3, _ := fakeProvider(t, 200, `{"models":[{"name":"models/gemini-x","supportedGenerationMethods":["generateContent"]},{"name":"models/text-embedding-004","supportedGenerationMethods":["embedContent"]}]}`)
	got, err = testClient(t, "gemini", srv3.URL).listModels(context.Background())
	if err != nil || strings.Join(got, ",") != "gemini-x" {
		t.Fatalf("%v %v", got, err)
	}
	srv4, _ := fakeProvider(t, 200, `{"data":[{"id":"claude-opus-5-5"},{"id":"claude-haiku-5-5"}]}`)
	got, err = testClient(t, "anthropic", srv4.URL).listModels(context.Background())
	if err != nil || strings.Join(got, ",") != "claude-opus-5-5,claude-haiku-5-5" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestResolveLLM(t *testing.T) {
	conf := func(m map[string]string) func(string) string {
		return func(k string) string {
			if v, ok := m[k]; ok {
				return v
			}
			return confByKey[k].Default
		}
	}
	if _, ok := resolveLLM(conf(nil)); ok {
		t.Fatal("no key resolves")
	}
	c, ok := resolveLLM(conf(map[string]string{"openai_api_key": "k"}))
	if !ok || c.Base != "https://api.openai.com/v1" || c.Model != "gpt-4.1" {
		t.Fatalf("%+v", c)
	}
	c, ok = resolveLLM(conf(map[string]string{"ai_provider": "ollama"}))
	if !ok || c.Base != "http://localhost:11434" || c.Model != "" {
		t.Fatalf("%+v", c)
	}
	c, ok = resolveLLM(conf(map[string]string{"ai_provider": "lmstudio", "lmstudio_host": "192.168.1.5:1234/"}))
	if !ok || c.Base != "http://192.168.1.5:1234/v1" {
		t.Fatalf("%+v", c)
	}
	c, ok = resolveLLM(conf(map[string]string{"ai_provider": "azure", "azure_openai_api_key": "k", "azure_openai_endpoint": "myres", "model": "dep"}))
	if !ok || c.Base != "https://myres.openai.azure.com" || !c.Azure {
		t.Fatalf("%+v", c)
	}
	if _, ok = resolveLLM(conf(map[string]string{"ai_provider": "azure", "azure_openai_api_key": "k", "azure_openai_endpoint": "myres"})); ok {
		t.Fatal("azure without a deployment resolves")
	}
	if got := azureEndpoint("https://x.openai.azure.com/openai/deployments/d/chat/completions?api-version=1"); got != "https://x.openai.azure.com" {
		t.Fatal(got)
	}
	c, ok = resolveLLM(conf(map[string]string{"ai_provider": "custom", "openai_api_base": "https://llm.example.com/v1/"}))
	if !ok || c.Base != "https://llm.example.com/v1" || c.Key != "" {
		t.Fatalf("%+v", c)
	}
	c, _ = resolveLLM(conf(map[string]string{"ai_provider": "anthropic", "anthropic_api_key": "k", "reasoning": "high"}))
	if c.Reasoning != "" {
		t.Fatal("reasoning sent to a provider that ignores it")
	}
}

func TestProviderMigration(t *testing.T) {
	if m := providerMigration(map[string]string{}); m != nil {
		t.Fatalf("fresh install %v", m)
	}
	if m := providerMigration(map[string]string{"openai_api_key": "k", "ai_provider": "gemini"}); m != nil {
		t.Fatalf("already chosen %v", m)
	}
	if m := providerMigration(map[string]string{"openai_api_key": "k"}); m["ai_provider"] != "openai" || len(m) != 1 {
		t.Fatalf("openai %v", m)
	}
	if m := providerMigration(map[string]string{"openai_api_key": "k", "openai_api_base": "https://api.openai.com/v1"}); m["ai_provider"] != "openai" {
		t.Fatalf("openai base %v", m)
	}
	m := providerMigration(map[string]string{"openai_api_key": "k", "openai_api_base": "https://openrouter.ai/api/v1"})
	if m["ai_provider"] != "custom" || m["custom_api_key"] != "k" {
		t.Fatalf("custom %v", m)
	}
	// A migrated custom install resolves to the same base and key.
	cfg := map[string]string{"openai_api_key": "k", "openai_api_base": "https://openrouter.ai/api/v1", "model": "x"}
	for k, v := range m {
		cfg[k] = v
	}
	c, ok := resolveLLM(func(k string) string {
		if v, ok := cfg[k]; ok {
			return v
		}
		return confByKey[k].Default
	})
	if !ok || c.Base != "https://openrouter.ai/api/v1" || c.Key != "k" || c.Model != "x" {
		t.Fatalf("%+v", c)
	}
}

func TestAIProviderSettingsExist(t *testing.T) {
	for _, p := range aiProviders {
		for _, k := range append([]string{p.KeyKey, p.HostKey}, p.Fields...) {
			if k != "" && confByKey[k] == nil {
				t.Errorf("%s: no setting %s", p.ID, k)
			}
		}
		if p.KeyKey != "" && confByKey[p.KeyKey].Kind != confSecret {
			t.Errorf("%s: key is not secret", p.ID)
		}
	}
}

func TestAITestEndpoint(t *testing.T) {
	srv, seen := fakeProvider(t, 200, `{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`)
	s := testServer()
	body := `{"values":{"ai_provider":"custom","openai_api_base":"` + srv.URL + `","custom_api_key":"typed-key","model":"m1"}}`
	r := httptest.NewRequest("POST", "/api/settings/ai/test", strings.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Role: "admin"}))
	w := httptest.NewRecorder()
	s.handleAITest(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) || !strings.Contains(w.Body.String(), `"model":"m1"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if (*seen)[0].header.Get("Authorization") != "Bearer typed-key" {
		t.Fatal("draft key not used")
	}
	if strings.Contains(w.Body.String(), "typed-key") {
		t.Fatal("key echoed")
	}

	r = httptest.NewRequest("POST", "/api/settings/ai/test", strings.NewReader(`{"values":{"ai_provider":"anthropic"}}`))
	r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Role: "admin"}))
	w = httptest.NewRecorder()
	s.handleAITest(w, r)
	if !strings.Contains(w.Body.String(), "Add the Anthropic (Claude) API key") {
		t.Fatal(w.Body.String())
	}

	r = httptest.NewRequest("POST", "/api/settings/ai/models", strings.NewReader(`{"values":{"ai_provider":"anthropic"}}`))
	r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Role: "viewer"}))
	w = httptest.NewRecorder()
	s.handleAIModels(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer %d", w.Code)
	}
}
