//go:build windows

package main

// 片づけ：Windows のごみ箱へ移す（あとからごみ箱の「元に戻す」で戻せる）。完全削除はしない。
// cgo なしで shell32.dll の SHFileOperationW を呼ぶ。

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var (
	shell32          = syscall.NewLazyDLL("shell32.dll")
	procSHFileOpW    = shell32.NewProc("SHFileOperationW")
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procGetDriveType = kernel32.NewProc("GetDriveTypeW")
)

const (
	foDelete            = 0x0003
	fofSilent           = 0x0004
	fofNoConfirmation   = 0x0010
	fofAllowUndo        = 0x0040 // ごみ箱へ（元に戻せる）
	fofNoErrorUI        = 0x0400
	fofWantNukeWarning  = 0x4000 // ごみ箱に入らず完全に消えそうなときは、Windows が確かめる画面を出す（黙って完全削除しない）
	driveFixed          = 3
	trashPlaceForScreen = "Windowsのごみ箱"
)

// SHFILEOPSTRUCTW（64ビット：56バイト。Go の並びと C の並びが同じになる）
type shFileOpStruct struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

// moveToTrash: abs（ファイルまたはフォルダ）を Windows のごみ箱へ移す。testRoot は使わない（試験用の代わりの置き場は Windows 以外だけ）
func moveToTrash(abs, testRoot string) error {
	_ = testRoot
	if !filepath.IsAbs(abs) {
		return fmt.Errorf("場所が正しくありません")
	}
	vol := filepath.VolumeName(abs)
	if vol == "" || strings.HasPrefix(vol, `\\`) {
		// ネットワーク上の場所は、ごみ箱に入らずに完全に消えることがあるため移さない
		return fmt.Errorf("ネットワーク上の場所なので、ごみ箱へ移せません（完全に消えてしまうのを防ぐため、移していません）")
	}
	root, err := syscall.UTF16PtrFromString(vol + `\`)
	if err != nil {
		return fmt.Errorf("場所が正しくありません")
	}
	if t, _, _ := procGetDriveType.Call(uintptr(unsafe.Pointer(root))); t != driveFixed {
		// USBメモリなどは、ごみ箱がなく完全に消えるため移さない
		return fmt.Errorf("このパソコンの内蔵ドライブではないため、ごみ箱へ移せません（完全に消えてしまうのを防ぐため、移していません）")
	}
	from, err := syscall.UTF16FromString(abs)
	if err != nil {
		return fmt.Errorf("名前が正しくありません")
	}
	from = append(from, 0) // 二重 NUL 終端
	op := shFileOpStruct{
		wFunc:  foDelete,
		pFrom:  &from[0],
		fFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI | fofWantNukeWarning,
	}
	ret, _, _ := procSHFileOpW.Call(uintptr(unsafe.Pointer(&op)))
	runtime.KeepAlive(from)
	if ret != 0 {
		return fmt.Errorf("ごみ箱へ移せませんでした（コード %d）。ファイルを開いていたら閉じて、もう一度押してください", ret)
	}
	if op.fAnyOperationsAborted != 0 {
		return fmt.Errorf("途中で取りやめになりました。移していません")
	}
	if _, err := os.Lstat(abs); err == nil {
		return fmt.Errorf("ごみ箱へ移せませんでした。ファイルを開いていたら閉じて、もう一度押してください")
	}
	return nil
}
