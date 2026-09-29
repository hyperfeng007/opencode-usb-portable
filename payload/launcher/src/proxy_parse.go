package main

import "strings"

// ---------------------------------------------------------------------------
// proxy value parsing (pure helpers, unit-tested on any platform)
// ---------------------------------------------------------------------------
func normalizeProxyURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		return value
	}
	return "http://" + value
}

// parseProxyValue understands the shapes Windows uses:
//
//	"127.0.0.1:7897"
//	"http=127.0.0.1:7897;https=127.0.0.1:7897"
//	"socks=127.0.0.1:1080"
func parseProxyValue(value string) map[string]string {
	out := map[string]string{}
	value = strings.TrimSpace(value)
	if value == "" {
		return out
	}
	if !strings.Contains(value, "=") {
		out["all"] = normalizeProxyURL(value)
		return out
	}
	for _, part := range strings.Split(value, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		val := strings.TrimSpace(kv[1])
		if val == "" {
			continue
		}
		switch key {
		case "http", "https":
			out[key] = normalizeProxyURL(val)
		case "socks", "socks5":
			out["socks"] = "socks5://" + strings.TrimPrefix(strings.TrimPrefix(val, "socks5://"), "socks://")
		case "socks4":
			out["socks"] = "socks4://" + strings.TrimPrefix(val, "socks4://")
		default:
			if _, exists := out["all"]; !exists {
				out["all"] = normalizeProxyURL(val)
			}
		}
	}
	return out
}

func pick(list ...string) string {
	for _, item := range list {
		if item != "" {
			return item
		}
	}
	return ""
}

func mergeBypass(existing string, fromSystem string) string {
	parts := []string{}
	seen := map[string]bool{}
	add := func(item string) {
		item = strings.TrimSpace(item)
		if item == "" || seen[strings.ToLower(item)] {
			return
		}
		seen[strings.ToLower(item)] = true
		parts = append(parts, item)
	}
	for _, item := range strings.Split(existing, ",") {
		add(item)
	}
	for _, item := range strings.Split(strings.ReplaceAll(fromSystem, ";", ","), ",") {
		add(item)
	}
	for _, item := range []string{"localhost", "127.0.0.1", "::1"} {
		add(item)
	}
	return strings.Join(parts, ",")
}
