package analytics

import (
	"net/url"
	"strings"
)

// The API shapes a provider can speak.
const (
	shapeOpenAI    = "openai" // chat completions, also Azure and the compatible APIs
	shapeAnthropic = "anthropic"
	shapeGemini    = "gemini"
	shapeOllama    = "ollama"
)

// aiProvider is one choice under Settings → Server → AI analyst. Its base URL
// is known, so only what it truly needs is asked for.
type aiProvider struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Shape string `json:"shape"`
	Base  string `json:"-"`
	// KeyKey is the setting holding its API key; "" when it takes none.
	KeyKey      string `json:"key_key,omitempty"`
	KeyOptional bool   `json:"key_optional,omitempty"`
	// HostKey is a setting for where it runs, with the localhost default.
	HostKey string `json:"host_key,omitempty"`
	// Fields are the other settings it needs, shown only for it.
	Fields       []string `json:"fields,omitempty"`
	DefaultModel string   `json:"default_model"`
	// Models are suggestions until its own list is fetched.
	Models     []string `json:"models,omitempty"`
	ModelLabel string   `json:"model_label,omitempty"`
	KeyURL     string   `json:"key_url,omitempty"`
	Note       string   `json:"note,omitempty"`
	// JSONMode is how chat completions are asked for a JSON object.
	JSONMode string `json:"-"`
	// Reasoning says whether the reasoning effort setting is sent.
	Reasoning bool `json:"reasoning,omitempty"`
}

var aiProviders = []aiProvider{
	{ID: "openai", Label: "OpenAI", Shape: shapeOpenAI, Base: "https://api.openai.com/v1", KeyKey: "openai_api_key", DefaultModel: "gpt-4.1", KeyURL: "https://platform.openai.com/api-keys", Reasoning: true},
	{ID: "anthropic", Label: "Anthropic (Claude)", Shape: shapeAnthropic, Base: "https://api.anthropic.com/v1", KeyKey: "anthropic_api_key", DefaultModel: "claude-opus-5-5", Models: []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}, KeyURL: "https://console.anthropic.com/settings/keys"},
	{ID: "gemini", Label: "Google Gemini", Shape: shapeGemini, Base: "https://generativelanguage.googleapis.com/v1beta", KeyKey: "gemini_api_key", DefaultModel: "gemini-2.5-flash", KeyURL: "https://aistudio.google.com/apikey"},
	{ID: "azure", Label: "Azure OpenAI", Shape: shapeOpenAI, KeyKey: "azure_openai_api_key", Fields: []string{"azure_openai_endpoint", "azure_openai_api_version"}, ModelLabel: "Deployment", Note: "The model is your deployment's name in Azure AI Foundry.", Reasoning: true},
	{ID: "ollama", Label: "Ollama (local)", Shape: shapeOllama, HostKey: "ollama_host", Note: "Runs on your own machine: pull a model with ollama pull, then pick it here."},
	{ID: "openrouter", Label: "OpenRouter", Shape: shapeOpenAI, Base: "https://openrouter.ai/api/v1", KeyKey: "openrouter_api_key", DefaultModel: "openrouter/auto", KeyURL: "https://openrouter.ai/keys", Reasoning: true},
	{ID: "groq", Label: "Groq", Shape: shapeOpenAI, Base: "https://api.groq.com/openai/v1", KeyKey: "groq_api_key", DefaultModel: "llama-3.3-70b-versatile", KeyURL: "https://console.groq.com/keys"},
	{ID: "mistral", Label: "Mistral", Shape: shapeOpenAI, Base: "https://api.mistral.ai/v1", KeyKey: "mistral_api_key", DefaultModel: "mistral-large-latest", KeyURL: "https://console.mistral.ai/api-keys"},
	{ID: "deepseek", Label: "DeepSeek", Shape: shapeOpenAI, Base: "https://api.deepseek.com/v1", KeyKey: "deepseek_api_key", DefaultModel: "deepseek-chat", KeyURL: "https://platform.deepseek.com/api_keys"},
	{ID: "together", Label: "Together AI", Shape: shapeOpenAI, Base: "https://api.together.xyz/v1", KeyKey: "together_api_key", DefaultModel: "meta-llama/Llama-3.3-70B-Instruct-Turbo", KeyURL: "https://api.together.ai/settings/api-keys"},
	{ID: "lmstudio", Label: "LM Studio (local)", Shape: shapeOpenAI, HostKey: "lmstudio_host", JSONMode: "schema", Note: "Start LM Studio's local server with a model loaded."},
	{ID: "vllm", Label: "vLLM", Shape: shapeOpenAI, HostKey: "vllm_host", KeyKey: "custom_api_key", KeyOptional: true, Reasoning: true},
	{ID: "custom", Label: "Other OpenAI-compatible API", Shape: shapeOpenAI, KeyKey: "custom_api_key", KeyOptional: true, Fields: []string{"openai_api_base"}, Reasoning: true},
}

var aiProviderByID = func() map[string]*aiProvider {
	m := make(map[string]*aiProvider, len(aiProviders))
	for i := range aiProviders {
		m[aiProviders[i].ID] = &aiProviders[i]
	}
	return m
}()

func aiProviderIDs() []string {
	ids := make([]string, len(aiProviders))
	for i, p := range aiProviders {
		ids[i] = p.ID
	}
	return ids
}

// aiProviderKeys are the settings only some providers show.
func aiProviderKeys() map[string]bool {
	m := map[string]bool{}
	for _, p := range aiProviders {
		for _, k := range append([]string{p.KeyKey, p.HostKey}, p.Fields...) {
			if k != "" {
				m[k] = true
			}
		}
	}
	return m
}

// llmConfig is everything a client is built from; equal configs make the
// same client.
type llmConfig struct {
	Provider   string
	Shape      string
	Label      string
	Base       string
	Key        string
	Model      string
	Reasoning  string
	JSONMode   string
	APIVersion string // Azure
	Azure      bool
}

// legacyProvider is the provider of an install from before the choice: a
// base URL other than OpenAI's meant another compatible API.
func legacyProvider(cfg map[string]string) string {
	base := strings.TrimSpace(cfg["openai_api_base"])
	if base == "" {
		return "openai"
	}
	if u, err := url.Parse(base); err == nil && u.Host == "api.openai.com" {
		return "openai"
	}
	return "custom"
}

// providerMigration is what an install from before the provider choice
// stores so it keeps working: nil when there is nothing to do.
func providerMigration(cfg map[string]string) map[string]string {
	if _, ok := cfg["ai_provider"]; ok {
		return nil
	}
	_, key := cfg["openai_api_key"]
	_, base := cfg["openai_api_base"]
	if !key && !base {
		return nil
	}
	p := legacyProvider(cfg)
	out := map[string]string{"ai_provider": p}
	if _, ok := cfg["custom_api_key"]; p == "custom" && key && !ok {
		out["custom_api_key"] = cfg["openai_api_key"]
	}
	return out
}

// resolveLLM turns settings into a client config; ok is false while the
// provider still lacks what it needs. get reads a setting with its default.
func resolveLLM(get func(string) string) (llmConfig, bool) {
	id := get("ai_provider")
	p := aiProviderByID[id]
	if p == nil {
		p = aiProviderByID["openai"]
	}
	c := llmConfig{Provider: p.ID, Shape: p.Shape, Label: p.Label, Base: p.Base, JSONMode: p.JSONMode}
	if c.JSONMode == "" {
		c.JSONMode = "object"
	}
	if p.KeyKey != "" {
		c.Key = strings.TrimSpace(get(p.KeyKey))
		if c.Key == "" && !p.KeyOptional {
			return c, false
		}
	}
	c.Model = strings.TrimSpace(get("model"))
	if c.Model == "" {
		c.Model = p.DefaultModel
	}
	if p.Reasoning {
		c.Reasoning = strings.ToLower(strings.TrimSpace(get("reasoning")))
	}
	switch p.ID {
	case "azure":
		c.Azure = true
		c.Base = azureEndpoint(get("azure_openai_endpoint"))
		c.APIVersion = strings.TrimSpace(get("azure_openai_api_version"))
		if c.Base == "" || c.Model == "" {
			return c, false
		}
	case "custom":
		c.Base = strings.TrimRight(strings.TrimSpace(get("openai_api_base")), "/")
		if c.Base == "" {
			return c, false
		}
	}
	if p.HostKey != "" {
		c.Base = hostURL(get(p.HostKey))
		if p.Shape == shapeOpenAI && !strings.HasSuffix(c.Base, "/v1") {
			c.Base += "/v1"
		}
	}
	return c, true
}

// azureEndpoint accepts a resource name or its endpoint URL.
func azureEndpoint(v string) string {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if v == "" {
		return ""
	}
	if !strings.Contains(v, "://") {
		if !strings.Contains(v, ".") {
			return "https://" + v + ".openai.azure.com"
		}
		v = "https://" + v
	}
	// A pasted endpoint may carry the path of one call.
	if i := strings.Index(v, "/openai"); i > 0 {
		v = v[:i]
	}
	return v
}

// hostURL accepts host:port or a URL for a server on your own network.
func hostURL(v string) string {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if v != "" && !strings.Contains(v, "://") {
		v = "http://" + v
	}
	return v
}
