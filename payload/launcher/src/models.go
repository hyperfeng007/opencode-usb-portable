package main

// Free-model health check and automatic switch.
//
// The problem: opencode's default model lives in opencode.json. Free models get
// retired, renamed, rate limited or moved behind a login every few weeks, so a
// hard-coded default eventually fails with an error the user cannot fix.
//
// What this file does, on every launcher start:
//   1. loads the public model catalogue (models.dev, cached on the stick);
//   2. finds every provider the user actually has credentials for
//      (data\data\opencode\auth.json + documented environment variables);
//   3. collects the models that are free AND can call tools (opencode is an
//      agent - a model without tool support is useless here);
//   4. ranks them (coder models first, then context size, then freshness);
//   5. probes the short list with a real 16-token request through the detected
//      system proxy, in parallel, inside a fixed time budget;
//   6. if the model in the user's config answers, keeps it untouched; if it is
//      gone/limited, switches the config to the best model that answered, after
//      backing the old config up;
//   7. writes data\models-report.json so the user can see exactly what happened.
//
// Everything is configurable through data\model-policy.json (created on first
// run) and can be disabled with OPENCODE_PORTABLE_SKIP_MODEL_CHECK=1.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const catalogURL = "https://models.dev/api.json"

// userAgent identifies the launcher towards providers and the catalogue.
const userAgent = "opencode-portable/" + launcherVersion

// ---------------------------------------------------------------------------
// policy
// ---------------------------------------------------------------------------

// keylessCustom lets users add their own no-key endpoints (intranet gateways,
// a friend's llama.cpp box, ...). They are treated exactly like the built-ins.
type keylessCustom struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	BaseURL string   `json:"baseURL"`
	Kind    string   `json:"kind"`
	Models  []string `json:"models"`
}

type customProvider struct {
	ID         string   `json:"id"`
	BaseURL    string   `json:"baseURL"`
	APIKey     string   `json:"apiKey"`
	APIKeyEnv  string   `json:"apiKeyEnv"`
	FreeModels []string `json:"freeModels"`
	Kind       string   `json:"kind"` // "openai" (default), "anthropic", "google"
}

type modelPolicy struct {
	Readme                string            `json:"_readme"`
	Enabled               bool              `json:"enabled"`
	AutoSwitch            bool              `json:"autoSwitch"`
	RequireToolCall       bool              `json:"requireToolCall"`
	PreferredProviders    []string          `json:"preferredProviders"`
	PinnedModel           string            `json:"pinnedModel"`
	ExcludedModels        []string          `json:"excludedModels"`
	ExtraFreeProviders    []string          `json:"extraFreeProviders"`
	KeylessOnly           bool              `json:"keylessOnly"`
	DisableBuiltinKeyless bool              `json:"disableBuiltinKeyless"`
	KeylessProviders      []keylessCustom   `json:"keylessProviders"`
	ToolCheckCandidates   int               `json:"toolCheckCandidates"`
	MaxProbeCandidates    int               `json:"maxProbeCandidates"`
	ProbeTimeoutMs        int               `json:"probeTimeoutMs"`
	TotalBudgetMs         int               `json:"totalBudgetMs"`
	CatalogRefreshHours   int               `json:"catalogRefreshHours"`
	CustomProviders       []customProvider  `json:"customProviders"`
	unset                 map[string]string // kept for forward compatibility
}

func defaultPolicy() modelPolicy {
	return modelPolicy{
		Readme: "OpenCode 便携版：免费模型体检设置。keylessOnly=true（默认）时只在完全不需要 key 的免费模型里挑选" +
			"（内置公开免 key 接口 + 本机 Ollama/LM Studio 等）；把 keylessOnly 改成 false 才会使用需要 API key 的来源。" +
			"autoSwitch=false 表示只检测不修改配置；pinnedModel 固定某个模型；excludedModels 排除模型；" +
			"preferredProviders 指定来源优先级；keylessProviders 可追加自己的免 key 接口；" +
			"disableBuiltinKeyless=true 时不使用内置的公开免 key 接口（只想用本机模型时这样设）；customProviders 接入需要 key 的中转站。",
		Enabled:             true,
		AutoSwitch:          true,
		RequireToolCall:     true,
		KeylessOnly:         true,
		ToolCheckCandidates: 3,
		PreferredProviders:  []string{"opencode", "openrouter", "zhipuai", "modelscope", "iflowcn", "siliconflow", "alibaba", "deepseek", "moonshotai", "google", "groq", "nvidia"},
		KeylessProviders:    []keylessCustom{},
		MaxProbeCandidates:  6,
		ProbeTimeoutMs:      8000,
		TotalBudgetMs:       20000,
		CatalogRefreshHours: 12,
	}
}

func policyPath(root string) string { return filepath.Join(root, "data", "model-policy.json") }

func loadPolicy(root string, logs *[]string) modelPolicy {
	policy := defaultPolicy()
	raw, err := os.ReadFile(policyPath(root))
	if err != nil {
		if data, err := json.MarshalIndent(policy, "", "  "); err == nil {
			_ = os.MkdirAll(filepath.Dir(policyPath(root)), 0o755)
			_ = os.WriteFile(policyPath(root), data, 0o644)
		}
		return policy
	}
	var loaded modelPolicy
	if err := json.Unmarshal(raw, &loaded); err != nil {
		*logs = append(*logs, fmt.Sprintf("model policy: cannot parse %s (%v) - using defaults", policyPath(root), err))
		return policy
	}
	// fill in anything the user left out
	defaults := defaultPolicy()
	if loaded.MaxProbeCandidates == 0 {
		loaded.MaxProbeCandidates = defaults.MaxProbeCandidates
	}
	if loaded.ProbeTimeoutMs == 0 {
		loaded.ProbeTimeoutMs = defaults.ProbeTimeoutMs
	}
	if loaded.TotalBudgetMs == 0 {
		loaded.TotalBudgetMs = defaults.TotalBudgetMs
	}
	if loaded.CatalogRefreshHours == 0 {
		loaded.CatalogRefreshHours = defaults.CatalogRefreshHours
	}
	if loaded.ToolCheckCandidates == 0 {
		loaded.ToolCheckCandidates = defaults.ToolCheckCandidates
	}
	return loaded
}

// ---------------------------------------------------------------------------
// catalogue (models.dev)
// ---------------------------------------------------------------------------

type catalogModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ToolCall    bool   `json:"tool_call"`
	ReleaseDate string `json:"release_date"`
	Limit       struct {
		Context int `json:"context"`
	} `json:"limit"`
	Cost struct {
		Input  *float64 `json:"input"`
		Output *float64 `json:"output"`
	} `json:"cost"`
}

func (m catalogModel) isFree() bool {
	if m.Cost.Input == nil || m.Cost.Output == nil {
		return false
	}
	return *m.Cost.Input == 0 && *m.Cost.Output == 0
}

type catalogProvider struct {
	ID     string                  `json:"-"`
	Name   string                  `json:"name"`
	API    string                  `json:"api"`
	NPM    string                  `json:"npm"`
	Env    []string                `json:"env"`
	Models map[string]catalogModel `json:"models"`
}

type catalog map[string]catalogProvider

func catalogCachePath(dataDir string) string {
	return filepath.Join(dataDir, "cache", "models.dev.json")
}

// loadCatalog returns the catalogue, refreshing the on-stick cache when it is
// older than the policy allows. A stale cache beats no catalogue at all.
func loadCatalog(dataDir string, policy modelPolicy, client *http.Client, logs *[]string) (catalog, error) {
	cachePath := catalogCachePath(dataDir)
	fresh := false
	if info, err := os.Stat(cachePath); err == nil {
		age := time.Since(info.ModTime())
		fresh = age < time.Duration(policy.CatalogRefreshHours)*time.Hour
	}
	readCache := func() (catalog, error) {
		raw, err := os.ReadFile(cachePath)
		if err != nil {
			return nil, err
		}
		parsed := catalog{}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, err
		}
		for id, provider := range parsed {
			provider.ID = id
			parsed[id] = provider
		}
		return parsed, nil
	}

	if fresh {
		if cached, err := readCache(); err == nil {
			*logs = append(*logs, fmt.Sprintf("模型目录    : 使用本地缓存（%d 个 provider）", len(cached)))
			return cached, nil
		}
	}

	request, err := http.NewRequest("GET", catalogURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := client.Do(request)
	if err != nil {
		if cached, cacheErr := readCache(); cacheErr == nil {
			*logs = append(*logs, fmt.Sprintf("模型目录    : 更新失败（%v），改用本地缓存", err))
			return cached, nil
		}
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("models.dev 返回 %s", response.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	parsed := catalog{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	for id, provider := range parsed {
		provider.ID = id
		parsed[id] = provider
	}
	_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
	_ = os.WriteFile(cachePath, raw, 0o644)
	*logs = append(*logs, fmt.Sprintf("模型目录    : 已刷新（%d 个 provider，%d 个免费模型）",
		len(parsed), countFreeModels(parsed)))
	return parsed, nil
}

func countFreeModels(list catalog) int {
	total := 0
	for _, provider := range list {
		for _, model := range provider.Models {
			if model.isFree() {
				total++
			}
		}
	}
	return total
}

// ---------------------------------------------------------------------------
// credentials
// ---------------------------------------------------------------------------

type credentials struct {
	authKeys  map[string]string // provider id -> api key (from auth.json)
	envKeys   map[string]string // provider id -> api key (from environment)
	authPath  string
	authError string
}

func authFilePath(dataDir string) string {
	return filepath.Join(dataDir, "data", "opencode", "auth.json")
}

// loadCredentials reads opencode's own credential store plus the environment
// variables the catalogue documents for each provider.
func loadCredentials(dataDir string, list catalog, logs *[]string) credentials {
	creds := credentials{authKeys: map[string]string{}, envKeys: map[string]string{}}
	creds.authPath = authFilePath(dataDir)

	raw, err := os.ReadFile(creds.authPath)
	if err == nil {
		var parsed map[string]struct {
			Type   string `json:"type"`
			Key    string `json:"key"`
			Access string `json:"access"`
		}
		if jsonErr := json.Unmarshal(raw, &parsed); jsonErr == nil {
			for provider, entry := range parsed {
				key := strings.TrimSpace(entry.Key)
				if key == "" {
					key = strings.TrimSpace(entry.Access)
				}
				if key != "" {
					creds.authKeys[provider] = key
				}
			}
		} else {
			creds.authError = jsonErr.Error()
		}
	} else if !os.IsNotExist(err) {
		creds.authError = err.Error()
	}

	for id, provider := range list {
		for _, name := range provider.Env {
			if value := strings.TrimSpace(os.Getenv(name)); value != "" {
				creds.envKeys[id] = value
				break
			}
		}
	}
	if len(creds.authKeys) > 0 {
		*logs = append(*logs, fmt.Sprintf("凭据来源    : auth.json 中 %d 个 provider（%s）",
			len(creds.authKeys), strings.Join(sortedKeys(creds.authKeys), ", ")))
	}
	if len(creds.envKeys) > 0 {
		*logs = append(*logs, "凭据来源    : 环境变量匹配到 "+strings.Join(sortedKeys(creds.envKeys), ", "))
	}
	if creds.authError != "" {
		*logs = append(*logs, "凭据提醒    : 无法解析 auth.json："+creds.authError)
	}
	return creds
}

func (c credentials) keyFor(provider string) string {
	if key := c.authKeys[provider]; key != "" {
		return key
	}
	return c.envKeys[provider]
}

func (c credentials) has(provider string) bool { return c.keyFor(provider) != "" }

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// candidates
// ---------------------------------------------------------------------------

type candidate struct {
	Provider    string
	Model       string
	Name        string
	BaseURL     string
	APIKey      string
	Kind        string // openai | anthropic | google
	Context     int
	ToolCall    bool
	Free        bool
	Keyless     bool // works with no API key at all
	SourceName  string
	SourceBonus int
	Score       int
}

func (c candidate) ref() string { return c.Provider + "/" + c.Model }

var smallModelHint = regexp.MustCompile(`(?i)(flash|mini|nano|small|lite|air|turbo|fast)`)

// buildCandidates collects every plausible free model the user could actually
// call: catalogue free models for providers we hold credentials for, plus any
// custom providers (gateways, Ollama, one-api, ...) declared in the policy.
func buildCandidates(list catalog, creds credentials, policy modelPolicy, logs *[]string) []candidate {
	excluded := map[string]bool{}
	for _, ref := range policy.ExcludedModels {
		excluded[strings.TrimSpace(ref)] = true
	}
	extraFree := map[string]bool{}
	for _, id := range policy.ExtraFreeProviders {
		extraFree[strings.TrimSpace(id)] = true
	}

	var out []candidate
	for id, provider := range list {
		key := creds.keyFor(id)
		if key == "" {
			continue
		}
		for modelID, model := range provider.Models {
			free := model.isFree() || extraFree[id]
			if !free {
				continue
			}
			if policy.RequireToolCall && !model.ToolCall {
				continue
			}
			item := candidate{
				Provider: id,
				Model:    modelID,
				Name:     model.Name,
				BaseURL:  provider.API,
				APIKey:   key,
				Kind:     kindFor(id, provider.NPM, provider.API),
				Context:  model.Limit.Context,
				ToolCall: model.ToolCall,
				Free:     true,
			}
			if excluded[item.ref()] {
				continue
			}
			out = append(out, item)
		}
	}

	for _, custom := range policy.CustomProviders {
		key := strings.TrimSpace(custom.APIKey)
		if key == "" && custom.APIKeyEnv != "" {
			key = strings.TrimSpace(os.Getenv(custom.APIKeyEnv))
		}
		if key == "" {
			key = creds.keyFor(custom.ID)
		}
		if custom.BaseURL == "" || (key == "" && !strings.HasPrefix(custom.BaseURL, "http://localhost") &&
			!strings.HasPrefix(custom.BaseURL, "http://127.0.0.1")) {
			continue
		}
		kind := custom.Kind
		if kind == "" {
			kind = "openai"
		}
		for _, modelID := range custom.FreeModels {
			item := candidate{
				Provider: custom.ID,
				Model:    modelID,
				Name:     modelID,
				BaseURL:  custom.BaseURL,
				APIKey:   key,
				Kind:     kind,
				Free:     true,
			}
			if excluded[item.ref()] {
				continue
			}
			out = append(out, item)
		}
	}

	rankCandidates(out, policy)
	*logs = append(*logs, fmt.Sprintf("免费候选    : %d 个模型（要求工具调用=%v）", len(out), policy.RequireToolCall))
	return out
}

func kindFor(providerID, npm, api string) string {
	switch {
	case providerID == "google" || strings.Contains(npm, "@ai-sdk/google"):
		return "google"
	case providerID == "anthropic" || strings.Contains(npm, "@ai-sdk/anthropic"):
		return "anthropic"
	default:
		return "openai"
	}
}

// rankCandidates scores "免费且好用": agent-capable, coding-oriented, large
// context, recently released, and from a preferred provider.
func rankCandidates(items []candidate, policy modelPolicy) {
	preferred := map[string]int{}
	for index, id := range policy.PreferredProviders {
		preferred[strings.TrimSpace(id)] = index
	}
	for i := range items {
		item := &items[i]
		score := 0
		if item.ToolCall {
			score += 400
		}
		if item.Context > 0 {
			score += int(log2(float64(item.Context))) * 8
		}
		lower := strings.ToLower(item.Model)
		switch {
		case strings.Contains(lower, "coder"), strings.Contains(lower, "-code"):
			score += 120
		case strings.Contains(lower, "code"):
			score += 60
		}
		if strings.Contains(lower, "flash") {
			score += 30
		}
		for _, weak := range []string{"7b", "8b", "3b", "1b", "tiny", "nano", "embed", "bge", "rerank", "whisper", "tts", "image", "video"} {
			if strings.Contains(lower, weak) {
				score -= 120
			}
		}
		if index, ok := preferred[item.Provider]; ok {
			score += 200 - index*5
		}
		if !item.Free {
			score -= 150
		}
		if item.Keyless {
			score += item.SourceBonus
		}
		item.Score = score
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].Score > items[b].Score })
}

func log2(v float64) float64 {
	result := 0.0
	for v >= 2 {
		v /= 2
		result++
	}
	return result
}

// isSmallModel is used to suggest a cheap model for titles and other light work.
func isSmallModel(id string) bool { return smallModelHint.MatchString(id) }

// ---------------------------------------------------------------------------
// probing
// ---------------------------------------------------------------------------

type probeResult struct {
	Ref      string `json:"ref"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Status   string `json:"status"` // ok, gone, limited, auth, error, timeout, skipped
	Detail   string `json:"detail,omitempty"`
	MS       int64  `json:"ms"`
}

func (r probeResult) usable() bool { return r.Status == "ok" }

func probeClient(proxyURL string, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: timeout,
		ForceAttemptHTTP2:     true,
	}
	if parsed, err := url.Parse(proxyURL); err == nil && parsed.Host != "" {
		transport.Proxy = func(request *http.Request) (*url.URL, error) {
			host := request.URL.Hostname()
			if host == "127.0.0.1" || host == "localhost" || host == "::1" {
				return nil, nil
			}
			return parsed, nil
		}
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func probeModel(ctx context.Context, client *http.Client, item candidate) probeResult {
	started := time.Now()
	result := probeResult{Ref: item.ref(), Provider: item.Provider, Model: item.Model}

	endpoint, body, headers, err := probeRequest(item)
	if err != nil {
		result.Status = "skipped"
		result.Detail = err.Error()
		return result
	}

	request, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		result.Status = "skipped"
		result.Detail = err.Error()
		return result
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("User-Agent", userAgent)

	response, err := client.Do(request)
	result.MS = time.Since(started).Milliseconds()
	if err != nil {
		if ctx.Err() != nil {
			result.Status = "timeout"
		} else {
			result.Status = "error"
		}
		result.Detail = trimTo(err.Error(), 160)
		return result
	}
	defer response.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
	text := trimTo(strings.TrimSpace(string(snippet)), 200)

	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		result.Status = "ok"
	case response.StatusCode == 400 && strings.Contains(strings.ToLower(text), "max_tokens"):
		// the endpoint answered and understood the request; only our tiny token
		// budget was rejected, which still proves the model is reachable
		result.Status = "ok"
		result.Detail = "400 but reachable: " + text
	case response.StatusCode == 401 || response.StatusCode == 403:
		result.Status = "auth"
		result.Detail = text
	case response.StatusCode == 404:
		result.Status = "gone"
		result.Detail = text
	case response.StatusCode == 429:
		result.Status = "limited"
		result.Detail = text
	default:
		result.Status = "error"
		result.Detail = fmt.Sprintf("HTTP %d %s", response.StatusCode, text)
	}
	return result
}

func probeRequest(item candidate) (string, []byte, map[string]string, error) {
	base := strings.TrimRight(item.BaseURL, "/")
	headers := map[string]string{"Content-Type": "application/json"}
	switch item.Kind {
	case "google":
		if base == "" {
			return "", nil, nil, fmt.Errorf("no baseURL")
		}
		endpoint := base + "/v1beta/models/" + url.PathEscape(item.Model) + ":generateContent"
		payload := map[string]any{
			"contents":         []any{map[string]any{"parts": []any{map[string]any{"text": "ping"}}}},
			"generationConfig": map[string]any{"maxOutputTokens": 16},
		}
		body, _ := json.Marshal(payload)
		headers["x-goog-api-key"] = item.APIKey
		return endpoint, body, headers, nil
	case "anthropic":
		if base == "" {
			return "", nil, nil, fmt.Errorf("no baseURL")
		}
		endpoint := base
		if !strings.HasSuffix(endpoint, "/messages") {
			if !strings.HasSuffix(endpoint, "/v1") {
				endpoint += "/v1"
			}
			endpoint += "/messages"
		}
		payload := map[string]any{
			"model":      item.Model,
			"max_tokens": 16,
			"messages":   []any{map[string]any{"role": "user", "content": "ping"}},
		}
		body, _ := json.Marshal(payload)
		headers["x-api-key"] = item.APIKey
		headers["anthropic-version"] = "2023-06-01"
		return endpoint, body, headers, nil
	default:
		if base == "" {
			return "", nil, nil, fmt.Errorf("no baseURL")
		}
		endpoint := base + "/chat/completions"
		payload := map[string]any{
			"model":      item.Model,
			"messages":   []any{map[string]any{"role": "user", "content": "ping"}},
			"max_tokens": 16,
			"stream":     false,
		}
		body, _ := json.Marshal(payload)
		if item.APIKey != "" {
			headers["Authorization"] = "Bearer " + item.APIKey
		}
		return endpoint, body, headers, nil
	}
}

func trimTo(text string, limit int) string {
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

// probeMany runs the probes in parallel and stops at the time budget.
func probeMany(ctx context.Context, client *http.Client, items []candidate, onProgress func(done, total int)) []probeResult {
	results := make([]probeResult, len(items))
	var group sync.WaitGroup
	done := 0
	var lock sync.Mutex

	for index, item := range items {
		group.Add(1)
		go func(index int, item candidate) {
			defer group.Done()
			results[index] = probeModel(ctx, client, item)
			if onProgress != nil {
				lock.Lock()
				done++
				current := done
				lock.Unlock()
				onProgress(current, len(items))
			}
		}(index, item)
	}
	group.Wait()
	return results
}
