//go:build !windows

package main

// Windows 以外（試験用の Linux など）では、キーは送らずに案内だけ返す
func sendWinH() (bool, string) {
	return false, "Windowsキー＋H でも声で入力できます"
}
