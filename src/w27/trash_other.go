//go:build !windows

package main

// 片づけ（Windows 以外＝試験用）：ごみ箱の代わりに、作業フォルダの中の「.試験用ごみ箱」へ移す。消さない。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const trashPlaceForScreen = "試験用のごみ箱（.試験用ごみ箱）"

func moveToTrash(abs, testRoot string) error {
	if !filepath.IsAbs(abs) {
		return fmt.Errorf("場所が正しくありません")
	}
	// 試験用：名前に MASKTOOL_TEST_TRASH_FAIL の文字を含むものは「移せなかった」ことにする
	if f := os.Getenv("MASKTOOL_TEST_TRASH_FAIL"); f != "" && strings.Contains(filepath.Base(abs), f) {
		return fmt.Errorf("ごみ箱へ移せませんでした（試験用の失敗）。ファイルを開いていたら閉じて、もう一度押してください")
	}
	dir := filepath.Join(testRoot, ".試験用ごみ箱", time.Now().Format("20060102_150405.000000000"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("ごみ箱へ移せませんでした: %v", err)
	}
	if err := os.Rename(abs, filepath.Join(dir, filepath.Base(abs))); err != nil {
		return fmt.Errorf("ごみ箱へ移せませんでした。ファイルを開いていたら閉じて、もう一度押してください")
	}
	return nil
}
