//go:build !windows

package main

import (
	"fmt"
	"syscall"
)

func freeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

func checkFreeSpace(path string) error {
	free, err := freeBytes(path)
	if err != nil {
		return nil // 无法检测时不阻止构建
	}
	required := uint64(requiredFreeGB * 1024 * 1024 * 1024)
	if free < required {
		return fmt.Errorf("可用空间不足：需要约 %.1f GB，当前只有 %.1f GB",
			requiredFreeGB, float64(free)/1073741824)
	}
	return nil
}
