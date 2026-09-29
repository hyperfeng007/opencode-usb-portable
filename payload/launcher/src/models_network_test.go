package main

// Optional test that exercises the real models.dev catalogue and real network
// probing. Enable with OCP_NETWORK_TEST=1 (needs internet, no API keys).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealCatalogBuildsCandidates(t *testing.T) {
	if os.Getenv("OCP_NETWORK_TEST") == "" {
		t.Skip("set OCP_NETWORK_TEST=1 to run the online test")
	}
	root := t.TempDir()
	data := filepath.Join(root, "data")
	for _, dir := range []string{"cache", filepath.Join("config", "opencode"), filepath.Join("data", "opencode")} {
		_ = os.MkdirAll(filepath.Join(data, dir), 0o755)
	}
	// a made-up key for a provider with free models: discovery must work, the
	// probes must come back as "auth" (not as a crash)
	t.Setenv("ZHIPU_API_KEY", "test-key-not-real")
	t.Setenv("OPENROUTER_API_KEY", "test-key-not-real")

	policy := defaultPolicy()
	client := probeClient("", 45_000_000_000)
	var logs []string
	list, err := loadCatalog(data, policy, client, &logs)
	if err != nil {
		t.Fatalf("catalogue fetch failed: %v", err)
	}
	if countFreeModels(list) < 50 {
		t.Fatalf("suspiciously few free models: %d", countFreeModels(list))
	}
	creds := loadCredentials(data, list, &logs)
	items := buildCandidates(list, creds, policy, &logs)
	if len(items) == 0 {
		t.Fatalf("no candidates discovered; logs:\n%v", logs)
	}
	t.Logf("catalogue: %d providers, %d free models, %d candidates for the configured keys",
		len(list), countFreeModels(list), len(items))
	for index, item := range items {
		if index >= 5 {
			break
		}
		t.Logf("  #%d %-40s ctx=%d tool=%v", index+1, item.ref(), item.Context, item.ToolCall)
	}
	if !itemHasTools(items) {
		t.Fatal("candidates must be tool-capable")
	}

	// the whole refresh must stay a no-op without valid keys
	refreshModels(root, data, filepath.Join(data, "launcher.log"), &logs, nil, "")
	raw, _ := os.ReadFile(reportPath(data))
	var report modelReport
	_ = json.Unmarshal(raw, &report)
	t.Logf("action=%s currentStatus=%s notes=%v", report.Action, report.CurrentStatus, report.Notes)
	if report.Action == "switched" {
		t.Fatalf("must not switch without a working model: %+v", report)
	}
}

func itemHasTools(items []candidate) bool {
	for _, item := range items {
		if item.ToolCall {
			return true
		}
	}
	return false
}

// Keyless mode against the real public endpoint: no API key anywhere, and the
// launcher must still find a working model that can call tools.
func TestRealKeylessDiscoveryAndToolCheck(t *testing.T) {
	if os.Getenv("OCP_NETWORK_TEST") == "" {
		t.Skip("set OCP_NETWORK_TEST=1 to run the online test")
	}
	root := t.TempDir()
	data := filepath.Join(root, "data")
	for _, dir := range []string{"cache", filepath.Join("config", "opencode"), filepath.Join("data", "opencode")} {
		_ = os.MkdirAll(filepath.Join(data, dir), 0o755)
	}
	if err := os.WriteFile(filepath.Join(data, "config", "opencode", "opencode.json"), []byte(`{
  // 用户注释
  "$schema": "https://opencode.ai/config.json",
  "model": "some-gone/model"
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	policy := defaultPolicy()
	if !policy.KeylessOnly {
		t.Fatal("keylessOnly must default to true")
	}
	var logs []string
	items := discoverKeylessCandidates(policy, &logs)
	if len(items) == 0 {
		t.Fatalf("no keyless candidates found; logs:\n%v", logs)
	}
	t.Logf("keyless candidates: %d", len(items))
	for index, item := range items {
		if index >= 4 {
			break
		}
		t.Logf("  #%d %-24s tools=%v ctx=%d source=%s", index+1, item.ref(), item.ToolCall, item.Context, item.SourceName)
	}

	client := probeClient("", 20_000_000_000)
	first := items[0]
	result := probeModel(context.Background(), client, first)
	if !result.usable() {
		t.Fatalf("best keyless candidate %s is not usable: %s %s", first.ref(), result.Status, result.Detail)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	toolOK, detail := verifyToolCalling(ctx, client, first)
	t.Logf("tool-calling check for %s: %v (%s)", first.ref(), toolOK, detail)
	if !toolOK {
		t.Fatalf("the best keyless model cannot call tools")
	}

	// full refresh: must write the provider block and switch the broken model
	refreshModels(root, data, filepath.Join(data, "launcher.log"), &logs, nil, "")
	raw, _ := os.ReadFile(filepath.Join(data, "config", "opencode", "opencode.json"))
	t.Logf("written config:\n%s", raw)
	if !strings.Contains(string(raw), `"provider"`) {
		t.Fatalf("provider block not written:\n%s", raw)
	}
	if !strings.Contains(string(raw), "// 用户注释") {
		t.Fatal("comment lost")
	}
	var report modelReport
	reportRaw, _ := os.ReadFile(reportPath(data))
	_ = json.Unmarshal(reportRaw, &report)
	if report.Action != "switched" || report.ChosenModel == "" {
		t.Fatalf("report: %+v", report)
	}
	if len(report.ToolChecks) == 0 || !report.ToolChecks[0].OK {
		t.Fatalf("tool check in report: %+v", report.ToolChecks)
	}
}
