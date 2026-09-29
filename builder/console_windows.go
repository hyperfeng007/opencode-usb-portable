//go:build windows

package main

// Make the builder's own console render UTF-8 correctly, so it works even when
// it is launched directly (without the .cmd wrapper that runs "chcp 65001").
func initConsoleCodepage() {
	if kernel32.NewProc("SetConsoleOutputCP").Find() == nil {
		return
	}
	kernel32.NewProc("SetConsoleOutputCP").Call(65001)
	kernel32.NewProc("SetConsoleCP").Call(65001)
}
