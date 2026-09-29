package main

import (
	_ "embed"
	"errors"
	"os"
	"path/filepath"
)

// The portable bootstrap is embedded into the builder, so the builder is a
// single self-contained executable.
//
//go:embed bootstrap.js
var bootstrapEmbedded []byte

// bootstrapSource returns the bootstrap that gets written into the patched
// app.asar. If a newer bootstrap.js sits next to the builder (or two levels up
// in the USB layout), that one wins, which makes it easy to update.
func bootstrapSource() ([]byte, error) {
	exe, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exe)
		candidates := []string{
			filepath.Join(exeDir, "bootstrap.js"),
			filepath.Join(exeDir, "..", "OpenCode-Portable.json"), // 仅用于探测 U 盘结构
			filepath.Join(exeDir, "..", "payload", "inject-bootstrap.js"),
		}
		for _, candidate := range candidates {
			if filepath.Base(candidate) != "bootstrap.js" && filepath.Base(candidate) != "inject-bootstrap.js" {
				continue
			}
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Size() > 512 {
				data, err := os.ReadFile(candidate)
				if err == nil && len(data) > 512 {
					return data, nil
				}
			}
		}
	}
	if len(bootstrapEmbedded) == 0 {
		return nil, errors.New("内置便携引导缺失")
	}
	return bootstrapEmbedded, nil
}
