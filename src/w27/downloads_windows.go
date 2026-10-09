//go:build windows

package main

// ダウンロードフォルダの場所：Windows の既知のフォルダ（FOLDERID_Downloads）を shell32.dll に聞く。cgo なし。
// 聞けなければ %USERPROFILE%\Downloads。

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	procSHGetKnownFolderPath = syscall.NewLazyDLL("shell32.dll").NewProc("SHGetKnownFolderPath")
	procCoTaskMemFree        = syscall.NewLazyDLL("ole32.dll").NewProc("CoTaskMemFree")
)

type winGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// FOLDERID_Downloads {374DE290-123F-4565-9164-39C4925E467B}
var folderIDDownloads = winGUID{0x374DE290, 0x123F, 0x4565, [8]byte{0x91, 0x64, 0x39, 0xC4, 0x92, 0x5E, 0x46, 0x7B}}

func knownDownloads() string {
	var out *uint16
	r, _, _ := procSHGetKnownFolderPath.Call(uintptr(unsafe.Pointer(&folderIDDownloads)), 0, 0, uintptr(unsafe.Pointer(&out)))
	if out == nil {
		return ""
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(out)))
	if r != 0 {
		return ""
	}
	// NUL までの長さを数えて読む
	n := 0
	for p := unsafe.Pointer(out); *(*uint16)(unsafe.Add(p, n*2)) != 0 && n < 32768; n++ {
	}
	return syscall.UTF16ToString(unsafe.Slice(out, n))
}

func platformDownloadsDir() string {
	if p := knownDownloads(); p != "" {
		return p
	}
	if h := os.Getenv("USERPROFILE"); h != "" {
		return filepath.Join(h, "Downloads")
	}
	return ""
}
