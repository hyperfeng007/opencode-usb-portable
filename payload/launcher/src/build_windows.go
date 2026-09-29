//go:build windows

package main

// First-time build orchestration for the portable launcher, driven by the
// progress window (ui_windows.go).
//
// Flow when opencode is not installed yet:
//  1. refuse to start a second build while one is already running - the second
//     launcher instead *follows* the running build and shows its progress;
//  2. run the builder directly (no cmd.exe, no batch parsing);
//  3. poll data\build-state.json, which the builder updates with the current
//     stage and percentage, and mirror it into the progress bar;
//  4. on success start opencode and close the progress window as soon as the
//     opencode window is up; on failure paint the bar red and show a dialog
//     with the reason and the tail of data\build.log.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type launcherBuildState struct {
	Status    string `json:"status"`
	PID       int    `json:"pid"`
	Builder   string `json:"builder"`
	Stage     string `json:"stage"`
	Progress  int    `json:"progress"`
	StartedAt string `json:"startedAt"`
	Updated   string `json:"updated"`
	Reason    string `json:"reason"`
}

var (
	procOpenProcess = kernel32.NewProc("OpenProcess")
	procCloseHandle = kernel32.NewProc("CloseHandle")
)

const processQueryLimitedInfo = 0x1000

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, _, _ := procOpenProcess.Call(processQueryLimitedInfo, 0, uintptr(uint32(pid)))
	if handle == 0 {
		return false
	}
	procCloseHandle.Call(handle)
	return true
}

func stateFile(dataDir string) string {
	return filepath.Join(dataDir, "build-state.json")
}

func readLauncherState(dataDir string) (launcherBuildState, bool) {
	var state launcherBuildState
	raw, err := os.ReadFile(stateFile(dataDir))
	if err != nil {
		return state, false
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, false
	}
	return state, true
}

func writeLauncherState(dataDir string, status string, pid int, reason string) {
	now := time.Now().Format(time.RFC3339)
	state := launcherBuildState{
		Status:   status,
		PID:      pid,
		Builder:  "launcher " + launcherVersion,
		Stage:    "准备安装环境",
		Progress: 2,
		Updated:  now,
		Reason:   reason,
	}
	if previous, ok := readLauncherState(dataDir); ok && previous.StartedAt != "" && status != "starting" {
		state.StartedAt = previous.StartedAt
	} else {
		state.StartedAt = now
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(dataDir, 0o755)
	tmp := stateFile(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err == nil {
		_ = os.Rename(tmp, stateFile(dataDir))
	}
}

// buildInProgress reports whether another first-time build is still working.
func buildInProgress(dataDir string) (bool, string) {
	state, ok := readLauncherState(dataDir)
	if !ok {
		return false, ""
	}
	switch state.Status {
	case "running", "starting":
	default:
		return false, ""
	}
	if processAlive(state.PID) {
		return true, state.Status
	}
	// process is gone: only block for a short while, in case the state file is
	// simply a moment behind
	if updated, err := time.Parse(time.RFC3339, state.Updated); err == nil {
		if time.Since(updated) < 2*time.Minute {
			return true, state.Status
		}
	}
	return false, ""
}

// tailOfLog returns the last n lines of a log file.
func tailOfLog(path string, n int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := 0
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i] == '\n' {
			lines++
			if lines > n {
				return string(raw[i+1:])
			}
		}
	}
	return string(raw)
}

func skipPortableGit() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("OPENCODE_PORTABLE_SKIP_GIT")))
	return value == "1" || value == "true"
}

// spawnFirstTimeBuild starts tools\OpenCode-Portable-Builder.exe.
func spawnFirstTimeBuild(root string, dataDir string, logPath string, logs *[]string, ui *progressUI) (int, error) {
	builderExe := filepath.Join(root, "tools", "OpenCode-Portable-Builder.exe")
	if !exists(builderExe) {
		return 0, fmt.Errorf("缺少安装程序：%s\\r\\n\\r\\n请把压缩包里的 tools 文件夹完整解压到 U 盘，"+
			"然后重新双击 OpenCode-Portable.exe。", builderExe)
	}

	arguments := `--root "` + root + `"`
	if !skipPortableGit() {
		arguments += " --with-git"
	}
	if extra := strings.TrimSpace(os.Getenv("OPENCODE_BUILDER_ARGS")); extra != "" {
		arguments += " " + extra
	}

	commandLine := `"` + builderExe + `" ` + arguments
	*logs = append(*logs, "builder command line: "+commandLine)
	appendLog(logPath, *logs)

	// mark the state so a second double-click does not start a second build
	writeLauncherState(dataDir, "starting", 0, "")

	build := exec.Command(builderExe)
	build.Dir = root
	build.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewConsole,
		CmdLine:       commandLine,
	}
	if err := build.Start(); err != nil {
		return 0, err
	}
	pid := build.Process.Pid
	_ = build.Process.Release()
	return pid, nil
}

// followBuild mirrors data\build-state.json into the progress window until the
// build finishes.
func followBuild(root string, dataDir string, ui *progressUI, builderPID int, timeout time.Duration) (bool, string) {
	deadline := time.Now().Add(timeout)
	started := false
	spawned := time.Now()
	lastStage := ""

	for {
		state, ok := readLauncherState(dataDir)
		switch {
		case ok && state.Status == "ok":
			return true, ""
		case ok && state.Status == "failed":
			return false, state.Reason
		case ok && state.Status == "running" && state.Builder != "launcher "+launcherVersion:
			started = true
			stage := state.Stage
			if stage == "" {
				stage = "正在安装 opencode"
			}
			if stage != lastStage {
				lastStage = stage
				ui.SetHint(hintForStage(stage))
			}
			ui.SetStatus(fmt.Sprintf("%s ... %d%%", stage, state.Progress), state.Progress)
		case builderPID > 0 && !started:
			// give the builder a few seconds to write its first state
			if time.Since(spawned) > 10*time.Second {
				if !processAlive(builderPID) {
					return false, "安装程序启动后立即退出了。请检查是否被安全软件拦截，" +
						"或手动运行 tools\\OpenCode-Portable-Builder.exe 查看提示。"
				}
			}
		}

		if time.Now().After(deadline) {
			return false, "安装超时（已等待很久仍未完成）。详见 data\\build.log。"
		}
		time.Sleep(350 * time.Millisecond)
	}
}

func hintForStage(stage string) string {
	switch {
	case strings.Contains(stage, "下载"):
		return "正在从 GitHub 下载 opencode（约 120 MB），视网速可能需要几分钟。\r\n" +
			"请不要拔掉 U 盘。下载内容会按官方 SHA-256 校验。"
	case strings.Contains(stage, "解包"):
		return "正在把下载的安装包解开到 U 盘（约 500 MB），U 盘速度决定耗时。\r\n" +
			"请不要拔掉 U 盘。进度明细见 data\\build.log。"
	case strings.Contains(stage, "补丁") || strings.Contains(stage, "改造"):
		return "正在做便携化改造：把可写目录全部指向 U 盘，并关闭自动更新。"
	case strings.Contains(stage, "重建") || strings.Contains(stage, "校验"):
		return "正在重建并校验应用归档，这一步确保 opencode 能正常启动。"
	case strings.Contains(stage, "Git"):
		return "正在安装便携版 Git（约 56 MB）。"
	default:
		return "首次安装会下载并改造 opencode，完成后会自动启动。\r\n" +
			"完整日志：data\\build.log   状态：data\\build-state.json"
	}
}

// runFirstTimeBuild is the whole "not installed yet" path. It returns true when
// opencode should be started by this launcher.
func runFirstTimeBuild(root string, dataDir string, logPath string, logs *[]string, ui *progressUI) bool {
	if running, _ := buildInProgress(dataDir); running {
		*logs = append(*logs, "another first-time build is already running - following it")
		appendLog(logPath, *logs)
		ui.SetStatus("首次安装已在进行中，正在跟随进度 ...", -1)
		ui.SetHint("检测到另一个窗口正在安装，这里会一直显示它的进度。\r\n完成后会自动接着启动 opencode。")
		if ok, reason := followBuild(root, dataDir, ui, 0, 60*time.Minute); !ok {
			ui.Fail(reason)
			ui.Close()
			messageBox("OpenCode 便携版 · 安装失败",
				"安装没有完成：\r\n\r\n"+reason+"\r\n\r\n最近日志（data\\build.log）：\r\n"+
					tailOfLog(filepath.Join(dataDir, "build.log"), 14), 0x30)
			os.Exit(4)
		}
		ui.SetStatus("安装完成", 100)
		time.Sleep(600 * time.Millisecond)
		// the other launcher starts opencode itself - do not start a second one
		return false
	}

	ui.SetStatus("正在启动安装程序 ...", 3)
	pid, err := spawnFirstTimeBuild(root, dataDir, logPath, logs, ui)
	if err != nil {
		*logs = append(*logs, "ERROR: "+err.Error())
		appendLog(logPath, *logs)
		ui.Fail("无法启动安装程序")
		ui.Close()
		messageBox("OpenCode 便携版 · 无法安装",
			"无法启动安装程序：\r\n\r\n"+err.Error()+"\r\n\r\n"+
				"也可以手动双击 \"Build portable version.cmd\" 安装，或把该错误信息发给我。", 0x10)
		os.Exit(3)
	}
	*logs = append(*logs, fmt.Sprintf("first-time build started: pid %d", pid))
	appendLog(logPath, *logs)

	ui.SetStatus("正在安装 opencode ...", 5)
	ok, reason := followBuild(root, dataDir, ui, pid, 60*time.Minute)
	if !ok {
		*logs = append(*logs, "build failed: "+reason)
		appendLog(logPath, *logs)
		ui.Fail(reason)
		ui.Close()
		messageBox("OpenCode 便携版 · 安装失败",
			"安装没有完成，原因如下：\r\n\r\n"+reason+"\r\n\r\n"+
				"最近日志（data\\build.log）：\r\n"+
				tailOfLog(filepath.Join(dataDir, "build.log"), 14)+"\r\n\r\n"+
				"常见处理：\r\n"+
				"  1) 先打开系统代理再重试（本程序会自动使用系统代理）\r\n"+
				"  2) 换版本：OPENCODE_BUILDER_ARGS=--version v1.18.31\r\n"+
				"  3) 离线：OPENCODE_BUILDER_ARGS=--installer \"D:\\opencode-desktop-win-x64.exe\"\r\n"+
				"完整日志：data\\build.log", 0x30)
		os.Exit(4)
	}
	ui.SetStatus("安装完成", 100)
	time.Sleep(500 * time.Millisecond)
	return true
}
