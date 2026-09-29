package main

// The second half of the free-model feature: reading the user's opencode
// config, deciding what to do, and writing the result back safely.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// the user's opencode config
// ---------------------------------------------------------------------------

type userConfig struct {
	Path       string
	Text       string
	Model      string
	SmallModel string
	providers  map[string]struct {
		NPM     string `json:"npm"`
		Options struct {
			BaseURL string `json:"baseURL"`
			APIKey  string `json:"apiKey"`
		} `json:"options"`
		Models map[string]json.RawMessage `json:"models"`
	}
}

// opencodeConfigPath finds the global config inside the portable folder
// (XDG_CONFIG_HOME=data\config is exported by the launcher).
func opencodeConfigPath(dataDir string) string {
	jsonPath := filepath.Join(dataDir, "config", "opencode", "opencode.json")
	jsoncPath := jsonPath + "c"
	if _, err := os.Stat(jsonPath); err == nil {
		return jsonPath
	}
	if _, err := os.Stat(jsoncPath); err == nil {
		return jsoncPath
	}
	return jsonPath // default target when the user has no config yet
}

func loadUserConfig(dataDir string) userConfig {
	config := userConfig{Path: opencodeConfigPath(dataDir)}
	raw, err := os.ReadFile(config.Path)
	if err != nil {
		return config
	}
	config.Text = string(raw)

	var parsed struct {
		Model      string `json:"model"`
		SmallModel string `json:"small_model"`
		Providers  map[string]struct {
			NPM     string `json:"npm"`
			Options struct {
				BaseURL string `json:"baseURL"`
				APIKey  string `json:"apiKey"`
			} `json:"options"`
			Models map[string]json.RawMessage `json:"models"`
		} `json:"provider"`
	}
	cleaned := stripJSONC(config.Text)
	// remember provider options even if the file has a syntax error later on
	if err := json.Unmarshal([]byte(cleaned), &parsed); err == nil {
		config.Model = strings.TrimSpace(parsed.Model)
		config.SmallModel = strings.TrimSpace(parsed.SmallModel)
		config.providers = parsed.Providers
	} else {
		// fall back to a plain text search so a commented file still works
		config.Model = findStringValue(cleaned, "model")
		config.SmallModel = findStringValue(cleaned, "small_model")
	}
	if config.providers == nil {
		config.providers = map[string]struct {
			NPM     string `json:"npm"`
			Options struct {
				BaseURL string `json:"baseURL"`
				APIKey  string `json:"apiKey"`
			} `json:"options"`
			Models map[string]json.RawMessage `json:"models"`
		}{}
	}
	return config
}

var stringValuePattern = regexp.MustCompile(`"([a-zA-Z_]+)"\s*:\s*"([^"]*)"`)

func findStringValue(text, key string) string {
	for _, match := range stringValuePattern.FindAllStringSubmatch(text, -1) {
		if match[1] == key {
			return match[2]
		}
	}
	return ""
}

// candidateFromConfig turns "<provider>/<model>" into something we can probe,
// using the user's own provider block when the catalogue does not know it.
func candidateFromConfig(ref string, config userConfig, creds credentials) (candidate, bool) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return candidate{}, false
	}
	providerID, modelID := parts[0], parts[1]
	item := candidate{Provider: providerID, Model: modelID, Name: modelID}

	if block, ok := config.providers[providerID]; ok {
		item.BaseURL = block.Options.BaseURL
		item.Kind = kindFor(providerID, block.NPM, block.Options.BaseURL)
		if key := strings.TrimSpace(block.Options.APIKey); key != "" {
			item.APIKey = key
		}
	}
	if item.APIKey == "" {
		item.APIKey = creds.keyFor(providerID)
	}
	if item.BaseURL == "" {
		return candidate{}, false
	}
	return item, true
}

// ---------------------------------------------------------------------------
// writing the config back (comments and unknown keys preserved)
// ---------------------------------------------------------------------------

var jsoncBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// stripJSONC removes // and /* */ comments plus trailing commas so the file can
// be parsed by encoding/json without losing anything on disk.
func stripJSONC(text string) string {
	var out strings.Builder
	inString := false
	escaped := false
	for index := 0; index < len(text); index++ {
		char := text[index]
		if inString {
			out.WriteByte(char)
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		if char == '"' {
			inString = true
			out.WriteByte(char)
			continue
		}
		if char == '/' && index+1 < len(text) {
			if text[index+1] == '/' {
				for index < len(text) && text[index] != '\n' {
					index++
				}
				out.WriteByte('\n')
				continue
			}
			if text[index+1] == '*' {
				end := strings.Index(text[index+2:], "*/")
				if end < 0 {
					break
				}
				index += end + 3
				continue
			}
		}
		out.WriteByte(char)
	}
	cleaned := out.String()
	cleaned = regexp.MustCompile(`,\s*([}\]])`).ReplaceAllString(cleaned, "$1")
	return cleaned
}

// setStringKey edits one top-level string key in place, keeping formatting and
// comments of a .jsonc file intact.
func setStringKey(text, key, value string) (string, bool) {
	pattern := regexp.MustCompile(`(?m)^(\s*)"` + regexp.QuoteMeta(key) + `"\s*:\s*"[^"]*"`)
	quoted := strconv.Quote(value)
	if pattern.MatchString(text) {
		return pattern.ReplaceAllString(text, "${1}\""+key+"\": "+quoted), true
	}
	brace := strings.Index(text, "{")
	if brace < 0 {
		return text, false
	}
	rest := strings.TrimSpace(text[brace+1:])
	if rest == "}" || rest == "" {
		return text[:brace+1] + "\n  \"" + key + "\": " + quoted + "\n" + text[brace+1:], true
	}
	return text[:brace+1] + "\n  \"" + key + "\": " + quoted + "," + text[brace+1:], true
}

// writeKeylessChoice writes both the provider definition and the model choice.
func writeKeylessChoice(dataDir string, config userConfig, item candidate, all []candidate, smallRef string, logs *[]string) writeOutcome {
	outcome := writeOutcome{Path: config.Path, Previous: config.Model}
	if config.Text == "" {
		if err := os.MkdirAll(filepath.Dir(config.Path), 0o755); err != nil {
			outcome.Reason = err.Error()
			return outcome
		}
		config.Text = "{\n  \"$schema\": \"https://opencode.ai/config.json\"\n}\n"
	}

	updated, ok := upsertProviderBlock(config.Text, item.Provider, candidateSourceModels(item, all), item)
	if !ok {
		outcome.Reason = "无法在配置中写入 provider 块"
		return outcome
	}
	if next, ok := setStringKey(updated, "model", item.ref()); ok {
		updated = next
	} else {
		outcome.Reason = "无法写入 model"
		return outcome
	}
	if smallRef != "" && config.SmallModel != smallRef {
		if next, ok := setStringKey(updated, "small_model", smallRef); ok {
			updated = next
		}
	}
	if err := verifyConfig(updated, item.Provider, item.BaseURL, item.ref()); err != nil {
		outcome.Reason = err.Error()
		return outcome
	}

	backupDir := filepath.Join(dataDir, "backup")
	_ = os.MkdirAll(backupDir, 0o755)
	backupPath := filepath.Join(backupDir, filepath.Base(config.Path)+"."+time.Now().Format("20060102-150405")+".bak")
	if err := os.WriteFile(backupPath, []byte(config.Text), 0o644); err != nil {
		outcome.Reason = "cannot write backup: " + err.Error()
		return outcome
	}
	if err := os.WriteFile(config.Path, []byte(updated), 0o644); err != nil {
		outcome.Reason = err.Error()
		return outcome
	}
	outcome.Changed = true
	outcome.Backup = backupPath
	if logs != nil {
		*logs = append(*logs, "模型配置    : 已写入 "+config.Path)
		*logs = append(*logs, "模型配置    : 原文件备份 "+backupPath)
	}
	return outcome
}

type writeOutcome struct {
	Changed  bool
	Path     string
	Backup   string
	Previous string
	Reason   string
}

// writeModelChoice updates "model" (and "small_model" when asked) in the user's
// config. The old file is copied to data\backup first and the result is parsed
// again before it replaces the original - a broken config is never left behind.
func writeModelChoice(dataDir string, config userConfig, modelRef, smallRef string, logs *[]string) writeOutcome {
	outcome := writeOutcome{Path: config.Path}
	if config.Text == "" {
		if err := os.MkdirAll(filepath.Dir(config.Path), 0o755); err != nil {
			outcome.Reason = err.Error()
			return outcome
		}
		config.Text = "{\n  \"$schema\": \"https://opencode.ai/config.json\"\n}\n"
	}

	updated := config.Text
	changed := false
	if modelRef != "" && config.Model != modelRef {
		next, ok := setStringKey(updated, "model", modelRef)
		if !ok {
			outcome.Reason = "config has no JSON object"
			return outcome
		}
		updated, changed = next, true
	}
	if smallRef != "" && config.SmallModel != smallRef {
		if next, ok := setStringKey(updated, "small_model", smallRef); ok {
			updated = next
			changed = true
		}
	}
	if !changed {
		return outcome
	}

	cleaned := stripJSONC(updated)
	var verify map[string]any
	if err := json.Unmarshal([]byte(cleaned), &verify); err != nil {
		outcome.Reason = "edited config would not parse: " + err.Error()
		return outcome
	}

	backupDir := filepath.Join(dataDir, "backup")
	_ = os.MkdirAll(backupDir, 0o755)
	backupPath := filepath.Join(backupDir, filepath.Base(config.Path)+"."+time.Now().Format("20060102-150405")+".bak")
	if err := os.WriteFile(backupPath, []byte(config.Text), 0o644); err != nil {
		outcome.Reason = "cannot write backup: " + err.Error()
		return outcome
	}
	if err := os.WriteFile(config.Path, []byte(updated), 0o644); err != nil {
		outcome.Reason = err.Error()
		return outcome
	}
	outcome.Changed = true
	outcome.Backup = backupPath
	outcome.Previous = config.Model
	if logs != nil {
		*logs = append(*logs, "模型配置    : 已写入 "+config.Path)
		*logs = append(*logs, "模型配置    : 原文件备份 "+backupPath)
	}
	return outcome
}

// ---------------------------------------------------------------------------
// the whole check
// ---------------------------------------------------------------------------

type modelReport struct {
	CheckedAt     string            `json:"checkedAt"`
	Action        string            `json:"action"` // kept, switched, pinned, nothing-to-do, disabled, no-candidates
	CurrentModel  string            `json:"currentModel,omitempty"`
	CurrentStatus string            `json:"currentStatus,omitempty"`
	ChosenModel   string            `json:"chosenModel,omitempty"`
	SmallModel    string            `json:"smallModel,omitempty"`
	Probed        []probeResult     `json:"probed,omitempty"`
	Available     []string          `json:"availableFreeModels,omitempty"`
	Notes         []string          `json:"notes,omitempty"`
	ToolChecks    []toolCheckResult `json:"toolChecks,omitempty"`
	KeylessOnly   bool              `json:"keylessOnly"`
	ConfigBackup  string            `json:"configBackup,omitempty"`
	ElapsedMS     int64             `json:"elapsedMs"`
}

type toolCheckResult struct {
	Ref    string `json:"ref"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

func reportPath(dataDir string) string { return filepath.Join(dataDir, "models-report.json") }

// refreshModels is called on every launch. It never blocks the app for longer
// than the configured budget and always finishes with a report on the stick.
func refreshModels(root, dataDir, logPath string, logs *[]string, ui *progressUI, proxyURL string) {
	started := time.Now()
	if disabled := strings.TrimSpace(os.Getenv("OPENCODE_PORTABLE_SKIP_MODEL_CHECK")); disabled == "1" || strings.EqualFold(disabled, "true") {
		*logs = append(*logs, "免费模型    : 已通过 OPENCODE_PORTABLE_SKIP_MODEL_CHECK 关闭检测")
		return
	}

	policy := loadPolicy(root, logs)
	if !policy.Enabled {
		*logs = append(*logs, "免费模型    : model-policy.json 中 enabled=false，跳过检测")
		writeReport(dataDir, modelReport{Action: "disabled", CheckedAt: time.Now().Format(time.RFC3339)})
		return
	}

	client := probeClient(proxyURL, time.Duration(policy.ProbeTimeoutMs)*time.Millisecond)
	catalogClient := probeClient(proxyURL, 45*time.Second)

	var items []candidate
	creds := credentials{authKeys: map[string]string{}, envKeys: map[string]string{}}
	if policy.KeylessOnly {
		ui.SetStatus("正在查找免 key 的免费模型 ...", -1)
		ui.SetHint("只在完全不需要 API key 的免费模型里挑选：\r\n" +
			"内置公开免 key 接口 + 本机 Ollama / LM Studio 等。")
		items = discoverKeylessCandidates(policy, logs)
	} else {
		ui.SetStatus("正在刷新免费模型目录 ...", -1)
		list, err := loadCatalog(dataDir, policy, catalogClient, logs)
		if err != nil {
			*logs = append(*logs, fmt.Sprintf("免费模型    : 无法获取模型目录（%v），跳过检测", err))
			writeReport(dataDir, modelReport{Action: "nothing-to-do", Notes: []string{err.Error()},
				CheckedAt: time.Now().Format(time.RFC3339)})
			return
		}
		creds = loadCredentials(dataDir, list, logs)
		items = buildCandidates(list, creds, policy, logs)
	}
	config := loadUserConfig(dataDir)

	report := modelReport{
		CheckedAt:    time.Now().Format(time.RFC3339),
		CurrentModel: config.Model,
		KeylessOnly:  policy.KeylessOnly,
	}
	health := map[string]bool{}

	// 1. pinned model wins when it answers
	if ref := strings.TrimSpace(policy.PinnedModel); ref != "" {
		if item, ok := candidateByRef(items, ref); ok {
			result := probeOne(client, item, ui, 1, 1)
			report.Probed = append(report.Probed, result)
			report.CurrentStatus = result.Status
			if result.usable() {
				outcome := writeModelChoice(dataDir, config, item.ref(), "", logs)
				report.Action = "pinned"
				report.ChosenModel = item.ref()
				report.ConfigBackup = outcome.Backup
				finishReport(dataDir, &report, logs, ui, started)
				return
			}
			*logs = append(*logs, fmt.Sprintf("免费模型    : 指定的 %s 不可用（%s），改为自动挑选", ref, result.Status))
		} else {
			*logs = append(*logs, fmt.Sprintf("免费模型    : 指定的 %s 不在候选列表中（缺少凭据或已下架）", ref))
		}
	}

	// 2. check what the user is currently using
	var currentResult *probeResult
	if config.Model != "" {
		if item, ok := candidateByRef(items, config.Model); ok {
			result := probeOne(client, item, ui, 1, 1)
			currentResult = &result
			report.Probed = append(report.Probed, result)
			report.CurrentStatus = result.Status
			health[result.Ref] = result.usable()
		} else if item, ok := candidateFromConfig(config.Model, config, creds); ok {
			result := probeModel(context.Background(), client, item)
			currentResult = &result
			report.Probed = append(report.Probed, result)
			report.CurrentStatus = result.Status
			health[result.Ref] = result.usable()
		} else {
			report.CurrentStatus = "unknown"
			report.Notes = append(report.Notes, "当前模型 "+config.Model+" 没有可用的凭据，无法检测")
		}
	}

	if currentResult != nil && currentResult.usable() {
		// healthy: keep it, but still refresh the free-model list in the
		// background so data\models-report.json stays useful
		report.Action = "kept"
		report.ChosenModel = config.Model
		probeAlternatives(client, items, config, policy, &report, health, ui)
		finishReport(dataDir, &report, logs, ui, started)
		return
	}

	// 3. the current model is missing or broken -> find a replacement
	if len(items) == 0 {
		report.Action = "no-candidates"
		if policy.KeylessOnly {
			report.Notes = append(report.Notes,
				"没有找到免 key 的免费模型来源：内置公开免 key 接口当前不可用，本机也没有运行 Ollama / LM Studio。可以："+
					"1) 稍后重试；2) 先启动本机 Ollama 再启动本程序；"+
					"3) 在 data\\model-policy.json 的 keylessProviders 里追加自己的免 key 接口；"+
					"4) 把 data\\model-policy.json 里的 keylessOnly 改为 false，改用需要 key 的免费来源（OpenCode Zen / 智谱 等）。")
		} else {
			report.Notes = append(report.Notes,
				"没有检测到可用的免费模型来源。可运行 opencode 的 /connect 或在 data\\data\\opencode\\auth.json 里配置一个 key，"+
					"也可以在 data\\model-policy.json 的 customProviders 里接入中转站。")
		}
		finishReport(dataDir, &report, logs, ui, started)
		return
	}

	pending := make([]candidate, 0, policy.MaxProbeCandidates)
	for _, item := range items {
		if _, seen := health[item.ref()]; seen {
			continue
		}
		pending = append(pending, item)
		if len(pending) >= policy.MaxProbeCandidates {
			break
		}
	}

	budget := time.Duration(policy.TotalBudgetMs) * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	ui.SetStatus(fmt.Sprintf("正在检测免费模型（共 %d 个候选）...", len(pending)), -1)
	results := probeMany(ctx, client, pending, func(done, total int) {
		ui.SetStatus(fmt.Sprintf("正在检测免费模型 %d/%d ...", done, total), -1)
	})
	report.Probed = append(report.Probed, results...)
	for _, result := range results {
		health[result.Ref] = result.usable()
		if result.usable() {
			report.Available = append(report.Available, result.Ref)
		}
	}

	var reachable []candidate
	for _, item := range items { // items are already ranked best-first
		if health[item.ref()] {
			reachable = append(reachable, item)
		}
	}

	if len(reachable) == 0 {
		report.Action = "nothing-to-do"
		if policy.KeylessOnly {
			report.Notes = append(report.Notes, "免 key 的免费模型本次都没能通过检测（公共接口可能被限流，"+
				"或本机没有运行 Ollama / LM Studio）。稍后重试或把 model-policy.json 的 keylessOnly 改为 false 使用需要 key 的来源。")
		} else {
			report.Notes = append(report.Notes, "所有候选都没能通过检测（可能被限流或需要重新登录）")
		}
		finishReport(dataDir, &report, logs, ui, started)
		return
	}

	// "好用"的最后一道关：模型必须真的会调用工具，否则 opencode 的
	// 读写文件、执行命令等能力都用不了。
	chosen := reachable[0]
	toolOK := false
	attempts := policy.ToolCheckCandidates
	if attempts <= 0 {
		attempts = 1
	}
	for index, item := range reachable {
		if index >= attempts {
			break
		}
		ui.SetStatus(fmt.Sprintf("正在验证工具调用能力（%s）...", item.ref()), -1)
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		ok, detail := verifyToolCalling(ctx, client, item)
		cancel()
		report.ToolChecks = append(report.ToolChecks, toolCheckResult{Ref: item.ref(), OK: ok, Detail: detail})
		if ok {
			chosen, toolOK = item, true
			break
		}
	}
	if !toolOK {
		report.Notes = append(report.Notes, "候选模型都没有通过工具调用验证（可能只支持纯对话），"+
			"opencode 的编辑文件/执行命令能力可能不可用。")
	}

	report.ChosenModel = chosen.ref()
	smallRef := pickSmallModel(items, health, chosen.ref())

	if !policy.AutoSwitch {
		report.Action = "detected-only"
		report.Notes = append(report.Notes, "检测到可用模型 "+chosen.ref()+"，但 autoSwitch=false，未修改配置")
		finishReport(dataDir, &report, logs, ui, started)
		return
	}

	var outcome writeOutcome
	if chosen.Keyless {
		outcome = writeKeylessChoice(dataDir, config, chosen, items, smallRef, logs)
	} else {
		outcome = writeModelChoice(dataDir, config, chosen.ref(), smallRef, logs)
	}
	if !outcome.Changed {
		report.Action = "nothing-to-do"
		report.Notes = append(report.Notes, "配置未改动："+outcome.Reason)
		finishReport(dataDir, &report, logs, ui, started)
		return
	}
	report.Action = "switched"
	report.SmallModel = smallRef
	report.ConfigBackup = outcome.Backup
	finishReport(dataDir, &report, logs, ui, started)
}

func candidateByRef(items []candidate, ref string) (candidate, bool) {
	ref = strings.TrimSpace(ref)
	for _, item := range items {
		if item.ref() == ref {
			return item, true
		}
	}
	return candidate{}, false
}

func probeOne(client *http.Client, item candidate, ui *progressUI, done, total int) probeResult {
	ui.SetStatus(fmt.Sprintf("正在检测当前模型（%s）...", item.ref()), -1)
	// keep startup snappy: never wait longer than 6s for the health check
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	return probeModel(ctx, client, item)
}

// probeAlternatives checks a couple of other candidates even when the current
// model is healthy, so the report knows what the fallback would be.
func probeAlternatives(client *http.Client, items []candidate, config userConfig, policy modelPolicy,
	report *modelReport, health map[string]bool, ui *progressUI) {

	limit := 3
	if len(items) < limit {
		limit = len(items)
	}
	pending := make([]candidate, 0, limit)
	for _, item := range items {
		if item.ref() == config.Model {
			continue
		}
		pending = append(pending, item)
		if len(pending) >= limit {
			break
		}
	}
	if len(pending) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := probeMany(ctx, client, pending, nil)
	report.Probed = append(report.Probed, results...)
	for _, result := range results {
		if result.usable() {
			report.Available = append(report.Available, result.Ref)
		}
	}
	ui.SetHint("当前模型可用，已保留；同时刷新了免费模型备选清单。")
}

// pickSmallModel suggests a cheap model for titles and other light work. It is
// only used when a *different* small model is available.
func pickSmallModel(items []candidate, health map[string]bool, excludeRef string) string {
	for _, item := range items {
		if item.ref() == excludeRef {
			continue
		}
		if health[item.ref()] && isSmallModel(item.Model) && item.Context >= 32768 {
			return item.ref()
		}
	}
	return ""
}

func finishReport(dataDir string, report *modelReport, logs *[]string, ui *progressUI, started time.Time) {
	report.ElapsedMS = time.Since(started).Milliseconds()
	switch report.Action {
	case "switched":
		*logs = append(*logs, fmt.Sprintf("免费模型    : 当前模型 %s 不可用，已切换到 %s", report.CurrentModel, report.ChosenModel))
		ui.SetHint("免费模型已自动切换为 " + report.ChosenModel + "\r\n原模型：" + report.CurrentModel)
	case "kept":
		*logs = append(*logs, fmt.Sprintf("免费模型    : 当前模型 %s 可用，保持不动", report.CurrentModel))
	case "pinned":
		*logs = append(*logs, fmt.Sprintf("免费模型    : 已按 model-policy.json 固定使用 %s", report.ChosenModel))
	case "no-candidates":
		*logs = append(*logs, "免费模型    : 没有可用的免费模型来源（未配置任何凭据）")
	case "nothing-to-do":
		*logs = append(*logs, "免费模型    : 没有可用的免费模型，配置保持不变")
	case "detected-only":
		*logs = append(*logs, "免费模型    : 检测完成（autoSwitch=false，未改配置）")
	}
	if len(report.Available) > 0 {
		*logs = append(*logs, "免费模型    : 本次通过检测的模型有 "+strings.Join(report.Available, ", "))
	}
	*logs = append(*logs, fmt.Sprintf("免费模型    : 检测用时 %d ms，明细见 data\\models-report.json", report.ElapsedMS))
	writeReport(dataDir, *report)
}

func writeReport(dataDir string, report modelReport) {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(dataDir, 0o755)
	_ = os.WriteFile(reportPath(dataDir), raw, 0o644)
}
