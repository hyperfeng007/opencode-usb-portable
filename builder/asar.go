package main

// Dependency-free ASAR reader / patcher.
//
// Port of payload/patch-asar.mjs, validated against the real 137.7 MB opencode
// desktop archive (6992 files in the rebuilt archive, everything except the two
// intentionally changed files byte-identical to the original).

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	bootFileName  = "opencode-portable-boot.js"
	bootArchive   = "out/main/" + bootFileName
	bootImportTag = `import "./` + bootFileName + `"; // opencode-portable`
	mainBundle    = "out/main/index.js"
	blockSize     = 4 * 1024 * 1024
	copyChunk     = 4 * 1024 * 1024
)

type entry struct {
	path       string
	node       *node
	size       int64
	origOffset int64
	unpacked   bool
}

type archive struct {
	header     *node
	headerSize int64
	dataOffset int64
	fileSize   int64
	files      []*entry // packed + unpacked entries, in header order
	byPath     map[string]*entry
}

func align4(n int64) int64 { return n + ((4 - (n % 4)) % 4) }

func readArchive(path string) (*archive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sizeBuf := make([]byte, 8)
	if _, err := io.ReadFull(f, sizeBuf); err != nil {
		return nil, fmt.Errorf("读取 ASAR 头部失败: %w", err)
	}
	if binary.LittleEndian.Uint32(sizeBuf[0:4]) != 4 {
		return nil, fmt.Errorf("ASAR 尺寸头格式不符")
	}
	headerSize := int64(binary.LittleEndian.Uint32(sizeBuf[4:8]))
	if headerSize <= 0 || headerSize > 64*1024*1024 {
		return nil, fmt.Errorf("ASAR 头部大小异常: %d", headerSize)
	}
	headerBuf := make([]byte, headerSize)
	if _, err := io.ReadFull(f, headerBuf); err != nil {
		return nil, fmt.Errorf("读取 ASAR 头部失败: %w", err)
	}
	payloadSize := int64(binary.LittleEndian.Uint32(headerBuf[0:4]))
	jsonLength := int64(binary.LittleEndian.Uint32(headerBuf[4:8]))
	if jsonLength == 0 || jsonLength > payloadSize {
		return nil, fmt.Errorf("ASAR 头部 pickle 损坏")
	}
	header, err := decodeJSON(headerBuf[8 : 8+jsonLength])
	if err != nil {
		return nil, fmt.Errorf("解析 ASAR 头部 JSON 失败: %w", err)
	}
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}

	a := &archive{
		header:     header,
		headerSize: headerSize,
		dataOffset: 8 + headerSize,
		fileSize:   stat.Size(),
		byPath:     map[string]*entry{},
	}
	a.collect(header, "")
	return a, nil
}

func (a *archive) collect(dir *node, prefix string) {
	filesNode, ok := dir.get("files")
	if !ok || filesNode.kind != kindObject {
		return
	}
	for _, name := range filesNode.keys {
		value := filesNode.obj[name]
		path := name
		if prefix != "" {
			path = prefix + "/" + name
		}
		if entries, ok := value.get("files"); ok && entries.kind == kindObject {
			a.collect(value, path)
			continue
		}
		e := &entry{path: path, node: value}
		if sizeNode, ok := value.get("size"); ok && sizeNode.kind == kindNumber {
			e.size, _ = sizeNode.num.Int64()
		}
		if offsetNode, ok := value.get("offset"); ok && offsetNode.kind == kindString {
			e.origOffset, _ = strconv.ParseInt(offsetNode.str, 10, 64)
		}
		if unpackedNode, ok := value.get("unpacked"); ok && unpackedNode.kind == kindBool {
			e.unpacked = unpackedNode.b
		}
		a.files = append(a.files, e)
		a.byPath[path] = e
	}
}

func (a *archive) readFileData(f *os.File, e *entry) ([]byte, error) {
	buf := make([]byte, e.size)
	if e.size == 0 {
		return buf, nil
	}
	if _, err := f.ReadAt(buf, a.dataOffset+e.origOffset); err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", e.path, err)
	}
	return buf, nil
}

// ---------------------------------------------------------------------------
// text patches applied to out/main/index.js
// ---------------------------------------------------------------------------
type patchResult struct {
	ID      string `json:"id"`
	Applied bool   `json:"applied"`
}

type textPatch struct {
	id       string
	find     string
	replace  string
	required bool
}

var mainBundlePatches = []textPatch{
	{
		id:       "updater-disabled",
		find:     `const UPDATER_ENABLED = app.isPackaged && CHANNEL !== "dev";`,
		replace:  `const UPDATER_ENABLED = app.isPackaged && CHANNEL !== "dev" && process.env.OPENCODE_PORTABLE !== "1";`,
		required: true,
	},
	{
		id:       "userdata-portable",
		find:     `onboardingTestRoot ? join(onboardingTestRoot, "desktop") : join(app.getPath("appData"), appId)`,
		replace:  `onboardingTestRoot ? join(onboardingTestRoot, "desktop") : (process.env.OPENCODE_PORTABLE_USERDATA || join(app.getPath("appData"), appId))`,
		required: true,
	},
	{
		id:       "sessiondata-portable",
		find:     `if (onboardingTestRoot) app.setPath("sessionData", join(onboardingTestRoot, "session"));`,
		replace:  `if (onboardingTestRoot) app.setPath("sessionData", join(onboardingTestRoot, "session"));` + "\n  " + `else if (process.env.OPENCODE_PORTABLE_USERDATA) app.setPath("sessionData", join(process.env.OPENCODE_PORTABLE_USERDATA, "session"));`,
		required: true,
	},
	{
		id:       "no-protocol-registration",
		find:     `app.setAsDefaultProtocolClient("opencode");`,
		replace:  `if (process.env.OPENCODE_PORTABLE !== "1") app.setAsDefaultProtocolClient("opencode");`,
		required: false,
	},
}

func patchMainBundle(source string, log func(string, ...any)) (string, []patchResult, error) {
	out := source
	var results []patchResult
	for _, patch := range mainBundlePatches {
		switch count := strings.Count(out, patch.find); {
		case count == 0:
			if patch.required {
				return "", nil, fmt.Errorf(
					"补丁 %q 未匹配到目标代码：这个 opencode 版本与当前补丁器不兼容（可用 --version 指定其它版本）", patch.id)
			}
			log("  [跳过] %s（此版本无需）", patch.id)
			results = append(results, patchResult{patch.id, false})
		case count > 1:
			return "", nil, fmt.Errorf("补丁 %q 匹配到 %d 处，拒绝猜测", patch.id, count)
		default:
			out = strings.Replace(out, patch.find, patch.replace, 1)
			log("  [完成] %s", patch.id)
			results = append(results, patchResult{patch.id, true})
		}
	}
	if !strings.Contains(out, bootImportTag) {
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += bootImportTag + "\n"
		log("  [完成] boot-import（便携引导已注入）")
	}
	return out, results, nil
}

// ---------------------------------------------------------------------------
// integrity block (same algorithm as @electron/asar)
// ---------------------------------------------------------------------------
func computeIntegrity(data []byte) *node {
	whole := sha256.Sum256(data)
	blocks := newArray()
	for offset := 0; offset < len(data); offset += blockSize {
		end := offset + blockSize
		if end > len(data) {
			end = len(data)
		}
		sum := sha256.Sum256(data[offset:end])
		blocks.append(&node{kind: kindString, str: hex.EncodeToString(sum[:])})
	}
	if len(data) == 0 {
		sum := sha256.Sum256(nil)
		blocks.append(&node{kind: kindString, str: hex.EncodeToString(sum[:])})
	}
	integrity := newObject()
	integrity.set("algorithm", &node{kind: kindString, str: "SHA256"})
	integrity.set("hash", &node{kind: kindString, str: hex.EncodeToString(whole[:])})
	integrity.set("blockSize", &node{kind: kindNumber, num: json.Number(strconv.Itoa(blockSize))})
	integrity.set("blocks", blocks)
	return integrity
}

// ---------------------------------------------------------------------------
// rebuild
// ---------------------------------------------------------------------------
type patchReport struct {
	Archive      string        `json:"archive"`
	Files        int           `json:"files"`
	NewFiles     int           `json:"newFiles"`
	Bytes        int64         `json:"bytes"`
	NewBytes     int64         `json:"newBytes"`
	Applied      []patchResult `json:"applied"`
	Backup       string        `json:"backup,omitempty"`
	BundleBytes  int           `json:"bundleBytes"`
	BootBytes    int           `json:"bootBytes"`
	NewFileAdded bool          `json:"newFileAdded"`
}

// rebuildProgress, when set, receives the fraction (0..1) of the archive that
// has been rewritten. The launcher shows this in its progress bar.
var rebuildProgress func(float64)

func PatchArchive(archivePath string, bootSource []byte, keepBackup bool, log func(string, ...any)) (*patchReport, error) {
	log("[1/5] 读取 %s", archivePath)
	a, err := readArchive(archivePath)
	if err != nil {
		return nil, err
	}
	log("       %.1f MB，%d 个文件，头部 %.0f KB", float64(a.fileSize)/1048576, len(a.files), float64(a.headerSize)/1024)

	if a.byPath[mainBundle] == nil {
		return nil, fmt.Errorf("归档中找不到 %s，这不是 opencode 桌面版", mainBundle)
	}

	log("[2/5] 修改 out/main/index.js")
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	original, err := a.readFileData(f, a.byPath[mainBundle])
	f.Close()
	if err != nil {
		return nil, err
	}
	patchedSource, applied, err := patchMainBundle(string(original), log)
	if err != nil {
		return nil, err
	}
	patchedBytes := []byte(patchedSource)

	log("[3/5] 重建归档")
	replacements := map[string][]byte{
		mainBundle:  patchedBytes,
		bootArchive: bootSource,
	}

	addedNewFile := a.byPath[bootArchive] == nil
	if addedNewFile {
		entryNode, err := ensureArchivePath(a.header, bootArchive)
		if err != nil {
			return nil, err
		}
		newEntry := &entry{path: bootArchive, node: entryNode, size: int64(len(bootSource)), origOffset: -1}
		// Insert in directory-tree order: the new key is appended to the
		// "out/main" object, so it is written right after the last file of
		// that directory and before "out/preload".
		insertAt := len(a.files)
		for i, e := range a.files {
			if strings.HasPrefix(e.path, "out/main/") {
				insertAt = i + 1
			}
		}
		ordered := make([]*entry, 0, len(a.files)+1)
		ordered = append(ordered, a.files[:insertAt]...)
		ordered = append(ordered, newEntry)
		ordered = append(ordered, a.files[insertAt:]...)
		a.files = ordered
		a.byPath[bootArchive] = newEntry
	}

	var cursor int64
	for _, e := range a.files {
		if e.unpacked {
			continue // stays in app.asar.unpacked
		}
		if data, ok := replacements[e.path]; ok {
			e.size = int64(len(data))
		}
		e.node.set("size", &node{kind: kindNumber, num: json.Number(strconv.FormatInt(e.size, 10))})
		e.node.set("offset", &node{kind: kindString, str: strconv.FormatInt(cursor, 10)})
		if data, ok := replacements[e.path]; ok {
			if integrity, ok := e.node.get("integrity"); ok && integrity.kind == kindObject {
				e.node.set("integrity", computeIntegrity(data))
			}
		}
		cursor += e.size
	}

	sizeBuf, headerBuf, err := buildHeaderBuffers(a.header)
	if err != nil {
		return nil, err
	}

	tempPath := filepath.Join(filepath.Dir(archivePath), fmt.Sprintf("app.asar.portable-%d.tmp", os.Getpid()))
	out, err := os.Create(tempPath)
	if err != nil {
		return nil, err
	}
	in, err := os.Open(archivePath)
	if err != nil {
		out.Close()
		return nil, err
	}
	writeErr := func() error {
		if _, err := out.Write(sizeBuf); err != nil {
			return err
		}
		if _, err := out.Write(headerBuf); err != nil {
			return err
		}
		totalFiles := len(a.files)
		for index, e := range a.files {
			if rebuildProgress != nil && index%64 == 0 && totalFiles > 0 {
				rebuildProgress(float64(index) / float64(totalFiles))
			}
			if e.unpacked {
				continue
			}
			if data, ok := replacements[e.path]; ok {
				if _, err := out.Write(data); err != nil {
					return err
				}
				continue
			}
			if e.size <= 0 {
				continue
			}
			if err := copyRange(in, out, a.dataOffset+e.origOffset, e.size); err != nil {
				return fmt.Errorf("复制 %s 失败: %w", e.path, err)
			}
		}
		if rebuildProgress != nil {
			rebuildProgress(1)
		}
		return nil
	}()
	in.Close()
	if writeErr != nil {
		out.Close()
		os.Remove(tempPath)
		return nil, writeErr
	}
	if err := out.Close(); err != nil {
		os.Remove(tempPath)
		return nil, err
	}
	stat, err := os.Stat(tempPath)
	if err != nil {
		return nil, err
	}
	log("       已写出 %.1f MB（临时文件）", float64(stat.Size())/1048576)

	log("[4/5] 校验新归档")
	check, err := readArchive(tempPath)
	if err != nil {
		os.Remove(tempPath)
		return nil, fmt.Errorf("校验失败: %w", err)
	}
	if check.byPath[mainBundle] == nil || check.byPath[bootArchive] == nil {
		os.Remove(tempPath)
		return nil, fmt.Errorf("校验失败：归档缺少关键文件")
	}
	verify, err := os.Open(tempPath)
	if err != nil {
		os.Remove(tempPath)
		return nil, err
	}
	bootData, err := check.readFileData(verify, check.byPath[bootArchive])
	verify.Close()
	if err != nil {
		os.Remove(tempPath)
		return nil, err
	}
	if sha256.Sum256(bootData) != sha256.Sum256(bootSource) {
		os.Remove(tempPath)
		return nil, fmt.Errorf("校验失败：便携引导内容不一致")
	}
	log("       %d 个文件，头部与文件表一致", len(check.files))

	log("[5/5] 替换原归档")
	report := &patchReport{
		Archive:      archivePath,
		Files:        len(a.files) - 1,
		NewFiles:     len(a.files),
		Bytes:        a.fileSize,
		NewBytes:     stat.Size(),
		Applied:      applied,
		BundleBytes:  len(patchedBytes),
		BootBytes:    len(bootSource),
		NewFileAdded: addedNewFile,
	}
	if keepBackup {
		backup := archivePath + ".orig"
		if err := copyFile(archivePath, backup); err != nil {
			os.Remove(tempPath)
			return nil, err
		}
		report.Backup = backup
		log("       原文件已备份为 %s", filepath.Base(backup))
	}
	if err := os.Rename(tempPath, archivePath); err != nil {
		if err := copyFile(tempPath, archivePath); err != nil {
			os.Remove(tempPath)
			return nil, err
		}
		os.Remove(tempPath)
	}
	return report, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------
func buildHeaderBuffers(header *node) ([]byte, []byte, error) {
	jsonBytes := []byte(header.marshal())
	payloadSize := 4 + align4(int64(len(jsonBytes)))
	headerBuf := make([]byte, 4+align4(int64(4+len(jsonBytes))))
	binary.LittleEndian.PutUint32(headerBuf[0:4], uint32(payloadSize))
	binary.LittleEndian.PutUint32(headerBuf[4:8], uint32(len(jsonBytes)))
	copy(headerBuf[8:], jsonBytes)

	sizeBuf := make([]byte, 8)
	binary.LittleEndian.PutUint32(sizeBuf[0:4], 4)
	binary.LittleEndian.PutUint32(sizeBuf[4:8], uint32(len(headerBuf)))
	return sizeBuf, headerBuf, nil
}

// ensureArchivePath creates (if needed) the file entry for an archive path and
// returns the entry node.
func ensureArchivePath(root *node, archivePath string) (*node, error) {
	parts := strings.Split(archivePath, "/")
	current := root
	for _, part := range parts[:len(parts)-1] {
		files, ok := current.get("files")
		if !ok || files.kind != kindObject {
			files = newObject()
			current.set("files", files)
		}
		child, ok := files.get(part)
		if !ok || child.kind != kindObject {
			child = newObject()
			files.set(part, child)
		}
		current = child
	}
	files, ok := current.get("files")
	if !ok || files.kind != kindObject {
		files = newObject()
		current.set("files", files)
	}
	name := parts[len(parts)-1]
	if existing, ok := files.get(name); ok {
		return existing, nil
	}
	entryNode := newObject()
	files.set(name, entryNode)
	return entryNode, nil
}

func copyRange(in *os.File, out *os.File, offset int64, length int64) error {
	buf := make([]byte, copyChunk)
	for length > 0 {
		wanted := int64(len(buf))
		if length < wanted {
			wanted = length
		}
		read, err := in.ReadAt(buf[:wanted], offset)
		if read > 0 {
			if _, werr := out.Write(buf[:read]); werr != nil {
				return werr
			}
			offset += int64(read)
			length -= int64(read)
		}
		if err != nil {
			if err == io.EOF && length == 0 {
				return nil
			}
			return err
		}
	}
	return nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
