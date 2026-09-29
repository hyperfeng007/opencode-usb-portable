//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceX = kernel32.NewProc("GetDiskFreeSpaceExW")
)

func freeBytes(path string) (uint64, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	pointer, err := syscall.UTF16PtrFromString(absolute)
	if err != nil {
		return 0, err
	}
	var freeToCaller, total, totalFree uint64
	ret, _, callErr := procGetDiskFreeSpaceX.Call(
		uintptr(unsafe.Pointer(pointer)),
		uintptr(unsafe.Pointer(&freeToCaller)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if ret == 0 {
		return 0, callErr
	}
	return freeToCaller, nil
}

func checkFreeSpace(path string) error {
	free, err := freeBytes(path)
	if err != nil {
		return nil // 检测失败不阻止构建
	}
	required := uint64(requiredFreeGB * 1024 * 1024 * 1024)
	if free < required {
		return fmt.Errorf("U 盘可用空间不足：需要约 %.1f GB，当前只有 %.1f GB。\n"+
			"提示：构建过程需要 安装包(约120MB) + 解压后的应用(约500MB) + 补丁临时文件(约150MB)",
			requiredFreeGB, float64(free)/1073741824)
	}
	return nil
}
