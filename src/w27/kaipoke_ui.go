package main

// カイポケのCSVをパネルの画面から入れる（改善20・R8.10.3）
//   - 入れ先：作業フォルダ（マスキング作業）＼カイポケCSV。フォルダが無ければ作る。
//   - 入れてよいのは、見出しで「利用者情報」か「認定情報」と分かる .csv だけ。分からないものは入れない。
//   - 同じ中身のCSVがすでにあれば入れない。同じ名前があれば (2) を付ける。上書きしない・消さない。
//   - 画面には、ファイル名・種類・更新日・人数（行数）だけを出す。中身（名前など）は出さない。

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// kpKindOf: CSVの中身から種類を見分ける（"利用者情報"・"認定情報"・""）。rows は見出しを除いた行数
func kpKindOf(b []byte) (kind string, rows int, why string) {
	text, _ := decodeKaipoke(b)
	recs := parseKpCSV(text)
	if len(recs) == 0 {
		return "", 0, "中身が空か、CSVとして読めません"
	}
	k1, why1 := classifyKp1(recs[0])
	if k1 != nil {
		return "利用者情報", len(k1.rows(recs[1:])), ""
	}
	k2, why2 := classifyKp2(recs[0])
	if k2 != nil {
		return "認定情報", len(k2.rows(recs[1:])), ""
	}
	w := strings.TrimSpace(strings.Trim(why1+" ／ "+why2, " ／"))
	if w == "" {
		w = "見出しが、カイポケの利用者情報・認定情報のどちらとも合いません"
	}
	return "", 0, w
}

func (s *panelServer) kaipokeDir() string { return filepath.Join(s.p.work, kaipokeCSVDir) }

// /api/kaipoke/status：いま置いてあるCSVの一覧と、第1表に使うCSV
func (s *panelServer) kaipokeStatus(r *http.Request) (any, error) {
	dir := s.kaipokeDir()
	type row struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
		Rows int    `json:"rows"`
		At   string `json:"at"`
		Used bool   `json:"used"`
		Why  string `json:"why,omitempty"`
	}
	out := []row{}
	ents, err := os.ReadDir(dir)
	exists := err == nil
	type fi struct {
		name string
		st   os.FileInfo
	}
	var fs []fi
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, "~$") || strings.HasPrefix(n, ".") || !strings.EqualFold(filepath.Ext(n), ".csv") {
			continue
		}
		st, err := os.Stat(filepath.Join(dir, n))
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		fs = append(fs, fi{n, st})
	}
	sort.Slice(fs, func(a, b int) bool { return fs[a].st.ModTime().After(fs[b].st.ModTime()) })
	d := loadKaipoke(dir)
	for _, f := range fs {
		x := row{Name: f.name, At: fmtTime(f.st.ModTime())}
		if f.st.Size() > 64<<20 {
			x.Why = "大きすぎて読みません"
		} else if b, err := os.ReadFile(filepath.Join(dir, f.name)); err != nil {
			x.Why = "読めません（Excelなどで開いていたら閉じてください）"
		} else {
			x.Kind, x.Rows, x.Why = kpKindOf(b)
		}
		x.Used = f.name == d.File1 || f.name == d.File2
		out = append(out, x)
	}
	return map[string]any{"exists": exists, "files": out, "file1": d.File1, "file2": d.File2,
		"problems1": d.Problems1, "problems2": d.Problems2, "notes": d.Notes}, nil
}

// /api/kaipoke/upload：CSVを カイポケCSV に入れる
func (s *panelServer) kaipokeUpload(r *http.Request) (any, error) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		return nil, fmt.Errorf("ファイルを受け取れませんでした: %v", err)
	}
	dir := s.kaipokeDir()
	type res struct {
		Name string `json:"name"`
		Kind string `json:"kind,omitempty"`
		Why  string `json:"why,omitempty"`
	}
	saved, rejected, dup := []res{}, []res{}, []string{}
	for _, fh := range r.MultipartForm.File["files"] {
		name := filepath.Base(strings.ReplaceAll(fh.Filename, "\\", "/"))
		if !strings.EqualFold(filepath.Ext(name), ".csv") || !safeName(name) || strings.HasPrefix(name, "~$") {
			rejected = append(rejected, res{Name: name, Why: "CSV（.csv）ではありません"})
			continue
		}
		f, err := fh.Open()
		if err != nil {
			rejected = append(rejected, res{Name: name, Why: "読めませんでした"})
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, 64<<20+1))
		f.Close()
		if err != nil || len(data) > 64<<20 {
			rejected = append(rejected, res{Name: name, Why: "読めないか、大きすぎます"})
			continue
		}
		kind, _, why := kpKindOf(data)
		if kind == "" {
			rejected = append(rejected, res{Name: name, Why: why})
			continue
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("「%s」フォルダを作れませんでした: %v", kaipokeCSVDir, err)
		}
		if sameFileIn(dir, data) {
			dup = append(dup, name)
			continue
		}
		dst := uniquePath(dir, name)
		if err := writeNew(dst, data); err != nil {
			rejected = append(rejected, res{Name: name, Why: "書けませんでした"})
			continue
		}
		saved = append(saved, res{Name: filepath.Base(dst), Kind: kind})
	}
	return map[string]any{"saved": saved, "rejected": rejected, "dup": dup}, nil
}

// /api/kaipoke/trash：入れたCSVを取り消す（Windows のごみ箱へ移す。消さない。改善27の2・注文）
func (s *panelServer) kaipokeTrash(r *http.Request) (any, error) {
	var q struct{ Name string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	n := q.Name
	if n == "" || n != filepath.Base(n) || strings.HasPrefix(n, ".") || !strings.EqualFold(filepath.Ext(n), ".csv") {
		return nil, fmt.Errorf("ファイルの名前が正しくありません")
	}
	abs := filepath.Join(s.kaipokeDir(), n)
	st, err := os.Lstat(abs)
	if err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("「%s」が カイポケCSV フォルダに見つかりません。画面を開き直してください", n)
	}
	if err := moveToTrash(abs, s.p.work); err != nil {
		return nil, err
	}
	return map[string]string{"ok": "「" + n + "」を取り消しました（ごみ箱へ移しました。戻すときは、ごみ箱から元に戻せます）"}, nil
}
