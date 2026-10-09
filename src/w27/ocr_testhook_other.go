//go:build !windows

package main

// 試験用（Windows 以外だけ）：Windows の文字読み取りの代わりに、PDF の中身を「読み取り結果の文」として読む。
// MASKTOOL_TEST_OCR=1 のときだけ効く。ふだん使うパソコン（Windows）の exe には入らない。

import "os"

func init() {
	if os.Getenv("MASKTOOL_TEST_OCR") == "" {
		return
	}
	runOCR = func(pdfPath, workDir string) ([]ocrPage, error) {
		b, err := os.ReadFile(pdfPath)
		if err != nil {
			return nil, err
		}
		return parseOCROutput(string(b)), nil
	}
}
