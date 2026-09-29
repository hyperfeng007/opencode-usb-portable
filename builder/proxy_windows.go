//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// Reads the current user's Windows proxy settings from the registry:
//
//	HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings
//	  ProxyEnable (DWORD) 1 = on
//	  ProxyServer (SZ)    "127.0.0.1:7897" or "http=..;https=.."
const (
	hkeyCurrentUserBuild = 0x80000001
	rrfRtRegDword        = 0x00000010
	rrfRtRegSz           = 0x00000002
	rrfNoExpand          = 0x10000000
	internetSettingsReg  = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
)

var (
	advapi32b        = syscall.NewLazyDLL("advapi32.dll")
	procRegGetValueB = advapi32b.NewProc("RegGetValueW")
)

func utf16b(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

func regValue(subKey string, name string, asDword bool) (string, bool) {
	if asDword {
		var data uint32
		size := uint32(4)
		ret, _, _ := procRegGetValueB.Call(
			hkeyCurrentUserBuild,
			uintptr(unsafe.Pointer(utf16b(subKey))),
			uintptr(unsafe.Pointer(utf16b(name))),
			rrfRtRegDword|rrfNoExpand,
			0,
			uintptr(unsafe.Pointer(&data)),
			uintptr(unsafe.Pointer(&size)),
		)
		if ret != 0 {
			return "", false
		}
		if data == 0 {
			return "0", true
		}
		return "1", true
	}

	var size uint32
	ret, _, _ := procRegGetValueB.Call(
		hkeyCurrentUserBuild,
		uintptr(unsafe.Pointer(utf16b(subKey))),
		uintptr(unsafe.Pointer(utf16b(name))),
		rrfRtRegSz|rrfNoExpand,
		0, 0,
		uintptr(unsafe.Pointer(&size)),
	)
	if ret != 0 || size < 4 || size > 128*1024 {
		return "", false
	}
	buffer := make([]uint16, size/2+2)
	ret, _, _ = procRegGetValueB.Call(
		hkeyCurrentUserBuild,
		uintptr(unsafe.Pointer(utf16b(subKey))),
		uintptr(unsafe.Pointer(utf16b(name))),
		rrfRtRegSz|rrfNoExpand,
		0,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret != 0 {
		return "", false
	}
	return syscall.UTF16ToString(buffer), true
}

func systemProxy() string {
	enabled, ok := regValue(internetSettingsReg, "ProxyEnable", true)
	if !ok || enabled != "1" {
		return ""
	}
	server, ok := regValue(internetSettingsReg, "ProxyServer", false)
	if !ok || server == "" {
		return ""
	}
	return parseProxyString(server)
}
