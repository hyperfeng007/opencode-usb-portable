package main

// Tests for the free-model health check. They use a fake OpenAI-compatible
// gateway, so no real key or network access is needed.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeGateway(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		switch payload.Model {
		case "good-model":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"1","choices":[{"message":{"role":"assistant","content":"pong"}}]}`)
		case "budget-model":
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"max_tokens: must be greater than 16"}}`)
		case "dead-model":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"message":"model not found"}}`)
		case "limited-model":
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"rate limit exceeded"}}`)
		case "paid-model":
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// usbTree builds a minimal portable tree: catalogue cache, credentials and a
// user config whose default model is broken.
func usbTree(t *testing.T, gatewayURL string, userModel string) string {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	for _, dir := range []string{
		filepath.Join("cache"), filepath.Join("config", "opencode"), filepath.Join("data", "opencode"), "backup",
	} {
		if err := os.MkdirAll(filepath.Join(data, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// a synthetic models.dev catalogue: one provider with a free tool-capable
	// coder model and a couple of free models that do not work
	catalog := map[string]any{
		"testprov": map[string]any{
			"name": "Test Provider",
			"api":  gatewayURL + "/v1",
			"npm":  "@ai-sdk/openai-compatible",
			"env":  []string{"TESTPROV_API_KEY"},
			"models": map[string]any{
				"good-model":    map[string]any{"name": "Good Coder", "tool_call": true, "limit": map[string]any{"context": 262144}, "cost": map[string]any{"input": 0, "output": 0}},
				"dead-model":    map[string]any{"name": "Dead", "tool_call": true, "limit": map[string]any{"context": 128000}, "cost": map[string]any{"input": 0, "output": 0}},
				"limited-model": map[string]any{"name": "Limited", "tool_call": true, "limit": map[string]any{"context": 128000}, "cost": map[string]any{"input": 0, "output": 0}},
				"paid-model":    map[string]any{"name": "Paid", "tool_call": true, "limit": map[string]any{"context": 128000}, "cost": map[string]any{"input": 25, "output": 50}},
			},
		},
	}
	raw, _ := json.Marshal(catalog)
	if err := os.WriteFile(filepath.Join(data, "cache", "models.dev.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	auth := map[string]any{"testprov": map[string]any{"type": "api", "key": "test-key"}}
	authRaw, _ := json.Marshal(auth)
	if err := os.WriteFile(filepath.Join(data, "data", "opencode", "auth.json"), authRaw, 0o644); err != nil {
		t.Fatal(err)
	}

	config := "{\n  // keep my comments\n  \"$schema\": \"https://opencode.ai/config.json\",\n  \"model\": \"" + userModel + "\",\n  \"autoupdate\": false,\n}\n"
	if err := os.WriteFile(filepath.Join(data, "config", "opencode", "opencode.jsonc"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// writePolicy stores a model policy for the test tree.
func writePolicy(t *testing.T, root string, mutate func(*modelPolicy)) {
	t.Helper()
	policy := defaultPolicy()
	if mutate != nil {
		mutate(&policy)
	}
	raw, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath(root), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStripJSONCAndSetStringKey(t *testing.T) {
	text := "{\n  // a comment\n  \"model\": \"old/model\", /* inline */\n  \"x\": [1, 2,],\n}"
	if got := findStringValue(stripJSONC(text), "model"); got != "old/model" {
		t.Fatalf("findStringValue = %q", got)
	}
	updated, ok := setStringKey(text, "model", "new/provider-model")
	if !ok {
		t.Fatal("setStringKey refused to edit")
	}
	if !strings.Contains(updated, `"model": "new/provider-model"`) {
		t.Fatalf("model not replaced: %s", updated)
	}
	if !strings.Contains(updated, "// a comment") {
		t.Fatal("comment was lost")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stripJSONC(updated)), &parsed); err != nil {
		t.Fatalf("edited config does not parse: %v", err)
	}
	if parsed["model"] != "new/provider-model" {
		t.Fatalf("parsed model = %v", parsed["model"])
	}
}

func TestSetStringKeyInsertsWhenMissing(t *testing.T) {
	updated, ok := setStringKey("{\n  \"$schema\": \"x\"\n}\n", "model", "a/b")
	if !ok {
		t.Fatal("insert failed")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stripJSONC(updated)), &parsed); err != nil {
		t.Fatalf("inserted config does not parse: %v\n%s", err, updated)
	}
	if parsed["model"] != "a/b" {
		t.Fatalf("model = %v", parsed["model"])
	}
}

func TestRankingPrefersToolCapableCoder(t *testing.T) {
	items := []candidate{
		{Provider: "p", Model: "chat-tiny-3b", ToolCall: false, Context: 8192, Free: true},
		{Provider: "p", Model: "general-large", ToolCall: true, Context: 200000, Free: true},
		{Provider: "p", Model: "qwen3-coder-next", ToolCall: true, Context: 262144, Free: true},
	}
	rankCandidates(items, defaultPolicy())
	if items[0].Model != "qwen3-coder-next" {
		t.Fatalf("best candidate = %s, want qwen3-coder-next", items[0].Model)
	}
	if items[2].Model != "chat-tiny-3b" {
		t.Fatalf("worst candidate = %s, want chat-tiny-3b", items[2].Model)
	}
}

func TestProbeStatuses(t *testing.T) {
	gateway := fakeGateway(t)
	client := probeClient("", 8_000_000_000)
	cases := map[string]string{
		"good-model":    "ok",
		"budget-model":  "ok", // 400 about max_tokens still proves reachability
		"dead-model":    "gone",
		"limited-model": "limited",
		"paid-model":    "auth",
	}
	for model, want := range cases {
		item := candidate{Provider: "testprov", Model: model, BaseURL: gateway.URL + "/v1", APIKey: "k", Kind: "openai"}
		if got := probeModel(t.Context(), client, item); got.Status != want {
			t.Errorf("probe %s = %s (%s), want %s", model, got.Status, got.Detail, want)
		}
	}
}

// The main scenario: the configured model is gone, so the launcher must switch
// the config to a working free model and keep a backup of the old file.
func TestRefreshModelsSwitchesBrokenModel(t *testing.T) {
	// these exercise the keyed path, so keyless-only mode is turned off
	gateway := fakeGateway(t)
	root := usbTree(t, gateway.URL, "testprov/dead-model")
	data := filepath.Join(root, "data")
	configPath := filepath.Join(data, "config", "opencode", "opencode.jsonc")
	writePolicy(t, root, func(p *modelPolicy) { p.KeylessOnly = false })

	var logs []string
	refreshModels(root, data, filepath.Join(data, "launcher.log"), &logs, nil, "")

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"model": "testprov/good-model"`) {
		t.Fatalf("config was not switched, got:\n%s\nlogs:\n%s", raw, strings.Join(logs, "\n"))
	}
	if !strings.Contains(string(raw), "// keep my comments") {
		t.Fatal("comments were lost during the switch")
	}

	reportRaw, err := os.ReadFile(reportPath(data))
	if err != nil {
		t.Fatal(err)
	}
	var report modelReport
	if err := json.Unmarshal(reportRaw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Action != "switched" || report.ChosenModel != "testprov/good-model" {
		t.Fatalf("report = %+v", report)
	}
	if report.CurrentStatus != "gone" {
		t.Fatalf("current status = %s, want gone", report.CurrentStatus)
	}
	backups, _ := filepath.Glob(filepath.Join(data, "backup", "*.bak"))
	if len(backups) != 1 {
		t.Fatalf("expected exactly one backup, got %v", backups)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "已切换到 testprov/good-model") {
		t.Fatalf("switch was not logged:\n%s", strings.Join(logs, "\n"))
	}
}

// When the configured model still answers, the config must not be touched.
func TestRefreshModelsKeepsHealthyModel(t *testing.T) {
	// these exercise the keyed path, so keyless-only mode is turned off
	gateway := fakeGateway(t)
	root := usbTree(t, gateway.URL, "testprov/good-model")
	data := filepath.Join(root, "data")
	configPath := filepath.Join(data, "config", "opencode", "opencode.jsonc")
	writePolicy(t, root, func(p *modelPolicy) { p.KeylessOnly = false })

	before, _ := os.ReadFile(configPath)
	var logs []string
	refreshModels(root, data, filepath.Join(data, "launcher.log"), &logs, nil, "")
	after, _ := os.ReadFile(configPath)

	if string(before) != string(after) {
		t.Fatalf("healthy config was modified:\n%s", after)
	}
	reportRaw, _ := os.ReadFile(reportPath(data))
	var report modelReport
	_ = json.Unmarshal(reportRaw, &report)
	if report.Action != "kept" {
		t.Fatalf("action = %s, want kept", report.Action)
	}
	backups, _ := filepath.Glob(filepath.Join(data, "backup", "*.bak"))
	if len(backups) != 0 {
		t.Fatalf("no backup expected, got %v", backups)
	}
}

// No credentials anywhere -> graceful no-op with an explanatory note.
func TestRefreshModelsWithoutCredentials(t *testing.T) {
	// these exercise the keyed path, so keyless-only mode is turned off
	gateway := fakeGateway(t)
	root := usbTree(t, gateway.URL, "testprov/dead-model")
	data := filepath.Join(root, "data")
	_ = os.Remove(filepath.Join(data, "data", "opencode", "auth.json"))
	configPath := filepath.Join(data, "config", "opencode", "opencode.jsonc")
	writePolicy(t, root, func(p *modelPolicy) { p.KeylessOnly = false })
	before, _ := os.ReadFile(configPath)

	var logs []string
	refreshModels(root, data, filepath.Join(data, "launcher.log"), &logs, nil, "")
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Fatal("config must not change when there are no credentials")
	}
	reportRaw, _ := os.ReadFile(reportPath(data))
	var report modelReport
	_ = json.Unmarshal(reportRaw, &report)
	if report.Action != "no-candidates" {
		t.Fatalf("action = %s, want no-candidates", report.Action)
	}
	if len(report.Notes) == 0 {
		t.Fatal("expected an explanatory note")
	}
}

// autoSwitch=false must only report, never edit.
func TestRefreshModelsRespectsAutoSwitchOff(t *testing.T) {
	gateway := fakeGateway(t)
	root := usbTree(t, gateway.URL, "testprov/dead-model")
	data := filepath.Join(root, "data")
	writePolicy(t, root, func(p *modelPolicy) { p.KeylessOnly = false; p.AutoSwitch = false })
	configPath := filepath.Join(data, "config", "opencode", "opencode.jsonc")
	before, _ := os.ReadFile(configPath)

	var logs []string
	refreshModels(root, data, filepath.Join(data, "launcher.log"), &logs, nil, "")
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Fatal("config changed although autoSwitch=false")
	}
	reportRaw, _ := os.ReadFile(reportPath(data))
	var report modelReport
	_ = json.Unmarshal(reportRaw, &report)
	if report.Action != "detected-only" {
		t.Fatalf("action = %s, want detected-only", report.Action)
	}
}

// ---------------------------------------------------------------------------
// keyless-only mode
// ---------------------------------------------------------------------------

func keylessGateway(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"free-coder-20b","object":"model"}]}`)
			return
		}
		var payload struct {
			Model string           `json:"model"`
			Tools []map[string]any `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.Header().Set("Content-Type", "application/json")
		if len(payload.Tools) > 0 {
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"ping","arguments":"{\"value\":1}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func keylessTree(t *testing.T, endpoint string, userModel string, policyFn func(*modelPolicy)) string {
	root := usbTree(t, endpoint, userModel)
	data := filepath.Join(root, "data")
	_ = os.Remove(filepath.Join(data, "data", "opencode", "auth.json"))
	writePolicy(t, root, func(p *modelPolicy) {
		p.KeylessOnly = true
		p.DisableBuiltinKeyless = true
		p.KeylessProviders = []keylessCustom{{
			ID: "localkeyless", Name: "本机免 key 接口", BaseURL: endpoint + "/v1", Models: []string{"free-coder-20b"},
		}}
		if policyFn != nil {
			policyFn(p)
		}
	})
	return root
}

func TestKeylessOnlySwitchesAndWritesProviderBlock(t *testing.T) {
	gateway := keylessGateway(t)
	root := keylessTree(t, gateway.URL, "testprov/dead-model", nil)
	data := filepath.Join(root, "data")
	configPath := filepath.Join(data, "config", "opencode", "opencode.jsonc")

	var logs []string
	refreshModels(root, data, filepath.Join(data, "launcher.log"), &logs, nil, "")

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `"model": "localkeyless/free-coder-20b"`) {
		t.Fatalf("model not switched:\n%s\nlogs:\n%s", text, strings.Join(logs, "\n"))
	}
	if !strings.Contains(text, `"provider": {`) || !strings.Contains(text, `"localkeyless": {`) {
		t.Fatalf("provider block missing:\n%s", text)
	}
	if !strings.Contains(text, gateway.URL+"/v1") {
		t.Fatalf("baseURL missing:\n%s", text)
	}
	if !strings.Contains(text, "// keep my comments") {
		t.Fatal("comments were lost")
	}
	if err := verifyConfig(text, "localkeyless", gateway.URL+"/v1", "localkeyless/free-coder-20b"); err != nil {
		t.Fatalf("config verification failed: %v\n%s", err, text)
	}

	reportRaw, _ := os.ReadFile(reportPath(data))
	var report modelReport
	_ = json.Unmarshal(reportRaw, &report)
	if report.Action != "switched" || !report.KeylessOnly {
		t.Fatalf("report = %+v", report)
	}
	if len(report.ToolChecks) == 0 || !report.ToolChecks[0].OK {
		t.Fatalf("tool check did not pass: %+v", report.ToolChecks)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "免 key") {
		t.Fatal("keyless source was not logged")
	}
	backups, _ := filepath.Glob(filepath.Join(data, "backup", "*.bak"))
	if len(backups) != 1 {
		t.Fatalf("expected one backup, got %v", backups)
	}
}

// keylessOnly must ignore providers that need a key, even when one is configured.
func TestKeylessOnlyIgnoresKeyedProviders(t *testing.T) {
	gateway := fakeGateway(t) // the keyed provider from usbTree, reachable
	root := usbTree(t, gateway.URL, "testprov/dead-model")
	data := filepath.Join(root, "data")
	configPath := filepath.Join(data, "config", "opencode", "opencode.jsonc")
	writePolicy(t, root, func(p *modelPolicy) {
		p.KeylessOnly = true
		p.DisableBuiltinKeyless = true // no keyless sources at all
	})
	before, _ := os.ReadFile(configPath)

	var logs []string
	refreshModels(root, data, filepath.Join(data, "launcher.log"), &logs, nil, "")
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Fatalf("config changed in keyless-only mode even though the only available provider needs a key:\n%s", after)
	}
	reportRaw, _ := os.ReadFile(reportPath(data))
	var report modelReport
	_ = json.Unmarshal(reportRaw, &report)
	if report.Action != "no-candidates" {
		t.Fatalf("action = %s, want no-candidates", report.Action)
	}
	if !strings.Contains(strings.Join(report.Notes, " "), "免 key") {
		t.Fatalf("notes should explain the keyless limitation: %+v", report.Notes)
	}
}

// The provider block must merge into a config that already has providers.
func TestUpsertProviderBlockMergesWithExistingProviders(t *testing.T) {
	text := `{
  // my own provider stays untouched
  "provider": {
    "mine": { "npm": "x", "options": { "baseURL": "https://mine.example/v1" } }
  },
  "model": "mine/keep-me"
}
`
	item := candidate{Provider: "pollinations", Model: "openai-fast", Name: "GPT-OSS 20B",
		BaseURL: "https://text.pollinations.ai/openai", SourceName: "Pollinations", ToolCall: true, Context: 131072, Keyless: true}
	updated, ok := upsertProviderBlock(text, "pollinations", []candidate{item}, item)
	if !ok {
		t.Fatal("upsert failed")
	}
	if !strings.Contains(updated, "// my own provider stays untouched") {
		t.Fatal("comment lost")
	}
	if !strings.Contains(updated, `"mine": {`) {
		t.Fatal("existing provider lost")
	}
	if err := verifyConfig(updated, "pollinations", item.BaseURL, ""); err != nil {
		t.Fatalf("verifyConfig: %v\n%s", err, updated)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stripJSONC(updated)), &parsed); err != nil {
		t.Fatalf("edited text does not parse: %v\n%s", err, updated)
	}
}

// And it must create the whole provider section in an empty config.
func TestUpsertProviderBlockCreatesProviderSection(t *testing.T) {
	text := "{\n  \"$schema\": \"https://opencode.ai/config.json\"\n}\n"
	item := candidate{Provider: "ollama", Model: "qwen3-coder:30b", Name: "Qwen3 Coder 30B",
		BaseURL: "http://127.0.0.1:11434/v1", SourceName: "本机 Ollama", ToolCall: true, Keyless: true}
	updated, ok := upsertProviderBlock(text, "ollama", []candidate{item}, item)
	if !ok {
		t.Fatal("upsert failed")
	}
	if err := verifyConfig(updated, "ollama", item.BaseURL, ""); err != nil {
		t.Fatalf("verifyConfig: %v\n%s", err, updated)
	}
}

func TestVerifyToolCalling(t *testing.T) {
	gateway := keylessGateway(t)
	client := probeClient("", 10_000_000_000)
	item := candidate{Provider: "localkeyless", Model: "free-coder-20b", BaseURL: gateway.URL + "/v1", Kind: "openai"}
	ok, detail := verifyToolCalling(context.Background(), client, item)
	if !ok {
		t.Fatalf("tool call was not detected: %s", detail)
	}

	// a plain chat endpoint must be reported as failing the check
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"I cannot call tools."}}]}`)
	}))
	defer plain.Close()
	plainItem := candidate{Provider: "plain", Model: "chat-only", BaseURL: plain.URL + "/v1", Kind: "openai"}
	if ok, _ := verifyToolCalling(context.Background(), client, plainItem); ok {
		t.Fatal("a model without tool support must not pass the check")
	}
}
