//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"unsafe"
)

// STD_INPUT_HANDLE (-10) as a DWORD.
const stdInputHandle = 0xFFFFFFF6

// stdinIsConsole reports whether standard input is a real console, i.e. the
// builder was started with its own console window.
func stdinIsConsole() bool {
	var mode uint32
	ret, _, _ := kernel32.NewProc("GetConsoleMode").Call(
		uintptr(stdInputHandle),
		uintptr(unsafe.Pointer(&mode)),
	)
	return ret != 0
}

// waitForUser keeps the console window open so a failure cannot flash away.
func waitForUser() {
	if !stdinIsConsole() {
		return
	}
	fmt.Print("\r\n按回车键关闭此窗口 ... ")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
