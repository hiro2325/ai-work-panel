package main

// 試し用の書類（samples）を ①資料を入れる の画面に出す。
// 環境変数 MASKTOOL_SAMPLES_DIR があるとき（run_source.bat から起動したとき）だけ出す。ふだんの exe には出ない。
// 読むのは samples フォルダのすぐ下の .xlsx・.docx・.pdf・.txt だけ。1_元データ へは画面から /api/upload で入れる（今までのドラッグと同じ受け取り口）。

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const sampleMaxBytes = 16 << 20

func samplesDir() string {
	v := os.Getenv("MASKTOOL_SAMPLES_DIR")
	if v == "" {
		return ""
	}
	if st, err := os.Stat(v); err != nil || !st.IsDir() {
		return ""
	}
	return v
}

func sampleExtOK(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".xlsx", ".docx", ".pdf", ".txt":
		return true
	}
	return false
}

// sampleList: 画面に出す試し用の書類の名前（samples がなければ空）
func sampleList() []string {
	out := []string{}
	dir := samplesDir()
	if dir == "" {
		return out
	}
	for _, n := range listFiles(dir) {
		if sampleExtOK(n) {
			out = append(out, n)
		}
	}
	return out
}

// /api/samples/file：試し用の書類の中身。.txt は文字のまま、ほかは base64 で返す
func (s *panelServer) sampleFile(r *http.Request) (any, error) {
	var q struct{ Name string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	ok := false
	for _, n := range sampleList() {
		if n == q.Name {
			ok = true
		}
	}
	if !ok {
		return nil, fmt.Errorf("試し用の書類が見つかりません：%s", q.Name)
	}
	p := filepath.Join(samplesDir(), q.Name)
	st, err := os.Stat(p)
	if err != nil || st.Size() > sampleMaxBytes {
		return nil, fmt.Errorf("試し用の書類を読めませんでした：%s", q.Name)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("試し用の書類を読めませんでした：%s", q.Name)
	}
	if strings.EqualFold(filepath.Ext(q.Name), ".txt") {
		t := strings.ReplaceAll(strings.TrimPrefix(string(data), bom), "\r\n", "\n")
		return map[string]string{"name": q.Name, "text": t}, nil
	}
	return map[string]string{"name": q.Name, "b64": base64.StdEncoding.EncodeToString(data)}, nil
}
