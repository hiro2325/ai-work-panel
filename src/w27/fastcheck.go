package main

// 開くときの速さ（改善21・R8.10.5）：⑥の書類が「支援経過（1日分）」「第1表」かを調べるのを軽くする
//   1. 先に workbook.xml だけを読んでシートの数を数え、形の合わないブック（ケアプランなど、シートの多いもの）は中身を開かない
//   2. 調べた結果を、ファイルの場所・大きさ・更新日時ごとに覚えておき、変わっていなければ調べ直さない

import (
	"archive/zip"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var reFastSheet = regexp.MustCompile(`<sheet[\s/>]`)

// wbSheetCount: xlsx の xl/workbook.xml だけを読んで <sheet> の数を返す。読めなければ -1
func wbSheetCount(path string) int {
	z, err := zip.OpenReader(path)
	if err != nil {
		return -1
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name != "xl/workbook.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return -1
		}
		b, err := io.ReadAll(io.LimitReader(rc, 4<<20))
		rc.Close()
		if err != nil {
			return -1
		}
		return len(reFastSheet.FindAllIndex(b, -1))
	}
	return -1
}

type kindKey struct {
	path, what string
	size       int64
	mod        time.Time
}

var kindCache sync.Map // kindKey → bool

// cachedKind: 同じファイル（大きさ・更新日時が同じ）なら前の結果を使う
// f は (結果, 覚えてよいか) を返す。読めなかったとき（ロック中など）は覚えずに、次にまた調べる
func cachedKind(path, what string, f func() (bool, bool)) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	k := kindKey{path, what, st.Size(), st.ModTime()}
	if v, ok := kindCache.Load(k); ok {
		return v.(bool)
	}
	r, keep := f()
	if keep {
		kindCache.Store(k, r)
	}
	return r
}

// canOpenZip: zip として開けるか（開けなければ一時的なロックなどとみて、結果を覚えない）
func canOpenZip(path string) bool {
	z, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	z.Close()
	return true
}

// wbHasSheet: ブックに、この名前のシートがあるか（workbook.xml だけ読む。改善27の2：第1表を1冊に入れたブック）
func wbHasSheet(path, name string) bool {
	z, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name != "xl/workbook.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return false
		}
		b, err := io.ReadAll(io.LimitReader(rc, 4<<20))
		rc.Close()
		if err != nil {
			return false
		}
		return strings.Contains(string(b), `name="`+name+`"`)
	}
	return false
}
