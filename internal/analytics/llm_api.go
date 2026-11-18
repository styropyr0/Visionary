package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// draftLLM reads the AI analyst settings as the form holds them: values in
// the body over the stored ones, null for a default.
func (s *Server) draftLLM(w http.ResponseWriter, r *http.Request) (llmConfig, *aiProvider, bool, bool) {
	if !s.require(w, r, "admin") {
		return llmConfig{}, nil, false, false
	}
	var body struct {
		Values map[string]*string `json:"values"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return llmConfig{}, nil, false, false
	}
	get := func(k string) string {
		if v, ok := body.Values[k]; ok {
			if d := confByKey[k]; d != nil && v == nil {
				return d.Default
			}
			if v != nil {
				return *v
			}
		}
		return s.conf(k)
	}
	cfg, ok := resolveLLM(get)
	return cfg, aiProviderByID[cfg.Provider], ok, true
}

// missing says what the provider still needs.
func missing(p *aiProvider) string {
	switch p.ID {
	case "azure":
		return "Azure OpenAI needs its API key, resource and deployment."
	case "custom":
		return "Add the API's base URL."
	}
	return "Add the " + p.Label + " API key."
}

// handleAIModels lists the models the chosen provider offers, falling back
// to known ones when it can't be asked.
func (s *Server) handleAIModels(w http.ResponseWriter, r *http.Request) {
	cfg, p, ok, proceed := s.draftLLM(w, r)
	if !proceed {
		return
	}
	out := map[string]any{"provider": p.ID, "default_model": p.DefaultModel, "models": p.Models}
	if !ok && p.ID != "azure" {
		out["error"] = missing(p)
		writeJSON(w, http.StatusOK, out)
		return
	}
	if p.ID == "azure" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	models, err := newLLMClient(cfg, &http.Client{Timeout: 15 * time.Second}).listModels(ctx)
	if err != nil {
		out["error"] = err.Error()
	} else if len(models) > 0 {
		out["models"] = models
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAITest asks the chosen provider for a tiny JSON answer.
func (s *Server) handleAITest(w http.ResponseWriter, r *http.Request) {
	cfg, p, ok, proceed := s.draftLLM(w, r)
	if !proceed {
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "provider": p.Label, "error": missing(p)})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	c := newLLMClient(cfg, nil)
	start := time.Now()
	content, err := c.chatJSON(ctx, []chatMessage{
		{Role: "system", Content: "You check that a connection works. Reply with a JSON object only."},
		{Role: "user", Content: `Reply with exactly this JSON object: {"ok": true}`},
	})
	if err == nil {
		var v map[string]any
		if json.Unmarshal([]byte(content), &v) != nil {
			err = errors.New(p.Label + ": the answer was not a JSON object")
		}
	}
	res := map[string]any{"ok": err == nil, "provider": p.Label, "model": c.modelName(), "ms": time.Since(start).Milliseconds()}
	if err != nil {
		res["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, res)
}
