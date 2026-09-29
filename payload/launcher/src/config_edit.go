package main

// JSONC-aware editing of opencode.json / opencode.jsonc.
//
// Keyless providers are not part of models.dev, so the launcher defines them
// itself:
//
//	"provider": {
//	  "pollinations": {
//	    "npm": "@ai-sdk/openai-compatible",
//	    "name": "Pollinations（免 key 公开接口）",
//	    "options": { "baseURL": "https://text.pollinations.ai/openai", "apiKey": "opencode-portable" },
//	    "models": { "openai-fast": { "name": "...", "tool_call": true } }
//	  }
//	}
//
// Everything is done by scanning the original text, so comments, key order and
// the user's own providers survive the edit untouched.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// scanning helpers (positions always refer to the original text)
// ---------------------------------------------------------------------------

func skipJSONCSpace(text string, index int) int {
	for index < len(text) {
		switch text[index] {
		case ' ', '\t', '\n', '\r':
			index++
			continue
		case '/':
			if index+1 < len(text) && text[index+1] == '/' {
				for index < len(text) && text[index] != '\n' {
					index++
				}
				continue
			}
			if index+1 < len(text) && text[index+1] == '*' {
				end := strings.Index(text[index+2:], "*/")
				if end < 0 {
					return len(text)
				}
				index += end + 4
				continue
			}
		}
		break
	}
	return index
}

func scanStringEnd(text string, index int) int {
	if index >= len(text) || text[index] != '"' {
		return -1
	}
	index++
	for index < len(text) {
		switch text[index] {
		case '\\':
			index += 2
		case '"':
			return index + 1
		default:
			index++
		}
	}
	return -1
}

// scanValueEnd returns the position just after the JSON value at index.
func scanValueEnd(text string, index int) int {
	index = skipJSONCSpace(text, index)
	if index >= len(text) {
		return -1
	}
	switch text[index] {
	case '"':
		return scanStringEnd(text, index)
	case '{', '[':
		depth := 0
		for index < len(text) {
			switch text[index] {
			case '"':
				index = scanStringEnd(text, index)
				if index < 0 {
					return -1
				}
				continue
			case '/':
				if index+1 < len(text) && (text[index+1] == '/' || text[index+1] == '*') {
					index = skipJSONCSpace(text, index)
					continue
				}
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return index + 1
				}
			}
			index++
		}
		return -1
	default:
		for index < len(text) {
			switch text[index] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return index
			}
			index++
		}
		return index
	}
}

type jsonMember struct {
	key        string
	keyStart   int
	valueStart int
	valueEnd   int
}

// objectMembers lists the direct members of the object that starts at open.
func objectMembers(text string, open int) []jsonMember {
	var members []jsonMember
	if open < 0 || open >= len(text) || text[open] != '{' {
		return members
	}
	index := open + 1
	for index < len(text) {
		index = skipJSONCSpace(text, index)
		if index >= len(text) || text[index] != '"' {
			break
		}
		keyEnd := scanStringEnd(text, index)
		if keyEnd < 0 {
			break
		}
		key := strings.Trim(text[index:keyEnd], `"`)
		cursor := skipJSONCSpace(text, keyEnd)
		if cursor >= len(text) || text[cursor] != ':' {
			break
		}
		valueStart := skipJSONCSpace(text, cursor+1)
		valueEnd := scanValueEnd(text, valueStart)
		if valueEnd < 0 {
			break
		}
		members = append(members, jsonMember{key: key, keyStart: index, valueStart: valueStart, valueEnd: valueEnd})
		index = skipJSONCSpace(text, valueEnd)
		if index < len(text) && text[index] == ',' {
			index++
			continue
		}
		break
	}
	return members
}

func findMember(text string, objectOpen int, key string) (jsonMember, bool) {
	for _, member := range objectMembers(text, objectOpen) {
		if member.key == key {
			return member, true
		}
	}
	return jsonMember{}, false
}

func rootObjectOpen(text string) int { return strings.Index(text, "{") }

// closingBrace finds the '}' that closes the object starting at open.
func closingBrace(text string, open int) int {
	end := scanValueEnd(text, open)
	if end <= open {
		return -1
	}
	for index := end - 1; index > open; index-- {
		if text[index] == '}' {
			return index
		}
	}
	return -1
}

// lineIndent returns the leading whitespace of the line containing position.
func lineIndent(text string, position int) string {
	if position > len(text) {
		position = len(text)
	}
	start := strings.LastIndex(text[:position], "\n") + 1
	indent := strings.Builder{}
	for index := start; index < position; index++ {
		if text[index] == ' ' || text[index] == '\t' {
			indent.WriteByte(text[index])
			continue
		}
		break
	}
	return indent.String()
}

// reindent prefixes every non-empty line of block with indent.
func reindent(block, indent string) string {
	if indent == "" {
		return block
	}
	lines := strings.Split(block, "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[index] = ""
			continue
		}
		lines[index] = indent + line
	}
	return strings.Join(lines, "\n")
}

func lastNonSpace(text string, from, to int) int {
	for index := to - 1; index > from; index-- {
		switch text[index] {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return index
	}
	return -1
}

// ---------------------------------------------------------------------------
// rendering
// ---------------------------------------------------------------------------

// keylessPlaceholderKey is what the AI SDK stores as api key for keyless
// endpoints; those servers ignore it.
const keylessPlaceholderKey = "opencode-portable"

// providerBody renders the provider definition with relative indentation.
func providerBody(item candidate, sourceModels []candidate) string {
	var body strings.Builder
	body.WriteString(`"npm": "@ai-sdk/openai-compatible",` + "\n")
	fmt.Fprintf(&body, `"name": %s,`+"\n", jsonString(item.SourceName))
	body.WriteString(`"options": {` + "\n")
	fmt.Fprintf(&body, `  "baseURL": %s,`+"\n", jsonString(item.BaseURL))
	fmt.Fprintf(&body, `  "apiKey": %s`+"\n", jsonString(keylessPlaceholderKey))
	body.WriteString("},\n")
	body.WriteString(`"models": {` + "\n")
	for index, model := range sourceModels {
		fmt.Fprintf(&body, "  %s: {\n", jsonString(model.Model))
		fmt.Fprintf(&body, "    \"name\": %s", jsonString(orDefault(model.Name, model.Model)))
		if model.ToolCall {
			body.WriteString(",\n    \"tool_call\": true")
		}
		if model.Context > 0 {
			fmt.Fprintf(&body, ",\n    \"limit\": { \"context\": %d, \"output\": %d }", model.Context, minInt(model.Context/4, 32768))
		}
		if index == len(sourceModels)-1 {
			body.WriteString("\n  }\n")
		} else {
			body.WriteString("\n  },\n")
		}
	}
	body.WriteString("}")
	return body.String()
}

// renderEntry renders `"<key>": { ... }` with relative indentation.
func renderEntry(key, body string) string {
	return jsonString(key) + ": {\n" + reindent(body, "  ") + "\n}"
}

// candidateSourceModels collects every model discovered for the same provider,
// so the user can switch models inside opencode without editing files.
func candidateSourceModels(item candidate, all []candidate) []candidate {
	var out []candidate
	seen := map[string]bool{}
	for _, other := range all {
		if other.Provider != item.Provider || seen[other.Model] {
			continue
		}
		seen[other.Model] = true
		out = append(out, other)
	}
	if len(out) == 0 {
		out = append(out, item)
	}
	return out
}

func jsonString(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(raw)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// merge
// ---------------------------------------------------------------------------

// mergeMemberRaw replaces the member with the given key inside the object at
// open, or inserts a new one before the object's closing brace.
func mergeMemberRaw(text string, open int, key, entry string) (string, bool) {
	if member, found := findMember(text, open, key); found {
		indent := lineIndent(text, member.keyStart)
		if strings.TrimSpace(indent) == "" && indent == "" {
			indent = lineIndent(text, open) + "  "
		}
		replacement := reindent(entry, indent)
		return text[:member.keyStart] + replacement + text[member.valueEnd:], true
	}

	closeIndex := closingBrace(text, open)
	if closeIndex < 0 {
		return text, false
	}
	baseIndent := lineIndent(text, open) + "  "

	if lastNonSpace(text, open, closeIndex) < 0 { // empty object
		addition := "\n" + reindent(entry, baseIndent) + "\n" + lineIndent(text, open)
		return text[:open+1] + addition + text[closeIndex:], true
	}

	last := lastNonSpace(text, open, closeIndex)
	indent := lineIndent(text, last)
	if strings.TrimSpace(indent) == "" {
		indent = baseIndent
	}
	if text[last] == ',' {
		addition := "\n" + reindent(entry, indent) + "\n" + lineIndent(text, closeIndex)
		return text[:closeIndex] + addition + text[closeIndex:], true
	}
	addition := ",\n" + reindent(entry, indent) + "\n" + lineIndent(text, closeIndex)
	return text[:last+1] + addition + text[closeIndex:], true
}

// upsertProviderBlock inserts or updates one provider entry in the config text.
func upsertProviderBlock(text, providerID string, sourceModels []candidate, item candidate) (string, bool) {
	open := rootObjectOpen(text)
	if open < 0 {
		return text, false
	}
	entry := renderEntry(providerID, providerBody(item, sourceModels))

	if current, ok := findMember(text, open, "provider"); ok && current.valueStart < len(text) && text[current.valueStart] == '{' {
		if existing, ok := findMember(text, current.valueStart, providerID); ok {
			indent := lineIndent(text, existing.keyStart)
			replacement := reindent(entry, indent)
			return text[:existing.keyStart] + replacement + text[existing.valueEnd:], true
		}
		return mergeMemberRaw(text, current.valueStart, providerID, entry)
	}

	// -> {"provider": { "<id>": { ... } }}
	providerMember := jsonString("provider") + ": {\n" + reindent(entry, "  ") + "\n}"
	return mergeMemberRaw(text, open, "provider", providerMember)
}

// verifyConfig checks the edited text really contains the intended provider.
func verifyConfig(text, providerID, baseURL string, wantModel string) error {
	var parsed struct {
		Provider map[string]struct {
			Options struct {
				BaseURL string `json:"baseURL"`
			} `json:"options"`
			Models map[string]json.RawMessage `json:"models"`
		} `json:"provider"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(stripJSONC(text)), &parsed); err != nil {
		return fmt.Errorf("配置无法解析：%w", err)
	}
	block, ok := parsed.Provider[providerID]
	if !ok {
		return fmt.Errorf("没有写入 provider %s", providerID)
	}
	if baseURL != "" && block.Options.BaseURL != baseURL {
		return fmt.Errorf("provider %s 的 baseURL 不正确（%s）", providerID, block.Options.BaseURL)
	}
	if len(block.Models) == 0 {
		return fmt.Errorf("provider %s 没有模型列表", providerID)
	}
	if wantModel != "" && parsed.Model != wantModel {
		return fmt.Errorf("model 未写入（当前为 %s）", parsed.Model)
	}
	return nil
}
