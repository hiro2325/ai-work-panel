//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

// platformDownloadsDir: Windows 以外（試験用）：ホームの Downloads
func platformDownloadsDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, "Downloads")
}
