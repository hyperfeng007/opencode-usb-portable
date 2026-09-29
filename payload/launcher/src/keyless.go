package main

// Keyless free-model sources.
//
// The point of this file: find free models that work with NO API key at all, so
// the user never has to register anywhere. Two kinds are supported:
//
//  1. built-in public endpoints - currently Pollinations.AI, which serves an
//     anonymous (no key) OpenAI-compatible endpoint. Its model list is fetched
//     live, with a static fallback if the listing fails;
//  2. local servers that are already running on the machine - Ollama, LM Studio,
//     llama.cpp, Jan, vLLM, SGLang, GPT4All, KoboldCpp. They need no key either
//     and are unlimited, so they are preferred when present.
//
// Users can add more keyless endpoints in data\model-policy.json
// ("keylessProviders"), and every candidate is verified with a real request
// before it is written into the opencode config.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// keylessModel describes one model on a keyless endpoint.
type keylessModel struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	ToolCall bool     `json:"tools"`
	Context  int      `json:"context"`
	Aliases  []string `json:"aliases"`
	Reason   bool     `json:"reasoning"`
}

// keylessEndpoint is a source that needs no credential.
type keylessEndpoint struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	BaseURL     string         `json:"baseURL"`   // OpenAI-compatible root, e.g. https://host/v1
	ModelsURL   string         `json:"modelsURL"` // listing endpoint (optional)
	Docs        string         `json:"docs"`
	Kind        string         `json:"kind"`
	SourceBonus int            `json:"sourceBonus"`
	Models      []keylessModel `json:"models"`
}

const pollinationsRoot = "https://text.pollinations.ai/openai"

// builtinKeyless are public endpoints with an anonymous tier.
func builtinKeyless() []keylessEndpoint {
	return []keylessEndpoint{{
		ID:          "pollinations",
		Name:        "Pollinations（免 key 公开接口）",
		BaseURL:     pollinationsRoot,
		ModelsURL:   "https://text.pollinations.ai/models", // 带 tools/aliases/tier 的完整清单
		Docs:        "https://pollinations.ai",
		Kind:        "openai",
		SourceBonus: 150,
		// used when the live listing cannot be fetched
		Models: []keylessModel{
			{ID: "openai-fast", Name: "openai-fast · GPT-OSS 20B（免 key）", ToolCall: true, Context: 131072, Reason: true,
				Aliases: []string{"openai", "gpt-oss", "gpt-oss-20b"}},
		},
	}}
}

// localKeyless are the usual development servers. They only become candidates
// when they actually answer on localhost.
func localKeyless() []keylessEndpoint {
	type entry struct {
		id, name string
		port     int
	}
	entries := []entry{
		{"ollama", "本机 Ollama", 11434},
		{"lmstudio", "本机 LM Studio", 1234},
		{"llamacpp", "本机 llama.cpp", 8080},
		{"jan", "本机 Jan", 1337},
		{"vllm", "本机 vLLM", 8000},
		{"sglang", "本机 SGLang", 30000},
		{"gpt4all", "本机 GPT4All", 4891},
		{"koboldcpp", "本机 KoboldCpp", 5001},
	}
	out := make([]keylessEndpoint, 0, len(entries))
	for _, item := range entries {
		out = append(out, keylessEndpoint{
			ID:          item.id,
			Name:        item.name,
			BaseURL:     fmt.Sprintf("http://127.0.0.1:%d/v1", item.port),
			ModelsURL:   fmt.Sprintf("http://127.0.0.1:%d/v1/models", item.port),
			Kind:        "openai",
			SourceBonus: 100,
		})
	}
	return out
}

// httpClientWithoutProxy is used for localhost endpoints: the Windows system
// proxy must never be asked to forward local traffic.
func httpClientWithoutProxy(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// discoverKeylessModels asks an endpoint for its model list.
func discoverKeylessModels(endpoint keylessEndpoint, timeout time.Duration) ([]keylessModel, error) {
	if endpoint.ModelsURL == "" {
		return endpoint.Models, nil
	}
	client := httpClientWithoutProxy(timeout)
	if !isLocalURL(endpoint.ModelsURL) {
		client = probeClient("", timeout) // public endpoints may need the proxy
	}
	request, err := http.NewRequest("GET", endpoint.ModelsURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %s", response.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	// Pollinations style: [{name, tools, aliases, tier}]
	var list []struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tools       bool     `json:"tools"`
		Aliases     []string `json:"aliases"`
		Reason      bool     `json:"reasoning"`
		Tier        string   `json:"tier"`
	}
	if err := json.Unmarshal(raw, &list); err == nil && len(list) > 0 && list[0].Name != "" {
		models := make([]keylessModel, 0, len(list))
		for _, item := range list {
			if item.Tier != "" && item.Tier != "anonymous" && item.Tier != "seed" {
				continue // that tier needs a token
			}
			name := item.Name
			if item.Description != "" {
				name = item.Name + " · " + trimTo(item.Description, 48)
			}
			models = append(models, keylessModel{
				ID: item.Name, Name: name, ToolCall: item.Tools, Aliases: item.Aliases,
				Context: 131072, Reason: item.Reason, // gpt-oss 级别的上下文
			})
		}
		if len(models) > 0 {
			return models, nil
		}
	}

	// OpenAI style: {"data":[{"id":"..."}]}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil && len(payload.Data) > 0 {
		models := make([]keylessModel, 0, len(payload.Data))
		for _, item := range payload.Data {
			models = append(models, keylessModel{ID: item.ID, Name: item.ID, ToolCall: true})
		}
		return models, nil
	}
	return nil, fmt.Errorf("unrecognised model list")
}

func isLocalURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// discoverKeylessCandidates builds the candidate list for keyless-only mode.
func discoverKeylessCandidates(policy modelPolicy, logs *[]string) []candidate {
	var out []candidate

	sources := policy.keylessEndpoints()
	if !policy.DisableBuiltinKeyless {
		sources = append(builtinKeyless(), sources...)
	}
	for _, endpoint := range sources {
		models, err := discoverKeylessModels(endpoint, 12*time.Second)
		if err != nil {
			if endpoint.SourceBonus >= 150 {
				// a public endpoint we could not enumerate: fall back to the models
				// that were declared for it
				models = endpoint.Models
			}
			if len(models) == 0 {
				*logs = append(*logs, fmt.Sprintf("免 key 来源 : %s 无法使用（%v）", endpoint.Name, err))
				continue
			}
			*logs = append(*logs, fmt.Sprintf("免 key 来源 : %s 列表获取失败（%v），使用内置清单", endpoint.Name, err))
		} else {
			*logs = append(*logs, fmt.Sprintf("免 key 来源 : %s 可用，%d 个模型", endpoint.Name, len(models)))
		}

		for _, model := range models {
			out = append(out, candidate{
				Provider:    endpoint.ID,
				Model:       model.ID,
				Name:        model.Name,
				BaseURL:     endpoint.BaseURL,
				Kind:        orDefault(endpoint.Kind, "openai"),
				Context:     model.Context,
				ToolCall:    model.ToolCall,
				Free:        true,
				Keyless:     true,
				SourceName:  endpoint.Name,
				SourceBonus: endpoint.SourceBonus,
			})
		}
	}

	// local servers are only reachable when they actually answer
	for _, endpoint := range localKeyless() {
		models, err := discoverKeylessModels(endpoint, 1500*time.Millisecond)
		if err != nil || len(models) == 0 {
			continue
		}
		*logs = append(*logs, fmt.Sprintf("免 key 来源 : 检测到%s，%d 个模型", endpoint.Name, len(models)))
		for _, model := range models {
			out = append(out, candidate{
				Provider:    endpoint.ID,
				Model:       model.ID,
				Name:        model.Name,
				BaseURL:     endpoint.BaseURL,
				Kind:        "openai",
				Context:     model.Context,
				ToolCall:    true,
				Free:        true,
				Keyless:     true,
				SourceName:  endpoint.Name,
				SourceBonus: endpoint.SourceBonus,
			})
		}
	}

	if len(out) == 0 {
		*logs = append(*logs, "免 key 来源 : 没有找到任何免 key 的免费模型来源")
	}
	rankCandidates(out, policy)
	return out
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// keylessEndpoints merges user-defined keyless endpoints from the policy.
func (p modelPolicy) keylessEndpoints() []keylessEndpoint {
	out := make([]keylessEndpoint, 0, len(p.KeylessProviders))
	for _, item := range p.KeylessProviders {
		if strings.TrimSpace(item.BaseURL) == "" {
			continue
		}
		models := make([]keylessModel, 0, len(item.Models))
		for _, model := range item.Models {
			models = append(models, keylessModel{ID: model, Name: model, ToolCall: true})
		}
		out = append(out, keylessEndpoint{
			ID:          item.ID,
			Name:        orDefault(item.Name, item.ID),
			BaseURL:     strings.TrimRight(item.BaseURL, "/"),
			ModelsURL:   strings.TrimRight(item.BaseURL, "/") + "/models",
			Kind:        orDefault(item.Kind, "openai"),
			SourceBonus: 120,
			Models:      models,
		})
	}
	return out
}

// verifyToolCalling asks the model to call a tool; this is what decides whether
// opencode can actually edit files and run commands with it.
func verifyToolCalling(ctx context.Context, client *http.Client, item candidate) (bool, string) {
	payload := map[string]any{
		"model": item.Model,
		"messages": []any{map[string]any{
			"role":    "user",
			"content": "Call the ping tool now with value 1. Do not answer with text.",
		}},
		"tools": []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "ping",
				"description": "returns pong",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"value": map[string]any{"type": "number"}},
					"required":   []string{"value"},
				},
			},
		}},
		"tool_choice": "auto",
		"max_tokens":  64,
		"stream":      false,
	}
	body, _ := json.Marshal(payload)
	endpoint, _, headers, err := probeRequest(item)
	if err != nil {
		return false, err.Error()
	}
	if strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint = strings.TrimSuffix(endpoint, "/chat/completions") + "/chat/completions"
	}
	request, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(body)))
	if err != nil {
		return false, err.Error()
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := client.Do(request)
	if err != nil {
		return false, err.Error()
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 32<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, fmt.Sprintf("HTTP %d: %s", response.StatusCode, trimTo(string(raw), 120))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return false, "unreadable answer"
	}
	for _, choice := range parsed.Choices {
		if len(choice.Message.ToolCalls) > 0 {
			return true, choice.Message.ToolCalls[0].Function.Name
		}
	}
	return false, "model answered without calling the tool"
}
