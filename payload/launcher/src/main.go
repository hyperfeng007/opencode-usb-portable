//go:build windows

// opencode-portable launcher
//
// A small native Windows launcher (GUI subsystem: no console window) that
//  1. finds the portable root next to itself (or via OPENCODE_PORTABLE_ROOT),
//  2. points every writable location at the USB stick (portable mode),
//  3. reads the machine's system proxy configuration and passes it on as
//     HTTP_PROXY / HTTPS_PROXY / NO_PROXY,
//  4. starts OpenCode.exe and exits immediately.
//
// Build (cross compile from any OS):
//
//	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-H windowsgui -s -w"
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (

	// win32 CreateProcess flag: give the child its own console window, so the
	// first-time build is visible even though this launcher is a GUI program.
	createNewConsole = 0x00000010

	markerName    = "OpenCode-Portable.json"
	markerAltName = ".opencode-portable"
	exeRelPath    = "OpenCode\\OpenCode.exe"
	logMaxBytes   = 80 * 1024
)

// ---------------------------------------------------------------------------
// Win32 plumbing
// ---------------------------------------------------------------------------
var (
	wininet        = syscall.NewLazyDLL("wininet.dll")
	winhttp        = syscall.NewLazyDLL("winhttp.dll")
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	user32         = syscall.NewLazyDLL("user32.dll")
	procGlobalFree = kernel32.NewProc("GlobalFree")
	procMessageBox = user32.NewProc("MessageBoxW")

	procInternetQueryOption     = wininet.NewProc("InternetQueryOptionW")
	procWinHttpGetIEProxyConfig = winhttp.NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	procWinHttpGetProxyForURL   = winhttp.NewProc("WinHttpGetProxyForUrl")
	procWinHttpOpen             = winhttp.NewProc("WinHttpOpen")
	procWinHttpCloseHandle      = winhttp.NewProc("WinHttpCloseHandle")
)

const (
	internetOptionPerConnOptionList = 75

	winhttpAutoProxyAutoDetect = 0x00000001
	winhttpAutoProxyConfigURL  = 0x00000002
	winhttpAutoDetectTypeDhcp  = 0x00000001
	winhttpAutoDetectTypeDnsA  = 0x00000002
)

type winhttpCurrentUserIEProxyConfig struct {
	AutoDetect    int32
	_             uint32
	AutoConfigURL *uint16
	Proxy         *uint16
	ProxyBypass   *uint16
}

type winhttpAutoProxyOptions struct {
	Flags                 uint32
	AutoDetectFlags       uint32
	AutoConfigURL         *uint16
	Reserved              unsafe.Pointer
	Reserved2             uint32
	AutoLogonIfChallenged int32
}

type winhttpProxyInfo struct {
	AccessType  uint32
	_           uint32
	Proxy       *uint16
	ProxyBypass *uint16
}

type internetPerConnOption struct {
	Option uint32
	_      uint32
	Value  *uint16
	_      uint32
}

type internetPerConnOptionList struct {
	Size        uint32
	_           uint32
	Connection  *uint16
	OptionCount uint32
	OptionError uint32
	Options     *internetPerConnOption
}

func utf16Ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

func goString(p *uint16) string {
	if p == nil {
		return ""
	}
	return syscall.UTF16ToString(unsafe.Slice(p, 1<<15))
}

func globalFree(p *uint16) {
	if p != nil {
		procGlobalFree.Call(uintptr(unsafe.Pointer(p)))
	}
}

func messageBox(title, text string, flags uintptr) {
	procMessageBox.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		uintptr(unsafe.Pointer(utf16Ptr(title))),
		flags)
}

// queryWinINET reads the current user's "ProxyServer" / "ProxyBypass" values.
// This reflects exactly what the Windows Settings app and most proxy tools
// (Clash, v2rayN, Surfboard, Fiddler, ...) write.
func queryWinINET() (proxy string, bypass string) {
	words := make([]uint64, 1024) // 8 KB, 8-byte aligned
	size := uint32(len(words) * 8)
	ret, _, _ := procInternetQueryOption.Call(
		0,
		internetOptionPerConnOptionList,
		uintptr(unsafe.Pointer(&words[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret == 0 {
		return "", ""
	}
	list := (*internetPerConnOptionList)(unsafe.Pointer(&words[0]))
	if list.Options == nil || list.OptionCount == 0 {
		return "", ""
	}
	opts := unsafe.Slice(list.Options, int(list.OptionCount))
	return strings.TrimSpace(goString(opts[0].Value)), ""
}

type ieProxyConfig struct {
	AutoDetect    bool
	AutoConfigURL string
	Proxy         string
	ProxyBypass   string
}

func queryIEProxyConfig() ieProxyConfig {
	var cfg winhttpCurrentUserIEProxyConfig
	ok, _, _ := procWinHttpGetIEProxyConfig.Call(uintptr(unsafe.Pointer(&cfg)))
	if ok == 0 {
		return ieProxyConfig{}
	}
	out := ieProxyConfig{AutoDetect: cfg.AutoDetect != 0}
	out.AutoConfigURL = goString(cfg.AutoConfigURL)
	out.Proxy = strings.TrimSpace(goString(cfg.Proxy))
	out.ProxyBypass = strings.TrimSpace(goString(cfg.ProxyBypass))
	globalFree(cfg.AutoConfigURL)
	globalFree(cfg.Proxy)
	globalFree(cfg.ProxyBypass)
	return out
}

// resolveProxyForURL either evaluates a PAC script or performs WPAD
// auto-detection for one URL ("https://opencode.ai") and returns the proxy
// list Windows would use.
func resolveProxyForURL(targetURL string, pacURL string, autoDetect bool) string {
	session, _, _ := procWinHttpOpen.Call(
		uintptr(unsafe.Pointer(utf16Ptr("opencode-portable-launcher/1.1"))),
		0, 0, 0, 0,
	)
	if session == 0 {
		return ""
	}
	defer procWinHttpCloseHandle.Call(session)

	options := winhttpAutoProxyOptions{}
	if pacURL != "" {
		options.Flags = winhttpAutoProxyConfigURL
		options.AutoConfigURL = utf16Ptr(pacURL)
	} else if autoDetect {
		options.Flags = winhttpAutoProxyAutoDetect
		options.AutoDetectFlags = winhttpAutoDetectTypeDhcp | winhttpAutoDetectTypeDnsA
	} else {
		return ""
	}

	var info winhttpProxyInfo
	ok, _, _ := procWinHttpGetProxyForURL.Call(
		session,
		uintptr(unsafe.Pointer(utf16Ptr(targetURL))),
		uintptr(unsafe.Pointer(&options)),
		uintptr(unsafe.Pointer(&info)),
	)
	if ok == 0 {
		return ""
	}
	proxy := strings.TrimSpace(goString(info.Proxy))
	globalFree(info.Proxy)
	globalFree(info.ProxyBypass)
	return proxy
}

// ---------------------------------------------------------------------------
// logging (kept on the stick, never on the host computer)
// ---------------------------------------------------------------------------
var loggedLines int

func appendLog(path string, lines []string) {
	if path == "" {
		return
	}
	if len(lines) <= loggedLines {
		return // everything in this buffer was already written
	}
	fresh := lines[loggedLines:]
	loggedLines = len(lines)
	if info, err := os.Stat(path); err == nil && info.Size() > logMaxBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	stamp := time.Now().Format("2006-01-02 15:04:05")
	_, _ = f.WriteString(fmt.Sprintf("==== %s ====\r\n%s\r\n", stamp, strings.Join(fresh, "\r\n")))
}

// ---------------------------------------------------------------------------
// portable root discovery
// ---------------------------------------------------------------------------
func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func looksLikeRoot(dir string) bool {
	if dir == "" {
		return false
	}
	return exists(filepath.Join(dir, markerName)) || exists(filepath.Join(dir, markerAltName))
}

func findRoot() (string, string) {
	if env := os.Getenv("OPENCODE_PORTABLE_ROOT"); env != "" && looksLikeRoot(env) {
		return env, "OPENCODE_PORTABLE_ROOT"
	}
	exe, err := os.Executable()
	if err != nil {
		return "", ""
	}
	start := filepath.Dir(exe)
	dir := start
	for i := 0; i < 6; i++ {
		if looksLikeRoot(dir) {
			return dir, "portable marker found above " + start
		}
		parent := filepath.Dir(dir)
		if parent == dir || parent == "" {
			break
		}
		dir = parent
	}
	return start, "launcher directory (no portable marker found yet)"
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------
func main() {
	// Hidden self-check used by the test suite: OPENCODE_PORTABLE_PROBE_WINDOW
	// makes the launcher wait for a window and report the result, then exit.
	if probe := strings.TrimSpace(os.Getenv("OPENCODE_PORTABLE_PROBE_WINDOW")); probe != "" {
		pid, _ := strconv.Atoi(probe)
		ok := waitForAppWindow(pid, 30*time.Second)
		target := filepath.Join(os.TempDir(), "probe-window.txt")
		_ = os.WriteFile(target, []byte(fmt.Sprintf("pid=%s detected=%v", probe, ok)), 0o644)
		os.Exit(0)
	}

	root, rootSource := findRoot()
	if root == "" {
		messageBox("OpenCode Portable",
			"Could not determine the portable folder.\r\n\r\nMake sure OpenCode-Portable.json is next to the launcher on the USB drive.",
			0x10)
		os.Exit(2)
	}

	data := filepath.Join(root, "data")
	appExe := filepath.Join(root, exeRelPath)

	for _, dir := range []string{
		"userdata", filepath.Join("userdata", "session"), filepath.Join("userdata", "Crashpad"),
		"config", "data", "cache", "state", "home", "tmp", "roaming", "local",
	} {
		_ = os.MkdirAll(filepath.Join(data, dir), 0o755)
	}

	ui := startProgress("首次使用会自动联网下载并改造 opencode，完成后立即启动。\r\n" +
		"界面无响应时请查看 data\\build.log，请不要在安装过程中拔掉 U 盘。")
	defer ui.Close()

	logPath := filepath.Join(data, "launcher.log")
	logs := []string{
		"launcher    : opencode-portable launcher " + launcherVersion,
		"root        : " + root,
		"root source : " + rootSource,
		"app         : " + appExe,
	}

	env := map[string]string{}
	for _, item := range os.Environ() {
		if idx := strings.Index(item, "="); idx > 0 {
			env[item[:idx]] = item[idx+1:]
		}
	}

	ui.SetStatus("正在读取系统网络代理设置 ...", -1)

	disabled := strings.ToLower(env["OPENCODE_PORTABLE_DISABLE_PROXY"]) == "1" ||
		strings.ToLower(env["OPENCODE_PORTABLE_NO_PROXY"]) == "1"
	switch {
	case disabled:
		logs = append(logs, "proxy       : detection disabled by OPENCODE_PORTABLE_DISABLE_PROXY")
	case env["HTTP_PROXY"] != "" || env["HTTPS_PROXY"] != "":
		logs = append(logs, "proxy source: existing environment variables")
		logs = append(logs, "HTTP_PROXY  : "+env["HTTP_PROXY"])
		logs = append(logs, "HTTPS_PROXY : "+env["HTTPS_PROXY"])
	default:
		cfg := queryIEProxyConfig()

		// WinINET "per connection" options (the classic system proxy)
		if staticProxy, staticBypass := queryWinINET(); staticProxy != "" {
			if cfg.Proxy == "" || (!strings.Contains(cfg.Proxy, "=") && strings.Contains(staticProxy, "=")) {
				cfg.Proxy = staticProxy
			}
			if cfg.ProxyBypass == "" {
				cfg.ProxyBypass = staticBypass
			}
		}
		// registry fallback: ProxyEnable + ProxyServer
		if regServer, regBypass, found := registryProxy(); found && cfg.Proxy == "" {
			cfg.Proxy = regServer
			if cfg.ProxyBypass == "" {
				cfg.ProxyBypass = regBypass
			}
		}

		source := ""
		proxyValue := ""
		if cfg.Proxy != "" {
			source = "Windows system proxy settings"
			proxyValue = cfg.Proxy
		}
		if proxyValue == "" && (cfg.AutoConfigURL != "" || cfg.AutoDetect) {
			if resolved := resolveProxyForURL("https://opencode.ai", cfg.AutoConfigURL, cfg.AutoDetect); resolved != "" {
				if cfg.AutoConfigURL != "" {
					source = "PAC script " + cfg.AutoConfigURL
				} else {
					source = "automatic detection (WPAD)"
				}
				proxyValue = resolved
			}
		}
		entry := parseProxyValue(proxyValue)
		httpProxy := pick(entry["http"], entry["all"], entry["socks"])
		httpsProxy := pick(entry["https"], entry["all"], entry["socks"])
		bypass := mergeBypass(env["NO_PROXY"], cfg.ProxyBypass)

		logs = append(logs, "proxy source: "+source)
		if httpProxy != "" {
			env["HTTP_PROXY"], env["http_proxy"] = httpProxy, httpProxy
			logs = append(logs, "HTTP_PROXY  : "+httpProxy)
		} else {
			logs = append(logs, "HTTP_PROXY  : none detected (direct connection)")
		}
		if httpsProxy != "" {
			env["HTTPS_PROXY"], env["https_proxy"] = httpsProxy, httpsProxy
			logs = append(logs, "HTTPS_PROXY : "+httpsProxy)
		} else if httpProxy != "" {
			env["HTTPS_PROXY"], env["https_proxy"] = httpProxy, httpProxy
			logs = append(logs, "HTTPS_PROXY : "+httpProxy+" (from HTTP proxy)")
		}
		if bypass != "" {
			env["NO_PROXY"], env["no_proxy"] = bypass, bypass
			logs = append(logs, "NO_PROXY    : "+bypass)
		}
		if proxyValue == "" && cfg.AutoDetect {
			logs = append(logs, "note        : Windows auto-detect is enabled but no proxy could be resolved")
		}
	}

	env["OPENCODE_PORTABLE"] = "1"
	env["OPENCODE_PORTABLE_ROOT"] = root
	env["OPENCODE_PORTABLE_USERDATA"] = filepath.Join(data, "userdata")
	env["XDG_CONFIG_HOME"] = filepath.Join(data, "config")
	env["XDG_DATA_HOME"] = filepath.Join(data, "data")
	env["XDG_CACHE_HOME"] = filepath.Join(data, "cache")
	env["XDG_STATE_HOME"] = filepath.Join(data, "state")
	env["HOME"] = filepath.Join(data, "home")
	env["TMP"] = filepath.Join(data, "tmp")
	env["TEMP"] = filepath.Join(data, "tmp")
	env["APPDATA"] = filepath.Join(data, "roaming")
	env["LOCALAPPDATA"] = filepath.Join(data, "local")
	env["OPENCODE_DISABLE_AUTOUPDATE"] = "1"
	if env["OPENCODE_CLIENT"] == "" {
		env["OPENCODE_CLIENT"] = "desktop"
	}
	if nodeExe := filepath.Join(root, "runtime", "node", "node.exe"); exists(nodeExe) {
		env["PATH"] = filepath.Dir(nodeExe) + ";" + env["PATH"]
	}
	if gitExe := filepath.Join(root, "tools", "git", "cmd", "git.exe"); exists(gitExe) {
		env["PATH"] = filepath.Join(root, "tools", "git", "cmd") + ";" +
			filepath.Join(root, "tools", "git", "bin") + ";" + env["PATH"]
	}

	envList := make([]string, 0, len(env))
	for key, value := range env {
		envList = append(envList, key+"="+value)
	}

	// Export the resolved values so child processes (the first-time builder)
	// inherit them: CreateProcess copies the launcher's environment block.
	for key, value := range env {
		_ = os.Setenv(key, value)
	}

	// free-model health check: runs on every start, quick and non-fatal
	probeProxy := env["HTTP_PROXY"]
	if probeProxy == "" {
		probeProxy = env["HTTPS_PROXY"]
	}
	refreshModels(root, data, logPath, &logs, ui, probeProxy)

	launchApp := true
	if !exists(appExe) {
		logs = append(logs, "app not built yet - starting the automatic first-time build")
		appendLog(logPath, logs)
		launchApp = runFirstTimeBuild(root, data, logPath, &logs, ui)
	}
	if !launchApp {
		ui.Close()
		os.Exit(0)
	}
	ui.SetHint("首次启动会初始化数据目录与内嵌服务，通常几秒钟。\r\n" +
		"opencode 窗口出现后本窗口会自动关闭。")
	ui.SetStatus("正在启动 opencode ...", -1)

	cmd := exec.Command(appExe)
	cmd.Dir = filepath.Dir(appExe)
	cmd.Env = envList
	if err := cmd.Start(); err != nil {
		logs = append(logs, "ERROR: "+err.Error())
		appendLog(logPath, logs)
		ui.Fail("无法启动 opencode")
		ui.Close()
		messageBox("OpenCode 便携版 · 启动失败",
			"opencode 无法启动：\r\n\r\n"+err.Error()+"\r\n\r\n"+
				"日志：data\\launcher.log", 0x10)
		os.Exit(3)
	}
	appPID := cmd.Process.Pid
	logs = append(logs, fmt.Sprintf("started     : pid %d", appPID))
	appendLog(logPath, logs)

	// keep the progress window until opencode really has a window on screen
	ui.SetStatus("正在等待 opencode 窗口 ...", 97)
	switch waitForAppOrExit(appPID, 90*time.Second) {
	case appWindowUp:
		ui.SetStatus("opencode 已启动", 100)
		logs = append(logs, "opencode window is up - closing the progress window")
	case appExited:
		logs = append(logs, "opencode exited before showing a window")
		appendLog(logPath, logs)
		ui.Fail("opencode 启动后立即退出")
		ui.Close()
		messageBox("OpenCode 便携版 · 启动失败",
			"opencode 启动后很快就退出了（没有出现窗口）。\r\n\r\n"+
				"请查看日志：\r\n  data\\launcher.log\r\n  data\\userdata\\logs\r\n\r\n"+
				"如果是第一次运行，也可以删掉 OpenCode 目录后重新双击 OpenCode-Portable.exe 重建。", 0x30)
		os.Exit(5)
	case appWindowTimeout:
		ui.SetStatus("opencode 已在后台运行", 100)
		logs = append(logs, "no opencode window within 90s - closing the progress window anyway")
	}
	appendLog(logPath, logs)
	time.Sleep(700 * time.Millisecond)
	ui.Close()

	// Leave the app running on its own and close the launcher.
	_ = cmd.Process.Release()
	os.Exit(0)
}
