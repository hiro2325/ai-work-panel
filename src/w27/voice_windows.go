//go:build windows

package main

// 「声で入力」：Windows の音声入力（Windowsキー＋H）を立ち上げる。cgo なしで user32.dll を呼ぶ。

import (
	"syscall"
	"time"
	"unsafe"
)

var (
	user32         = syscall.NewLazyDLL("user32.dll")
	procSendInput  = user32.NewProc("SendInput")
	procKeybdEvent = user32.NewProc("keybd_event")
)

const (
	vkLWin         = 0x5B
	vkH            = 0x48
	keyeventfExt   = 0x0001
	keyeventfKeyUp = 0x0002
	inputKeyboard  = 1
)

// keyInput は INPUT（type＋KEYBDINPUT）。64ビットでは 40 バイト
type keyInput struct {
	typ   uint32
	_     uint32
	vk    uint16
	scan  uint16
	flags uint32
	time  uint32
	_     uint32
	extra uintptr
	_     [8]byte
}

func sendWinH() (bool, string) {
	// ブラウザが前面のまま、題名欄にフォーカスが戻るのを少し待つ
	time.Sleep(350 * time.Millisecond)
	in := []keyInput{
		{typ: inputKeyboard, vk: vkLWin, flags: keyeventfExt},
		{typ: inputKeyboard, vk: vkH},
		{typ: inputKeyboard, vk: vkH, flags: keyeventfKeyUp},
		{typ: inputKeyboard, vk: vkLWin, flags: keyeventfExt | keyeventfKeyUp},
	}
	if err := procSendInput.Find(); err == nil {
		n, _, _ := procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0]))
		if int(n) == len(in) {
			return true, ""
		}
	}
	// 予備：keybd_event
	if err := procKeybdEvent.Find(); err != nil {
		return false, "Windowsキー＋H でも声で入力できます"
	}
	procKeybdEvent.Call(vkLWin, 0, keyeventfExt, 0)
	procKeybdEvent.Call(vkH, 0, 0, 0)
	procKeybdEvent.Call(vkH, 0, keyeventfKeyUp, 0)
	procKeybdEvent.Call(vkLWin, 0, keyeventfExt|keyeventfKeyUp, 0)
	return true, ""
}
