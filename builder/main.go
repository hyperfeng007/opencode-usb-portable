package main

// OpenCode Portable - first-run builder
//
// Downloads the official opencode desktop build, unpacks it onto the USB
// stick with the bundled 7za.exe, converts it to a portable application
// (patched ASAR + bootstrap) and writes the portable marker file.
//
//	OpenCode-Portable-Builder.exe [flags]
//
//	  --root <dir>        便携根目录（默认：本程序所在目录，若在 tools\ 下则取其上级）
//	  --version <tag>     指定版本（默认固定版本；也可用 latest）
//	  --installer <file>  使用本地安装包，不联网下载
//	  --sevenzip <file>   指定 7za 可执行文件
//	  --arch <x64|arm64>  目标架构（默认 x64）
//	  --with-git          同时下载便携版 Git
//	  --update            强制重新构建（覆盖已有的 OpenCode 目录）
//	  --keep-backup       保留 app.asar.orig 备份
//	  --dry-run           只检查与下载，不做修改
//	  --patch-only <asar> 仅对已有的 app.asar 打补丁（调试用）

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	appVersion     = "1.1.5"
	defaultVersion = "v1.18.31" // 已验证过补丁锚点的版本
	repoOwner      = "anomalyco"
	repoName       = "opencode"
	requiredFreeGB = 1.5
	userAgent      = "opencode-portable-builder/" + appVersion
)

type options struct {
	root        string
	version     string
	installer   string
	sevenZip    string
	arch        string
	withGit     bool
	update      bool
	keepBackup  bool
	dryRun      bool
	patchOnly   string
	proxy       string
	launchAfter bool
	noMirror    bool
}

type buildInfo struct {
	Builder    string `json:"builder"`
	Built      string `json:"built"`
	Opencode   string `json:"opencodeVersion"`
	Arch       string `json:"arch"`
	Installer  string `json:"installerSha256"`
	BootBytes  int    `json:"bootstrapBytes"`
	BundleSize int    `json:"mainBundleBytes"`
	Files      int    `json:"archiveFiles"`
}

func main() {
	initConsoleCodepage()
	opts := parseFlags()

	if opts.patchOnly != "" {
		boot, err := bootstrapSource()
		must(err)
		report, err := PatchArchive(opts.patchOnly, boot, !opts.keepBackup, logf)
		must(err)
		logf("完成：%d → %d 个文件，%.1f MB → %.1f MB", report.Files, report.NewFiles,
			float64(report.Bytes)/1048576, float64(report.NewBytes)/1048576)
		return
	}

	started := time.Now()
	banner()
	root, err := resolveRoot(opts.root)
	must(err)
	logf("便携根目录 : %s", root)
	openBuildLog(root)
	currentRoot = root
	writeBuildState(root, "running", "")
	reportStage("正在检查环境", 2, 4)
	logf("构建器     : opencode-portable builder v%s", appVersion)
	logf("便携根目录 : %s", root)

	if err := ensureDataDirs(root); err != nil {
		must(err)
	}

	appDir := filepath.Join(root, "OpenCode")
	appExe := filepath.Join(appDir, "OpenCode.exe")
	alreadyBuilt := fileExists(appExe)

	if alreadyBuilt && !opts.update {
		logf("")
		logf("检测到已经构建完成（%s）。", appExe)
		logf("如需更新到新版本，请运行：  OpenCode-Portable-Builder.exe --update")
		os.Exit(0)
	}

	if err := checkFreeSpace(root); err != nil {
		must(err)
	}

	proxy, proxySource := resolveProxy(opts.proxy)
	detectedProxy = proxy
	noMirrorFallback = opts.noMirror
	if proxy == "" {
		logf("网络代理   : %s", proxySource)
		logf("              （如果下载失败，请先打开系统代理，或用 --proxy 指定）")
	} else {
		logf("网络代理   : %s  （来源：%s）", proxy, proxySource)
	}
	if mirrorPrefix() != "" {
		logf("下载镜像   : %s", mirrorPrefix())
	}

	sevenZip, err := locate7za(root, opts.sevenZip)
	must(err)
	logf("解包工具   : %s", sevenZip)

	assetName := fmt.Sprintf("opencode-desktop-win-%s.exe", opts.arch)
	installerPath, installerSHA, err := obtainInstaller(root, opts)
	must(err)
	logf("安装包     : %s", installerPath)
	if installerSHA != "" {
		logf("SHA-256    : %s", installerSHA)
	}

	if opts.dryRun {
		logf("")
		logf("--dry-run：已完成下载与校验，未做任何修改。")
		return
	}

	if alreadyBuilt {
		logf("")
		logf("[0/5] 移除旧的 OpenCode 目录（用户数据 data\\ 不受影响）")
		if err := os.RemoveAll(appDir); err != nil {
			must(fmt.Errorf("无法删除旧目录 %s: %w", appDir, err))
		}
	}

	logf("")
	logf("[1/5] 解包应用（约 500 MB，视 U 盘速度可能需要几分钟）")
	reportStage("正在解包应用（约 500 MB）", 42, 58)
	reportFraction(0.05)
	if err := extractWith7za(sevenZip, installerPath, appDir); err != nil {
		must(err)
	}
	reportDone()

	if !fileExists(appExe) {
		must(fmt.Errorf("解包完成但找不到 %s，可能安装包格式有变化", appExe))
	}
	archivePath := filepath.Join(appDir, "resources", "app.asar")
	if !fileExists(archivePath) {
		must(fmt.Errorf("解包完成但找不到 %s", archivePath))
	}

	logf("")
	logf("[2/5] 便携化改造：为 app.asar 打补丁")
	reportStage("正在做便携化改造（打补丁）", 62, 76)
	boot, err := bootstrapSource()
	must(err)
	rebuildProgress = reportFraction
	report, err := PatchArchive(archivePath, boot, opts.keepBackup, func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		logf(format, args...)
		// mirror the patcher's own steps into the launcher's progress bar
		switch {
		case strings.Contains(line, "[1/5]"):
			reportStage("正在读取应用归档", 62, 66)
		case strings.Contains(line, "[2/5]"):
			reportStage("正在注入便携引导", 66, 70)
		case strings.Contains(line, "[3/5]"):
			reportStage("正在重建应用归档（约 140 MB）", 70, 84)
		case strings.Contains(line, "已写出"):
			reportFraction(0.5)
		case strings.Contains(line, "[4/5]"):
			reportStage("正在校验新归档", 84, 90)
		case strings.Contains(line, "[5/5]"):
			reportStage("正在替换原归档", 90, 93)
		}
	})
	if err != nil {
		must(fmt.Errorf("补丁失败：%w\n提示：可尝试指定已验证版本 --version %s", err, defaultVersion))
	}
	reportDone()

	logf("")
	logf("[3/5] 写入便携标记与目录")
	reportStage("正在写入便携标记与目录", 78, 82)
	marker := filepath.Join(root, "OpenCode-Portable.json")
	if !fileExists(marker) {
		if err := os.WriteFile(marker, markerContent(), 0o644); err != nil {
			must(err)
		}
		logf("       已写入 %s", filepath.Base(marker))
	} else {
		logf("       便携标记已存在，保持不变")
	}

	if opts.withGit {
		logf("")
		logf("[4/5] 下载便携版 Git")
		reportStage("正在安装便携版 Git（约 56 MB）", 84, 92)
		if err := installPortableGit(root, sevenZip, opts); err != nil {
			logf("       [!] Git 安装失败（不影响 opencode 使用）：%v", err)
		}
	} else {
		logf("")
		logf("[4/5] 跳过便携版 Git（需要时用 --with-git）")
	}

	logf("")
	logf("[5/5] 记录构建信息")
	reportStage("正在记录构建信息", 94, 98)
	info := buildInfo{
		Builder:    appVersion,
		Built:      time.Now().Format(time.RFC3339),
		Opencode:   opts.version,
		Arch:       opts.arch,
		Installer:  installerSHA,
		BootBytes:  report.BootBytes,
		BundleSize: report.BundleBytes,
		Files:      report.NewFiles,
	}
	infoBytes, _ := json.MarshalIndent(info, "", "  ")
	_ = os.WriteFile(filepath.Join(root, "version.json"), infoBytes, 0o644)

	logf("")
	logf("构建完成，用时 %s", time.Since(started).Round(time.Second))
	logf("")
	logf("现在双击 %s 即可启动 opencode。", filepath.Join(root, "OpenCode-Portable.exe"))
	logf("全部用户数据都会保存在 %s", filepath.Join(root, "data"))
	logf("安装包已缓存，重新构建时无需再次下载：%s", installerPath)
	_ = assetName
	reportStage("构建完成", 100, 100)
	writeBuildState(root, "ok", "")
	if buildLogFile != nil {
		logf("结果       : 成功")
		buildLogFile.Close()
	}
	if opts.launchAfter {
		launcherExe := filepath.Join(root, "OpenCode-Portable.exe")
		if fileExists(launcherExe) {
			logf("正在启动 %s", launcherExe)
			launch := exec.Command(launcherExe)
			launch.Dir = root
			if err := launch.Start(); err != nil {
				logf("[提示] 自动启动失败：%v（可手动双击 OpenCode-Portable.exe）", err)
			} else {
				_ = launch.Process.Release()
			}
		}
	}
}

// ---------------------------------------------------------------------------
// flags / bootstrapping
// ---------------------------------------------------------------------------
func parseFlags() options {
	var opts options
	flag.StringVar(&opts.root, "root", "", "便携根目录")
	flag.StringVar(&opts.version, "version", defaultVersion, "opencode 版本标签，例如 v1.18.31 或 latest")
	flag.StringVar(&opts.installer, "installer", "", "使用本地安装包（跳过下载）")
	flag.StringVar(&opts.sevenZip, "sevenzip", "", "7za 可执行文件路径")
	flag.StringVar(&opts.arch, "arch", "x64", "目标架构：x64 或 arm64")
	flag.BoolVar(&opts.withGit, "with-git", false, "同时下载便携版 Git")
	flag.BoolVar(&opts.update, "update", false, "强制重新构建")
	flag.BoolVar(&opts.keepBackup, "keep-backup", false, "保留 app.asar.orig")
	flag.BoolVar(&opts.dryRun, "dry-run", false, "只下载与校验，不修改")
	flag.StringVar(&opts.patchOnly, "patch-only", "", "只对指定 app.asar 打补丁")
	flag.StringVar(&opts.proxy, "proxy", "", "下载时使用的代理，例如 http://127.0.0.1:7897（默认自动检测）")
	flag.BoolVar(&opts.launchAfter, "launch-after-build", false, "构建成功后自动启动 opencode")
	flag.BoolVar(&opts.noMirror, "no-mirror", false, "禁止在直连失败时改用公共镜像重试")
	flag.Parse()
	return opts
}

func resolveRoot(given string) (string, error) {
	if given != "" {
		abs, err := filepath.Abs(given)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	// 常见布局：<root>\tools\OpenCode-Portable-Builder.exe
	if strings.EqualFold(filepath.Base(dir), "tools") {
		parent := filepath.Dir(dir)
		if looksLikePortableRoot(parent) || !looksLikePortableRoot(dir) {
			return parent, nil
		}
	}
	return dir, nil
}

func looksLikePortableRoot(dir string) bool {
	return fileExists(filepath.Join(dir, "OpenCode-Portable.json")) ||
		fileExists(filepath.Join(dir, ".opencode-portable")) ||
		fileExists(filepath.Join(dir, "OpenCode-Portable.exe"))
}

func ensureDataDirs(root string) error {
	data := filepath.Join(root, "data")
	for _, dir := range []string{
		"userdata", filepath.Join("userdata", "session"), filepath.Join("userdata", "Crashpad"),
		"config", "data", "cache", "state", "home", "tmp", "roaming", "local",
		filepath.Join("cache", "build"),
	} {
		if err := os.MkdirAll(filepath.Join(data, dir), 0o755); err != nil {
			return fmt.Errorf("无法创建 %s: %w", dir, err)
		}
	}
	return nil
}

func markerContent() []byte {
	return []byte("{\n" +
		"  \"name\": \"OpenCode Portable\",\n" +
		"  \"kind\": \"opencode-usb-portable\",\n" +
		"  \"schema\": 1,\n" +
		"  \"comment\": \"便携模式标记文件，请勿删除。启动器依靠它定位 U 盘根目录，所有数据都会写入同级的 data\\\\ 文件夹。\"\n" +
		"}\n")
}

func locate7za(root string, given string) (string, error) {
	if given != "" {
		if fileExists(given) {
			return given, nil
		}
		return "", fmt.Errorf("指定的 7za 不存在: %s", given)
	}
	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)
	name := "7za.exe"
	if runtime.GOOS != "windows" {
		name = "7za"
	}
	candidates := []string{
		filepath.Join(root, "tools", "7za", name),
		filepath.Join(exeDir, "7za", name),
		filepath.Join(exeDir, name),
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath("7za"); err == nil {
		return path, nil
	}
	return "", errors.New("找不到 7za.exe。请确认 tools\\7za\\7za.exe 存在")
}

// ---------------------------------------------------------------------------
// installer acquisition
// ---------------------------------------------------------------------------
type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
	Hash string `json:"digest"`
}

type release struct {
	Tag    string         `json:"tag_name"`
	Assets []releaseAsset `json:"assets"`
}

func obtainInstaller(root string, opts options) (string, string, error) {
	cacheDir := filepath.Join(root, "data", "cache", "build")

	if opts.installer != "" {
		path, err := filepath.Abs(opts.installer)
		if err != nil {
			return "", "", err
		}
		if !fileExists(path) {
			return "", "", fmt.Errorf("本地安装包不存在: %s", path)
		}
		sum, err := fileSHA256(path)
		if err != nil {
			return "", "", err
		}
		return path, sum, nil
	}

	logf("正在查询 opencode 版本 %s ...", opts.version)
	rel, err := fetchRelease(opts.version)
	if err != nil {
		return "", "", err
	}
	assetName := fmt.Sprintf("opencode-desktop-win-%s.exe", opts.arch)
	var asset *releaseAsset
	for i := range rel.Assets {
		if rel.Assets[i].Name == assetName {
			asset = &rel.Assets[i]
			break
		}
	}
	if asset == nil {
		return "", "", fmt.Errorf("版本 %s 中没有找到 %s", rel.Tag, assetName)
	}
	logf("找到资源   : %s (%.1f MB)", asset.Name, float64(asset.Size)/1048576)

	target := filepath.Join(cacheDir, rel.Tag+"-"+asset.Name)
	expected := strings.TrimPrefix(asset.Hash, "sha256:")

	if fileExists(target) && expected != "" {
		if sum, err := fileSHA256(target); err == nil && sum == expected {
			logf("使用缓存   : %s", target)
			return target, sum, nil
		}
	}

	reportStage("正在下载 opencode 安装包", 10, 40)
	logf("开始下载   : %s", asset.URL)
	if err := retryWithMirrors("下载 opencode", func() error {
		logf("       地址 %s", remoteURL(asset.URL))
		return download(remoteURL(asset.URL), target, asset.Size)
	}); err != nil {
		return "", "", err
	}
	sum, err := fileSHA256(target)
	if err != nil {
		return "", "", err
	}
	if expected != "" && sum != expected {
		os.Remove(target)
		return "", "", fmt.Errorf("下载文件校验失败：期望 %s，实际 %s", expected, sum)
	}
	logf("下载完成并校验通过")
	return target, sum, nil
}

func fetchRelease(version string) (*release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", apiBase(), repoOwner, repoName)
	if version != "latest" && version != "" {
		url = fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", apiBase(), repoOwner, repoName, version)
	}
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", "application/vnd.github+json")

	var rel release
	var directErr error
	err = retryWithMirrors("读取版本信息", func() error {
		request, err := http.NewRequest("GET", remoteURL(url), nil)
		if err != nil {
			return err
		}
		request.Header.Set("User-Agent", userAgent)
		request.Header.Set("Accept", "application/vnd.github+json")
		client := newHTTPClient(60 * time.Second)
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("GitHub 返回 %s（版本号是否存在？）", response.Status)
		}
		rel = release{}
		return json.NewDecoder(response.Body).Decode(&rel)
	})
	if err != nil {
		directErr = err
		return nil, fmt.Errorf("无法获取 %s 的版本信息：%w\n\n若网络受限：\n"+
			"  * 先打开系统代理再重试（本程序会自动读取系统代理）\n"+
			"  * 或指定代理：  --proxy http://127.0.0.1:7897\n"+
			"  * 或设置镜像：  set OPENCODE_PORTABLE_MIRROR=https://ghfast.top/\n"+
			"  * 或离线构建：  --installer \"D:\\opencode-desktop-win-x64.exe\"", apiBase(), directErr)
	}
	return &rel, nil
}

func download(url string, target string, expectedSize int64) error {
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", userAgent)

	client := newHTTPClient(0) // 大文件，不设总超时
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("下载失败：HTTP %s", response.Status)
	}

	temp := target + ".part"
	out, err := os.Create(temp)
	if err != nil {
		return err
	}
	defer func() {
		out.Close()
		os.Remove(temp)
	}()

	total := response.ContentLength
	if total <= 0 {
		total = expectedSize
	}
	written := int64(0)
	start := time.Now()
	lastReport := time.Time{}
	buf := make([]byte, 1024*1024)
	for {
		read, readErr := response.Body.Read(buf)
		if read > 0 {
			if _, writeErr := out.Write(buf[:read]); writeErr != nil {
				return writeErr
			}
			written += int64(read)
			if time.Since(lastReport) > 300*time.Millisecond {
				lastReport = time.Now()
				printProgress(written, total, start)
				if total > 0 {
					reportFraction(float64(written) / float64(total))
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return readErr
		}
	}
	printProgress(written, total, start)
	fmt.Println()
	reportFraction(1)
	if total > 0 && written != total {
		return fmt.Errorf("下载不完整：%d / %d 字节", written, total)
	}
	if err := out.Sync(); err != nil {
		return err
	}
	out.Close()
	return os.Rename(temp, target)
}

func printProgress(done int64, total int64, start time.Time) {
	seconds := time.Since(start).Seconds()
	if seconds <= 0 {
		seconds = 0.001
	}
	speed := float64(done) / 1048576 / seconds
	if total > 0 {
		fmt.Printf("\r        %5.1f%%  %6.1f / %6.1f MB  %5.1f MB/s",
			float64(done)*100/float64(total), float64(done)/1048576, float64(total)/1048576, speed)
	} else {
		fmt.Printf("\r        %6.1f MB  %5.1f MB/s", float64(done)/1048576, speed)
	}
}

// ---------------------------------------------------------------------------
// extraction
// ---------------------------------------------------------------------------
func extractWith7za(sevenZip string, archive string, destination string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	fmt.Println("       解压中（7-Zip 会实时输出进度）...")
	cmd := exec.Command(sevenZip, "x", "-y", "-o"+destination, archive)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	err := cmd.Run()
	// 7-Zip 对“归档末尾有多余数据”的 NSIS/SFX 安装包返回 1，属正常情况
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			fmt.Println("       （7-Zip 报告了警告，通常可忽略）")
			return nil
		}
		return fmt.Errorf("7-Zip 解包失败: %w", err)
	}
	return nil
}

func installPortableGit(root string, sevenZip string, opts options) error {
	const pattern = "PortableGit-*64-bit.7z.exe"
	type gitAsset struct {
		Name string
		URL  string
		Size int64
		Hash string
	}
	var payload struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
			Hash string `json:"digest"`
		} `json:"assets"`
	}
	if err := retryWithMirrors("读取 Git for Windows 版本信息", func() error {
		request, err := http.NewRequest("GET", remoteURL(apiBase()+"/repos/git-for-windows/git/releases/latest"), nil)
		if err != nil {
			return err
		}
		request.Header.Set("User-Agent", userAgent)
		client := newHTTPClient(60 * time.Second)
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		payload = struct {
			Tag    string `json:"tag_name"`
			Assets []struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
				Size int64  `json:"size"`
				Hash string `json:"digest"`
			} `json:"assets"`
		}{}
		return json.NewDecoder(response.Body).Decode(&payload)
	}); err != nil {
		return err
	}
	var chosen *gitAsset
	for _, asset := range payload.Assets {
		if strings.HasPrefix(asset.Name, "PortableGit-") && strings.HasSuffix(asset.Name, "64-bit.7z.exe") {
			chosen = &gitAsset{asset.Name, asset.URL, asset.Size, strings.TrimPrefix(asset.Hash, "sha256:")}
			break
		}
	}
	if chosen == nil {
		return fmt.Errorf("Git for Windows %s 中没有找到便携版资源", payload.Tag)
	}
	_ = pattern
	target := filepath.Join(root, "data", "cache", "build", payload.Tag+"-"+chosen.Name)
	if !fileExists(target) {
		logf("       下载 %s (%.1f MB)", chosen.Name, float64(chosen.Size)/1048576)
		if err := retryWithMirrors("下载 PortableGit", func() error {
			return download(remoteURL(chosen.URL), target, chosen.Size)
		}); err != nil {
			return err
		}
	}
	if chosen.Hash != "" {
		if sum, err := fileSHA256(target); err == nil && sum != chosen.Hash {
			return fmt.Errorf("Git 安装包校验失败")
		}
	}
	gitDir := filepath.Join(root, "tools", "git")
	if err := os.RemoveAll(gitDir); err != nil {
		return err
	}
	logf("       解压到 tools\\git ...")
	return extractWith7za(sevenZip, target, gitDir)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func banner() {
	fmt.Println()
	fmt.Println("=========================================================")
	fmt.Printf(" OpenCode 便携版 · 首次构建器  v%s\n", appVersion)
	fmt.Println(" 会在 U 盘上直接完成安装、便携化改造与配置")
	fmt.Println("=========================================================")
	fmt.Println()
}

var buildLogFile *os.File

func openBuildLog(root string) {
	path := filepath.Join(root, "data", "build.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if info, err := os.Stat(path); err == nil && info.Size() > 512*1024 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	buildLogFile = f
	fmt.Fprintf(f, "\r\n==== %s ====\r\n", time.Now().Format("2006-01-02 15:04:05"))
}

// logf writes to the console and, when available, to data\build.log so the
// build can be reviewed after the console window is closed.
func logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	fmt.Println(line)
	if buildLogFile != nil {
		fmt.Fprintf(buildLogFile, "%s\r\n", line)
	}
}

func must(err error) {
	if err == nil {
		return
	}
	message := fmt.Sprintf("%v", err)
	logf("")
	logf("  [错误] %s", message)
	logf("")
	logf("  日志文件：%s", filepath.Join(currentRoot, "data", "build.log"))
	logf("  常见原因：1) 网络/代理问题  2) 版本不兼容（试 --version v1.18.31）  3) 空间不足")
	writeState(currentRoot, "failed", message, "安装失败", 0)
	if buildLogFile != nil {
		buildLogFile.Close()
	}
	waitForUser()
	os.Exit(1)
}
