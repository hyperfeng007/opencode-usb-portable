//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// Direct registry fallback for the Windows system proxy.
//
// HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings
//
//	ProxyEnable  (DWORD) 1 = system proxy is switched on
//	ProxyServer  (SZ)    "127.0.0.1:7897" or "http=127.0.0.1:7897;https=..."
//	ProxyBypass  (SZ)    "<local>;*.internal"
//
// This is exactly what the Windows Settings app writes, and what Clash,
// v2rayN, sing-box, Proxifier, Fiddler and friends configure, so it is the
// most dependable source. The WinINET / WinHTTP APIs are tried first; the
// registry is the fallback when those return nothing.
const (
	hkeyCurrentUser = 0x80000001
	rrfRtRegDword   = 0x00000010
	rrfRtRegSz      = 0x00000002
	rrfNoExpand     = 0x10000000

	internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
)

var (
	advapi32        = syscall.NewLazyDLL("advapi32.dll")
	procRegGetValue = advapi32.NewProc("RegGetValueW")
)

func regGetDword(subKey string, value string) (uint32, bool) {
	var data uint32
	size := uint32(4)
	ret, _, _ := procRegGetValue.Call(
		hkeyCurrentUser,
		uintptr(unsafe.Pointer(utf16Ptr(subKey))),
		uintptr(unsafe.Pointer(utf16Ptr(value))),
		rrfRtRegDword|rrfNoExpand,
		0,
		uintptr(unsafe.Pointer(&data)),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret != 0 {
		return 0, false
	}
	return data, true
}

func regGetString(subKey string, value string) (string, bool) {
	var size uint32
	ret, _, _ := procRegGetValue.Call(
		hkeyCurrentUser,
		uintptr(unsafe.Pointer(utf16Ptr(subKey))),
		uintptr(unsafe.Pointer(utf16Ptr(value))),
		rrfRtRegSz|rrfNoExpand,
		0,
		0,
		uintptr(unsafe.Pointer(&size)),
	)
	if ret != 0 || size < 4 || size > 128*1024 {
		return "", false
	}
	buf := make([]uint16, size/2+2)
	ret, _, _ = procRegGetValue.Call(
		hkeyCurrentUser,
		uintptr(unsafe.Pointer(utf16Ptr(subKey))),
		uintptr(unsafe.Pointer(utf16Ptr(value))),
		rrfRtRegSz|rrfNoExpand,
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret != 0 {
		return "", false
	}
	return syscall.UTF16ToString(buf), true
}

// registryProxy reports the proxy configured by the current user, but only
// when the system proxy switch is actually on.
func registryProxy() (proxy string, bypass string, ok bool) {
	enabled, found := regGetDword(internetSettingsKey, "ProxyEnable")
	if !found || enabled == 0 {
		return "", "", false
	}
	server, found := regGetString(internetSettingsKey, "ProxyServer")
	if !found || server == "" {
		return "", "", false
	}
	bypass, _ = regGetString(internetSettingsKey, "ProxyBypass")
	return server, bypass, true
}
