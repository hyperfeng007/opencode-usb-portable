package main

// Proxy helpers shared by all platforms. The Windows registry lookup lives in
// proxy_windows.go; this file keeps the parsing logic testable everywhere.

import (
	"os"
	"strings"
)

// parseProxyString turns the formats Windows stores in the registry into a
// single proxy URL. Our traffic to GitHub is HTTPS, so an "https=" entry wins,
// then "http=", then "socks=", then a bare "host:port".
//
//	127.0.0.1:7897
//	http=127.0.0.1:7897;https=127.0.0.1:7897
//	socks=127.0.0.1:1080
func parseProxyString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "=") {
		return normalizeProxy(value)
	}
	entries := map[string]string{}
	for _, part := range strings.Split(value, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.SplitN(part, "=", 2)
		if len(fields) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(fields[0]))
		entry := strings.TrimSpace(fields[1])
		if entry == "" {
			continue
		}
		switch key {
		case "https", "http":
			entries[key] = normalizeProxy(entry)
		case "socks", "socks5":
			entries["socks"] = "socks5://" + strings.TrimPrefix(strings.TrimPrefix(entry, "socks5://"), "socks://")
		case "socks4":
			entries["socks"] = "socks4://" + strings.TrimPrefix(entry, "socks4://")
		}
	}
	for _, key := range []string{"https", "http", "socks"} {
		if entries[key] != "" {
			return entries[key]
		}
	}
	return ""
}

func normalizeProxy(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		return value
	}
	return "http://" + value
}

// resolveProxy decides which proxy the builder uses for its own downloads:
//  1. --proxy <url>                        (explicit)
//  2. HTTP_PROXY / HTTPS_PROXY             (set by the launcher or the user)
//  3. the Windows system proxy settings    (Clash / v2rayN / sing-box / ...)
func resolveProxy(flagValue string) (string, string) {
	if value := strings.TrimSpace(flagValue); value != "" {
		return normalizeProxy(value), "--proxy 参数"
	}
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return normalizeProxy(value), "环境变量 " + key
		}
	}
	if value := systemProxy(); value != "" {
		return value, "Windows 系统代理设置"
	}
	return "", "未检测到（直连）"
}

// mirrorPrefix lets users behind a slow or blocked connection route the GitHub
// downloads through a mirror, e.g.
//
//	set OPENCODE_PORTABLE_MIRROR=https://ghfast.top/
func mirrorPrefix() string {
	value := strings.TrimSpace(os.Getenv("OPENCODE_PORTABLE_MIRROR"))
	if value == "" {
		return ""
	}
	if !strings.HasSuffix(value, "/") {
		value += "/"
	}
	return value
}

// activeMirror is the mirror currently in use ("" = connect directly to GitHub).
// It is switched by retryWithMirrors when a direct connection fails.
var activeMirror string

// remoteURL rewrites a GitHub URL through the mirror in use.
func remoteURL(original string) string {
	if activeMirror == "" {
		return original
	}
	return activeMirror + original
}

// builtinMirrors are public GitHub download proxies in mainland China. They are
// only used as a fallback when the direct connection to GitHub fails; whatever
// comes back is still checked against the official SHA-256 digest.
var builtinMirrors = []string{
	"https://ghfast.top/",
	"https://gh-proxy.com/",
}

// retryWithMirrors runs attempt directly and, when that fails and no mirror was
// configured explicitly, retries through the built-in mirrors.
func retryWithMirrors(what string, attempt func() error) error {
	if prefix := mirrorPrefix(); prefix != "" { // explicit mirror: single try
		activeMirror = prefix
		return attempt()
	}
	if noMirrorFallback {
		activeMirror = ""
		return attempt()
	}
	activeMirror = ""
	err := attempt()
	if err == nil {
		return nil
	}
	for _, mirror := range builtinMirrors {
		logf("[网络] %s 直连失败，改用公共镜像 %s 重试 ...", what, mirror)
		activeMirror = mirror
		if retryErr := attempt(); retryErr == nil {
			logf("[注意] 本次通过公共镜像下载，已按官方 SHA-256 校验完整性。")
			return nil
		}
	}
	activeMirror = ""
	return err
}

// apiBase allows overriding the GitHub API host (mirrors, enterprise testing).
func apiBase() string {
	if value := strings.TrimSpace(os.Getenv("OPENCODE_PORTABLE_API_BASE")); value != "" {
		return strings.TrimRight(value, "/")
	}
	return "https://api.github.com"
}
