//go:build windows

package main

// A small native progress window ("进度条").
//
// The launcher is a GUI-subsystem program, so without it a first-time install
// would be a black box: the user double-clicks and something happens somewhere
// for a few minutes. This window shows what is going on right now - checking
// the environment, downloading opencode, unpacking, patching, rebuilding the
// archive, starting opencode - and closes by itself as soon as the opencode
// window appears.
//
// Only plain Win32 (user32 / comctl32 / gdi32) is used, no cgo, so the binary
// stays self-contained and leaves nothing behind on the host.

import (
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	wmCreate         = 0x0001
	wmDestroy        = 0x0002
	wmClose          = 0x0010
	wmSetFont        = 0x0030
	wmTimer          = 0x0113
	wmCtlColorStatic = 0x0138
	wmAppClose       = 0x8000 + 1 // our own "close the window" message

	pbmSetRange = 0x0401
	pbmSetPos   = 0x0402
	pbmSetState = 0x0410

	pbstNormal = 1
	pbstError  = 2

	iccProgressClass = 0x00000020

	wsOverlapped   = 0x00000000
	wsCaption      = 0x00C00000
	wsSysMenu      = 0x00080000
	wsMinimizeBox  = 0x00020000
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsTabStop      = 0x00010000
	wsExTopmost    = 0x00000008
	swShow         = 5
	smCxScreen     = 0
	smCyScreen     = 1
	defaultGuiFont = 17
	transparent    = 1
)

var (
	comctl32b = syscall.NewLazyDLL("comctl32.dll")
	gdi32b    = syscall.NewLazyDLL("gdi32.dll")

	procInitCommonControlsEx = comctl32b.NewProc("InitCommonControlsEx")
	procCreateFontW          = gdi32b.NewProc("CreateFontW")
	procCreateSolidBrush     = gdi32b.NewProc("CreateSolidBrush")
	procSetBkMode            = gdi32b.NewProc("SetBkMode")
	procSetTextColor         = gdi32b.NewProc("SetTextColor")

	procRegisterClassEx       = user32.NewProc("RegisterClassExW")
	procCreateWindowEx        = user32.NewProc("CreateWindowExW")
	procDefWindowProc         = user32.NewProc("DefWindowProcW")
	procDestroyWindow         = user32.NewProc("DestroyWindow")
	procShowWindow            = user32.NewProc("ShowWindow")
	procUpdateWindow          = user32.NewProc("UpdateWindow")
	procGetMessage            = user32.NewProc("GetMessageW")
	procTranslateMessage      = user32.NewProc("TranslateMessage")
	procDispatchMessage       = user32.NewProc("DispatchMessageW")
	procPostQuitMessage       = user32.NewProc("PostQuitMessage")
	procPostMessage           = user32.NewProc("PostMessageW")
	procSendMessage           = user32.NewProc("SendMessageW")
	procSetWindowText         = user32.NewProc("SetWindowTextW")
	procSetTimer              = user32.NewProc("SetTimer")
	procGetSystemMetrics      = user32.NewProc("GetSystemMetrics")
	procLoadCursor            = user32.NewProc("LoadCursorW")
	procGetModuleHandle       = kernel32.NewProc("GetModuleHandleW")
	procEnumWindows           = user32.NewProc("EnumWindows")
	procGetWindowThreadProcID = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible       = user32.NewProc("IsWindowVisible")
	procGetClassNameW         = user32.NewProc("GetClassNameW")
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type msgT struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	ptX     int32
	ptY     int32
}

type initCommonControlsEx struct {
	dwSize uint32
	dwICC  uint32
}

func rgb(r, g, b uint32) uintptr {
	return uintptr(r | g<<8 | b<<16)
}

func u16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

// ---------------------------------------------------------------------------
// progress window
// ---------------------------------------------------------------------------

type progressUI struct {
	mu      sync.Mutex
	status  string
	percent int // 0..100
	auto    bool
	err     bool
	hwnd    uintptr
	hStatus uintptr
	hBar    uintptr
	hHint   uintptr
	fontBig uintptr
	fontMid uintptr

	brushBg uintptr

	ready  chan struct{}
	closed chan struct{}
}

var (
	uiOnce    sync.Once
	uiClass   = u16("OpenCodePortableProgress")
	uiWndProc = syscall.NewCallback(progressWndProc)
	activeUI  *progressUI
)

// startProgress creates the window and returns immediately; the window lives on
// its own locked OS thread with a normal message pump.
func startProgress(hint string) *progressUI {
	ui := &progressUI{
		status:  "正在检查运行环境 ...",
		auto:    true,
		brushBg: 0,
		ready:   make(chan struct{}),
		closed:  make(chan struct{}),
	}
	activeUI = ui
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		ui.run(hint)
	}()
	select {
	case <-ui.ready:
	case <-time.After(3 * time.Second):
	}
	return ui
}

func (ui *progressUI) run(hint string) {
	icc := initCommonControlsEx{dwSize: uint32(unsafe.Sizeof(initCommonControlsEx{})), dwICC: iccProgressClass}
	procInitCommonControlsEx.Call(uintptr(unsafe.Pointer(&icc)))

	ui.brushBg, _, _ = procCreateSolidBrush.Call(rgb(250, 250, 250))
	hInstance, _, _ := procGetModuleHandle.Call(0)

	uiOnce.Do(func() {
		class := wndClassExW{
			cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
			lpfnWndProc:   uiWndProc,
			hInstance:     hInstance,
			hCursor:       loadCursor(0, 32512), // IDC_ARROW
			hbrBackground: ui.brushBg,
			lpszClassName: uiClass,
		}
		procRegisterClassEx.Call(uintptr(unsafe.Pointer(&class)))
	})

	const width, height = 470, 176
	screenW, _, _ := procGetSystemMetrics.Call(smCxScreen)
	screenH, _, _ := procGetSystemMetrics.Call(smCyScreen)
	x := (int(screenW) - width) / 2
	y := (int(screenH) - height) / 2

	style := uintptr(wsOverlapped | wsCaption | wsSysMenu | wsMinimizeBox)
	hwnd, _, _ := procCreateWindowEx.Call(
		wsExTopmost,
		uintptr(unsafe.Pointer(uiClass)),
		uintptr(unsafe.Pointer(u16("OpenCode 便携版"))),
		style,
		uintptr(int32(x)), uintptr(int32(y)), width, height,
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		close(ui.ready)
		close(ui.closed)
		return
	}
	ui.hwnd = hwnd

	ui.fontBig = makeFont(13, 700)
	ui.fontMid = makeFont(9, 400)

	ui.hStatus, _, _ = procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(u16("STATIC"))), uintptr(unsafe.Pointer(u16(ui.status))),
		wsChild|wsVisible, 20, 16, width-40, 26, hwnd, 0, hInstance, 0)
	ui.hBar, _, _ = procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(u16("msctls_progress32"))), 0,
		wsChild|wsVisible, 20, 52, width-40, 22, hwnd, 0, hInstance, 0)
	ui.hHint, _, _ = procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(u16("STATIC"))), uintptr(unsafe.Pointer(u16(hint))),
		wsChild|wsVisible, 20, 84, width-40, 60, hwnd, 0, hInstance, 0)

	if ui.fontBig != 0 {
		procSendMessage.Call(ui.hStatus, wmSetFont, ui.fontBig, 1)
	}
	if ui.fontMid != 0 {
		procSendMessage.Call(ui.hHint, wmSetFont, ui.fontMid, 1)
	}
	procSendMessage.Call(ui.hBar, pbmSetRange, 0, 100)
	procSendMessage.Call(ui.hBar, pbmSetPos, 0, 0)

	procSetTimer.Call(hwnd, 1, 120, 0)
	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)
	close(ui.ready)

	var message msgT
	for {
		ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
	}
	close(ui.closed)
}

func makeFont(size int, weight int) uintptr {
	// "Microsoft YaHei UI" is present on every Windows 10/11; Wine falls back
	// to whatever it has. A null handle simply means "use the default font".
	font, _, _ := procCreateFontW.Call(
		uintptr(int32(-size)),
		0, 0, 0,
		uintptr(int32(weight)),
		0, 0, 0,
		134, // GB2312_CHARSET
		0, 0, 5, 0,
		uintptr(unsafe.Pointer(u16("Microsoft YaHei UI"))),
	)
	return font
}

func loadCursor(instance uintptr, name uintptr) uintptr {
	handle, _, _ := procLoadCursor.Call(instance, name)
	return handle
}

func progressWndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	ui := activeUI
	switch message {
	case wmTimer:
		if ui != nil {
			ui.apply()
		}
		return 0
	case wmCtlColorStatic:
		if ui != nil {
			procSetBkMode.Call(wParam, transparent)
			procSetTextColor.Call(wParam, rgb(0x33, 0x33, 0x33))
			return ui.brushBg
		}
	case wmAppClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmClose:
		// keep the window alive: the work continues in the background
		return 0
	}
	ret, _, _ := procDefWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
	return ret
}

// apply pushes the pending state into the controls (UI thread only).
func (ui *progressUI) apply() {
	ui.mu.Lock()
	status, percent, auto, failed := ui.status, ui.percent, ui.auto, ui.err
	ui.mu.Unlock()

	if auto && !failed {
		// no exact number available: creep forward so the bar still moves
		if percent < 90 {
			percent += 1
			ui.mu.Lock()
			ui.percent = percent
			ui.mu.Unlock()
		}
	}
	if percent > 100 {
		percent = 100
	}
	procSendMessage.Call(ui.hBar, pbmSetPos, uintptr(percent), 0)
	if failed {
		procSendMessage.Call(ui.hBar, pbmSetState, pbstError, 0)
	} else {
		procSendMessage.Call(ui.hBar, pbmSetState, pbstNormal, 0)
	}
	text := status
	if !failed && percent > 0 {
		text = status
	} else if failed {
		text = status
	}
	procSetWindowText.Call(ui.hStatus, uintptr(unsafe.Pointer(u16(text))))
}

// SetStatus updates the text and (when percent >= 0) the progress position.
func (ui *progressUI) SetStatus(status string, percent int) {
	if ui == nil {
		return
	}
	ui.mu.Lock()
	ui.status = status
	if percent >= 0 {
		ui.percent = percent
		ui.auto = false
	} else {
		ui.auto = true
	}
	ui.mu.Unlock()
}

// SetHint replaces the small grey help text under the bar.
func (ui *progressUI) SetHint(hint string) {
	if ui == nil || ui.hHint == 0 {
		return
	}
	procSetWindowText.Call(ui.hHint, uintptr(unsafe.Pointer(u16(hint))))
}

// Fail paints the bar red and shows the reason before the dialog appears.
func (ui *progressUI) Fail(reason string) {
	if ui == nil {
		return
	}
	ui.mu.Lock()
	ui.status = "安装失败：" + reason
	ui.err = true
	ui.auto = false
	ui.mu.Unlock()
	time.Sleep(400 * time.Millisecond)
}

// Close removes the window.
func (ui *progressUI) Close() {
	if ui == nil || ui.hwnd == 0 {
		return
	}
	procPostMessage.Call(ui.hwnd, wmAppClose, 0, 0)
	select {
	case <-ui.closed:
	case <-time.After(1500 * time.Millisecond):
	}
}

// ---------------------------------------------------------------------------
// "has opencode opened its window yet?"
// ---------------------------------------------------------------------------

type appWaitResult int

const (
	appWindowUp appWaitResult = iota
	appExited
	appWindowTimeout
)

// waitForAppOrExit waits until opencode shows a window, until the process dies,
// or until the timeout expires.
func waitForAppOrExit(pid int, timeout time.Duration) appWaitResult {
	baseline := electronWindows()
	deadline := time.Now().Add(timeout)
	goneChecks := 0
	for {
		if appWindowVisible(pid) {
			return appWindowUp
		}
		for hwnd := range electronWindows() {
			if _, seen := baseline[hwnd]; !seen {
				return appWindowUp
			}
		}
		if pid > 0 && !processAlive(pid) {
			goneChecks++
			if goneChecks >= 3 { // ~1s of grace, the window may still be coming
				return appExited
			}
		}
		if time.Now().After(deadline) {
			return appWindowTimeout
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// waitForAppWindow blocks until opencode has a real window on screen, or until
// the timeout expires. Two independent signals are accepted:
//
//  1. a visible top-level window owned by the process we started, or
//  2. a visible Electron window ("Chrome_WidgetWin_1") that was NOT there
//     before the app was started - this also covers the case where the window
//     belongs to an Electron helper process instead of the main process.
func waitForAppWindow(pid int, timeout time.Duration) bool {
	baseline := electronWindows()
	deadline := time.Now().Add(timeout)
	for {
		if appWindowVisible(pid) {
			return true
		}
		for hwnd := range electronWindows() {
			if _, seen := baseline[hwnd]; !seen {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// electronWindows lists the visible Electron top-level windows currently on
// screen, used as a "was it already there?" baseline.
func electronWindows() map[uintptr]bool {
	windows := map[uintptr]bool{}
	callback := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		visible, _, _ := procIsWindowVisible.Call(hwnd)
		if visible != 0 && windowClassIs(hwnd, "Chrome_WidgetWin_1") {
			windows[hwnd] = true
		}
		return 1
	})
	procEnumWindows.Call(callback, 0)
	return windows
}

func appWindowVisible(pid int) bool {
	found := false
	callback := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		visible, _, _ := procIsWindowVisible.Call(hwnd)
		if visible == 0 {
			return 1
		}
		var owner uint32
		procGetWindowThreadProcID.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
		if pid > 0 && int(owner) == pid {
			found = true
			return 0
		}
		if pid <= 0 && windowClassIs(hwnd, "Chrome_WidgetWin_1") {
			found = true
			return 0
		}
		return 1
	})
	procEnumWindows.Call(callback, 0)
	return found
}

func windowClassIs(hwnd uintptr, want string) bool {
	buffer := make([]uint16, 256)
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if n == 0 {
		return false
	}
	return syscall.UTF16ToString(buffer[:n]) == want
}
