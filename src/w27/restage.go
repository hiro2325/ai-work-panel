package main

// 置き換え直しと最後の関所（現場試験 S2・R8.10.1）
//
//   - restage：「置き換える」「まとめて置き換え直す」のたびに、渡す前のファイル（3_確認待ち）も今の氏名一覧で置き換え直す。
//     控え（片づけの紐付け）の「元データのハッシュ → 置き換え済みのハッシュ」から元データ（1_元データ＼処理済み）を見つけて
//     1_元データ に戻し、置き換え済みの写しは外す（元データから作り直せる写しなので、写しだけを外す）。
//     外した写しは消さずに マスキング作業＼_置き換え直し前の写し＼3_確認待ち＼ へよける（同じ名前は1世代だけ）。
//     元データが見つからない写しはそのまま（渡すときの最後の関所で確かめる）。
//   - finalGate：③「AIに渡す」を押したとき、渡すファイル全部の文字を今の氏名一覧で調べ、
//     登録済みの名前・住所が記号になっていない所が1つでもあれば渡さない。

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const restageAsideDir = "_置き換え直し前の写し"

func (s *panelServer) restage(ib *intakeBook) map[string]bool {
	out := map[string]bool{}
	stage := listFiles(s.p.stage)
	if len(stage) == 0 {
		return out
	}
	l := s.readLedger()
	inv := map[string]string{} // 置き換え済みのハッシュ → 元データのハッシュ
	for src, st := range l.srcStage {
		for h := range st {
			inv[h] = src
		}
	}
	doneByHash := map[string]string{}
	for _, f := range listFiles(s.p.done) {
		if h := s.hashFile(filepath.Join(s.p.done, f)); h != "" {
			doneByHash[h] = f
		}
	}
	origName := map[string]string{}
	if s.last != nil {
		for _, f := range s.last.Files {
			if f.Status == "ok" && f.OutName != "" {
				origName[f.OutName] = f.Name
			}
		}
	}
	for _, f := range stage {
		h := s.hashFile(filepath.Join(s.p.stage, f))
		srcH := inv[h]
		if srcH == "" {
			continue
		}
		df := doneByHash[srcH]
		if df == "" {
			continue
		}
		name := origName[f]
		if name == "" || name != filepath.Base(name) {
			name = df
		}
		dst := filepath.Join(s.p.src, name)
		if _, err := os.Stat(dst); err == nil {
			dst = uniquePath(s.p.src, name)
		}
		if err := os.Rename(filepath.Join(s.p.done, df), dst); err != nil {
			continue
		}
		// 置き換え済みの写しは消さずに脇によける（No.44 と同じく消さない。マスキング作業＼_置き換え直し前の写し＼3_確認待ち。戻すときはここから 3_確認待ち へ移す）。
		// 増え続けないよう、同じ名前の古い写しは1世代だけ残す（前の写しは今回の写しで置き換わる）
		aside := filepath.Join(s.p.work, restageAsideDir, "3_確認待ち")
		if err := os.MkdirAll(aside, 0700); err != nil {
			os.Rename(dst, filepath.Join(s.p.done, df))
			continue
		}
		if err := os.Rename(filepath.Join(s.p.stage, f), filepath.Join(aside, f)); err != nil {
			os.Rename(dst, filepath.Join(s.p.done, df)) // 写しを外せなければ元に戻す（二重にしない）
			continue
		}
		nn := filepath.Base(dst)
		if rec := ib.Stage[f]; rec != nil {
			ib.Src[nn] = rec
			delete(ib.Stage, f)
		}
		out[nn] = true
	}
	return out
}

// finalGate: 渡すファイルに、今の氏名一覧で記号になる名前・住所が残っていないか。残っていればファイル名を返す
func (s *panelServer) finalGate(files []string) []string {
	return s.finalGateIn(s.p.stage, files)
}

// finalGateIn: dir の中のファイルについて finalGate と同じ確かめ（同じ資料でほかの書類も作る でも使う。改善27）
func (s *panelServer) finalGateIn(dir string, files []string) []string {
	people, excl, err := readNameList(s.p.nameList)
	if err != nil {
		return nil
	}
	cb, err := loadCodeBook(s.p.codeBookF)
	if err != nil {
		return nil
	}
	m := newMasker(people, excl, cb) // 記号対応表は保存しない（調べるだけ）
	var bad []string
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext != ".docx" && ext != ".xlsx" && ext != ".xlsm" {
			continue
		}
		lines, _ := gateTextOOXML(filepath.Join(dir, f))
		if lines == nil && !gateReadable(filepath.Join(dir, f)) {
			bad = append(bad, f) // 読めないファイルは渡さない（安全側・点検役 R8.10.1）
			continue
		}
		lines = append(lines, sheetNames(filepath.Join(dir, f))...) // Excel のシート名も調べる（点検役 R8.10.1）
		hit := ""
		for _, ln := range lines {
			for _, x := range m.findMatches(ln, true) {
				if gateKind(x.kind) {
					hit = ln[x.start:x.end]
					break
				}
			}
			if hit != "" {
				break
			}
		}
		if hit != "" {
			bad = append(bad, f)
		}
	}
	return bad
}

// gateKind: 氏名一覧に登録した名前・呼び名・読み・住所から見つかった置き換えか（電話・日付などの形は除く）
func gateKind(k string) bool {
	for _, p := range []string{"利用者", "家族", "その他", "姓", "名だけ", "住所"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return strings.Contains(k, "呼び名") || strings.Contains(k, "読み") || strings.Contains(k, "名だけ")
}

var reSheetNameAttr = regexp.MustCompile(`<sheet\b[^>]*?\bname="([^"]*)"`)

// sheetNames: Excel のシート名（xl/workbook.xml）
func sheetNames(path string) []string {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil
	}
	defer zr.Close()
	var out []string
	for _, f := range zr.File {
		if f.Name != "xl/workbook.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		for _, m := range reSheetNameAttr.FindAllSubmatch(b, -1) {
			out = append(out, decodeEntities(string(m[1])))
		}
	}
	return out
}

// gateTextOOXML: 最後の関所で調べる文字。置き換えと同じ範囲にそろえる（改善15の2 R8.10.1）
// 置き換えは隠しのマスタ（郵便番号マスタ・医療機関マスタなど）とマスタだけが使う共有文字を調べずに写す（改善9）。
// 関所がそこまで見ると、もう一度置き換えても直らない止まり方になるので、同じく外す。
func gateTextOOXML(path string) ([]string, [][2]string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil
	}
	hidden, _, names := sheetVisibility(zr)
	parts, only := masterSheets(zr, hidden, names)
	if len(parts) == 0 {
		return docTextOOXMLData(data)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, zf := range zr.File {
		if parts[zf.Name] {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			continue
		}
		x, _ := io.ReadAll(rc)
		rc.Close()
		if zf.Name == "xl/sharedStrings.xml" && len(only) > 0 {
			n := -1
			x = reSI.ReplaceAllFunc(x, func(si []byte) []byte {
				n++
				if only[n] {
					return []byte("<si/>")
				}
				return si
			})
		}
		w, err := zw.Create(zf.Name)
		if err != nil {
			continue
		}
		w.Write(x)
	}
	zw.Close()
	return docTextOOXMLData(buf.Bytes())
}

// gateReadable: zip として開けるか（開けないものは最後の関所で止める）
func gateReadable(path string) bool {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	zr.Close()
	return true
}
