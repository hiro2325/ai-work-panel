package main

// 見える所だけの文字（改善15・R8.10.1 指摘）
//
// ③の「別の方のファイル」「別の利用者の記号も出てくる」と、①②の各ファイルの利用者の表示は、
// 書類の「見える所」に出てくる記号だけで判定する（隠したシート・行・列、セルが使っていない共有文字列、
// コメント、計算式の文字、定義名、docProps、削除済みの変更・隠し文字・フィールドのコードなどは見ない）。
// 置き換え（マスク）と最後の関所（finalGate）は今までどおりファイルの全部を見る（安全はゆるめない）。

import (
	"archive/zip"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// visibleText: xlsx/xlsm/docx の見える所の文字（行ごと）。ok=false はこの形では読めない（pdf など）
func visibleText(p string) ([]string, bool) {
	ext := strings.ToLower(path.Ext(p))
	if ext != ".xlsx" && ext != ".xlsm" && ext != ".docx" {
		return nil, false
	}
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, false
	}
	defer zr.Close()
	parts := map[string][]byte{}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".xml") && !strings.HasSuffix(f.Name, ".rels") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(rc, 64<<20))
		rc.Close()
		parts[f.Name] = b
	}
	if ext == ".docx" {
		return visibleDocx(parts), true
	}
	return visibleXlsx(parts), true
}

var (
	reVisPara    = regexp.MustCompile(`(?s)</w:p>`)
	reVisRun     = regexp.MustCompile(`(?s)<w:r\b[^>]*>(.*?)</w:r>`)
	reVisVanish  = regexp.MustCompile(`<w:vanish(?:\s+w:val="(?:1|true|on)")?\s*/>`)
	reVisWT      = regexp.MustCompile(`(?s)<w:t(?:\s[^>]*)?>(.*?)</w:t>`)
	reVisDocPart = regexp.MustCompile(`^word/(?:document|header\d*|footer\d*|footnotes|endnotes)\.xml$`)
)

func visibleDocx(parts map[string][]byte) []string {
	var out []string
	for name, b := range parts {
		if !reVisDocPart.MatchString(name) {
			continue
		}
		for _, para := range reVisPara.Split(string(b), -1) {
			var sb strings.Builder
			for _, r := range reVisRun.FindAllStringSubmatch(para, -1) {
				if reVisVanish.MatchString(r[1]) {
					continue // 隠し文字
				}
				for _, t := range reVisWT.FindAllStringSubmatch(r[1], -1) {
					sb.WriteString(decodeEntities(t[1]))
				}
			}
			if sb.Len() > 0 {
				out = append(out, sb.String())
			}
		}
	}
	return out
}

var (
	reVisSheet   = regexp.MustCompile(`<sheet\b[^>]*>`)
	reVisAttr    = func(a string) *regexp.Regexp { return regexp.MustCompile(`\b` + a + `="([^"]*)"`) }
	reVisName    = reVisAttr("name")
	reVisState   = reVisAttr("state")
	reVisRID     = regexp.MustCompile(`\br:id="([^"]*)"`)
	reVisRel     = regexp.MustCompile(`<Relationship\b[^>]*>`)
	reVisID      = reVisAttr("Id")
	reVisTarget  = reVisAttr("Target")
	reVisSI      = regexp.MustCompile(`(?s)<si>(.*?)</si>|<si/>`)
	reVisRPh     = regexp.MustCompile(`(?s)<rPh\b.*?</rPh>`)
	reVisT       = regexp.MustCompile(`(?s)<t(?:\s[^>]*)?>(.*?)</t>`)
	reVisCol     = regexp.MustCompile(`<col\b[^>]*>`)
	reVisMin     = reVisAttr("min")
	reVisMax     = reVisAttr("max")
	reVisHidden  = regexp.MustCompile(`\bhidden="(?:1|true)"`)
	reVisRow     = regexp.MustCompile(`(?s)<row\b([^>]*?)(?:/>|>(.*?)</row>)`)
	reVisCell    = regexp.MustCompile(`(?s)<c\b([^>]*?)(?:/>|>(.*?)</c>)`)
	reVisRef     = reVisAttr("r")
	reVisType    = reVisAttr("t")
	reVisV       = regexp.MustCompile(`(?s)<v>(.*?)</v>`)
	reVisIS      = regexp.MustCompile(`(?s)<is>(.*?)</is>`)
	reVisHF      = regexp.MustCompile(`(?s)<headerFooter\b[^>]*>(.*?)</headerFooter>`)
	reVisHFItem  = regexp.MustCompile(`(?s)<(?:odd|even|first)(?:Header|Footer)>(.*?)</(?:odd|even|first)(?:Header|Footer)>`)
	reVisColName = regexp.MustCompile(`^([A-Z]+)`)
)

func visColNum(ref string) int {
	m := reVisColName.FindString(ref)
	n := 0
	for _, c := range m {
		n = n*26 + int(c-'A'+1)
	}
	return n
}

func siText(si string) string {
	si = reVisRPh.ReplaceAllString(si, "") // ふりがなは見える文字ではない
	var sb strings.Builder
	for _, t := range reVisT.FindAllStringSubmatch(si, -1) {
		sb.WriteString(decodeEntities(t[1]))
	}
	return sb.String()
}

func visibleXlsx(parts map[string][]byte) []string {
	var out []string
	var sst []string
	if b, ok := parts["xl/sharedStrings.xml"]; ok {
		for _, m := range reVisSI.FindAllStringSubmatch(string(b), -1) {
			sst = append(sst, siText(m[1]))
		}
	}
	rels := map[string]string{}
	for _, r := range reVisRel.FindAllString(string(parts["xl/_rels/workbook.xml.rels"]), -1) {
		id, tg := reVisID.FindStringSubmatch(r), reVisTarget.FindStringSubmatch(r)
		if id == nil || tg == nil {
			continue
		}
		t := tg[1]
		if strings.HasPrefix(t, "/") {
			t = strings.TrimPrefix(t, "/")
		} else {
			t = path.Clean("xl/" + t)
		}
		rels[id[1]] = t
	}
	for _, sh := range reVisSheet.FindAllString(string(parts["xl/workbook.xml"]), -1) {
		if st := reVisState.FindStringSubmatch(sh); st != nil && (st[1] == "hidden" || st[1] == "veryHidden") {
			continue // 隠したシート
		}
		rid := reVisRID.FindStringSubmatch(sh)
		if rid == nil {
			continue
		}
		x := string(parts[rels[rid[1]]])
		if x == "" {
			continue
		}
		hiddenCol := map[int]bool{}
		for _, c := range reVisCol.FindAllString(x, -1) {
			if !reVisHidden.MatchString(c) {
				continue
			}
			mn, mx := reVisMin.FindStringSubmatch(c), reVisMax.FindStringSubmatch(c)
			if mn == nil || mx == nil {
				continue
			}
			a, _ := strconv.Atoi(mn[1])
			b, _ := strconv.Atoi(mx[1])
			for i := a; i <= b && i-a < 20000; i++ {
				hiddenCol[i] = true
			}
		}
		for _, row := range reVisRow.FindAllStringSubmatch(x, -1) {
			if reVisHidden.MatchString(row[1]) {
				continue // 隠した行
			}
			var line []string
			for _, c := range reVisCell.FindAllStringSubmatch(row[2], -1) {
				ref := reVisRef.FindStringSubmatch(c[1])
				if ref != nil && hiddenCol[visColNum(ref[1])] {
					continue // 隠した列
				}
				typ := ""
				if t := reVisType.FindStringSubmatch(c[1]); t != nil {
					typ = t[1]
				}
				txt := ""
				switch typ {
				case "s":
					if v := reVisV.FindStringSubmatch(c[2]); v != nil {
						if i, err := strconv.Atoi(strings.TrimSpace(v[1])); err == nil && i >= 0 && i < len(sst) {
							txt = sst[i]
						}
					}
				case "inlineStr":
					if is := reVisIS.FindStringSubmatch(c[2]); is != nil {
						txt = siText(is[1])
					}
				default: // 数値・計算式（表示される値 <v> だけ。式の文字 <f> は見ない）
					if v := reVisV.FindStringSubmatch(c[2]); v != nil {
						txt = decodeEntities(v[1])
					}
				}
				if txt != "" {
					line = append(line, txt)
				}
			}
			if len(line) > 0 {
				out = append(out, strings.Join(line, "\t"))
			}
		}
		// このシートに付いている図形・テキストボックスの文字（画面に見える。改善15 追加）
		for _, d := range sheetDrawings(parts, rels[rid[1]]) {
			for _, tb := range reVisTxBody.FindAllStringSubmatch(string(parts[d]), -1) {
				var sb strings.Builder
				for _, t := range reVisAT.FindAllStringSubmatch(tb[1], -1) {
					sb.WriteString(decodeEntities(t[1]))
				}
				if sb.Len() > 0 {
					out = append(out, sb.String())
				}
			}
		}
		if hf := reVisHF.FindStringSubmatch(x); hf != nil {
			for _, it := range reVisHFItem.FindAllStringSubmatch(hf[1], -1) {
				out = append(out, decodeEntities(it[1]))
			}
		}
	}
	return out
}

// visibleUsers: 見える所に出てくる利用者の記号（ok=false はこの形では読めない）
func visibleUsers(p string) (map[string]bool, bool) {
	lines, ok := visibleText(p)
	if !ok {
		return nil, false
	}
	seen := map[string]bool{}
	for _, l := range lines {
		for _, m := range reStageUser.FindAllStringSubmatch(l, -1) {
			seen[m[1]] = true
		}
	}
	return seen, true
}

var (
	reVisTxBody = regexp.MustCompile(`(?s)<(?:xdr:)?txBody\b[^>]*>(.*?)</(?:xdr:)?txBody>`)
	reVisAT     = regexp.MustCompile(`(?s)<a:t(?:\s[^>]*)?>(.*?)</a:t>`)
	reVisType2  = reVisAttr("Type")
)

// sheetDrawings: シートの部品（xl/worksheets/sheet1.xml）に付いている図形の部品（xl/drawings/drawing1.xml）
func sheetDrawings(parts map[string][]byte, sheet string) []string {
	dir, file := path.Split(sheet)
	relsName := dir + "_rels/" + file + ".rels"
	var out []string
	for _, r := range reVisRel.FindAllString(string(parts[relsName]), -1) {
		ty, tg := reVisType2.FindStringSubmatch(r), reVisTarget.FindStringSubmatch(r)
		if ty == nil || tg == nil || !strings.HasSuffix(ty[1], "/drawing") {
			continue
		}
		t := tg[1]
		if strings.HasPrefix(t, "/") {
			t = strings.TrimPrefix(t, "/")
		} else {
			t = path.Clean(dir + t)
		}
		out = append(out, t)
	}
	return out
}
