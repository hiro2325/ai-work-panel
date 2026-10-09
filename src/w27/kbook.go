package main

// 支援経過を1冊にまとめる「支援経過_氏名.xlsx」（改善29 No.57・R8.10.7 承認）
//
//   - 利用者ごとに1冊。1枚目のシート「一覧」と、日ごとのシート（月.日。同じ日の2件目は 月.日②）が並ぶ。マクロなしの .xlsx。
//   - 一覧の形は、支援経過記録の原紙（R8.10.3案）の「一覧」シートと同じ（見出し・列幅・罫線・印刷設定）。
//     列：A 日付／B 区分／C 本文／D 並びキー（非表示）／E 続き（非表示）。1日分は、見出し「（10.7）【目的】」＋本文で C に入れる。
//     長い本文は、行の切れ目で数行に分ける（原紙のマクロと同じ決まり。日付・区分は最初の行だけ）。
//   - 新しい日は一覧の一番上（見出しのすぐ下）に1行足す。まとめるときは、古い日から順に足したのと同じ並び（新しい日が上）。
//   - ここには「ブックを作る・一覧の行を作る・確かめる」だけを置く。サーバーへの書き込みは daysheet.go の手順（控え→一時ファイル→入れ替え）。
//   - パネルのコードには、サーバーで消す操作を入れない。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	keikaBookPrefix = "支援経過_"
	keikaBookExt    = ".xlsx"

	listMaxLines     = 20    // 一覧の1行に入れる本文の行数の上限（原紙のマクロと同じ）
	listCharsPerLine = 32    // 一覧の本文列の1行の全角字数（見積り）
	listMaxCell      = 30000 // 1セルに入れる字数の上限
)

// bookXlsxFiles: 利用者フォルダのいちばん上にある「支援経過_…xlsx」（ロックファイル・途中のファイルは除く）
func bookXlsxFiles(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, "~$") || strings.HasPrefix(n, ".") || strings.HasSuffix(n, ".part") {
			continue
		}
		if strings.HasPrefix(n, keikaBookPrefix) && strings.EqualFold(filepath.Ext(n), keikaBookExt) {
			if st, err := os.Lstat(filepath.Join(dir, n)); err == nil && st.Mode().IsRegular() {
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// noSpaceName: 氏名の空白（半角・全角）を取る（前の1日分のファイル名「支援経過1日分_架空一郎_…」と同じ書き方）
func noSpaceName(n string) string {
	return strings.NewReplacer(" ", "", "　", "").Replace(n)
}

// bookNameFor: 「支援経過_氏名.xlsx」。氏名は利用者フォルダの名前（ふりがな_氏名）の「_」のあと。なければ氏名一覧の氏名
func bookNameFor(dir, rowName string) string {
	n := filepath.Base(dir)
	if i := strings.Index(n, "_"); i >= 0 {
		n = n[i+1:]
	} else {
		n = rowName
	}
	n = noSpaceName(cleanDirName(n))
	if n == "" {
		n = noSpaceName(cleanDirName(rowName))
	}
	return keikaBookPrefix + n + keikaBookExt
}

// ---------------- 一覧の行を作る ----------------

type listRec struct {
	a, b, c, d, e string // 日付・区分・本文・並びキー・続き
	end           bool   // 1件の記録の最後の行（この行の下に横線）
}

// 改行・タブをそろえ、使えない制御文字を除き、末尾の空白・改行を落とす（原紙のマクロの CleanText）
func listCleanText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\t", "　")
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r >= 32 {
			b.WriteRune(r)
		}
	}
	return strings.TrimRight(b.String(), "\n 　")
}

// 改行を全角空白にして1行にする（区分・目的用）
func listOneLine(s string) string {
	s = strings.ReplaceAll(listCleanText(s), "\n", "　")
	return strings.Trim(s, " ")
}

// 1段落が何行になるかの見積り（半角は 0.6 字）
func listEstLines(s string) int {
	w := 0.0
	for _, r := range s {
		if r <= 0x7F || (r >= 0xFF61 && r <= 0xFF9F) {
			w += 0.6
		} else {
			w++
		}
	}
	n := int(w / float64(listCharsPerLine))
	if float64(n)*float64(listCharsPerLine) < w-1e-9 {
		n++
	}
	if n < 1 {
		n = 1
	}
	return n
}

// 本文を、1行に入れる量ごとに分ける。切るのは段落（改行）の切れ目。1つの段落だけで多すぎるときは字数で切り、続き（1）にする
func listSplitChunks(text string) (texts []string, flags []int) {
	sliceLen := listMaxLines * listCharsPerLine
	if sliceLen > listMaxCell {
		sliceLen = listMaxCell
	}
	cur, hasCur, curLines := "", false, 0
	push := func(s string, f int) {
		texts = append(texts, s)
		flags = append(flags, f)
	}
	for _, p := range strings.Split(text, "\n") {
		pl := listEstLines(p)
		if pl > listMaxLines || utf8.RuneCountInString(p) > listMaxCell {
			if hasCur {
				push(cur, 0)
				cur, hasCur, curLines = "", false, 0
			}
			rs := []rune(p)
			for pos := 0; pos < len(rs); pos += sliceLen {
				end := pos + sliceLen
				if end > len(rs) {
					end = len(rs)
				}
				f := 1
				if pos == 0 {
					f = 0
				}
				push(string(rs[pos:end]), f)
			}
			continue
		}
		if hasCur && curLines+pl > listMaxLines {
			push(cur, 0)
			cur, hasCur, curLines = "", false, 0
		}
		if hasCur {
			cur += "\n" + p
		} else {
			cur, hasCur = p, true
		}
		curLines += pl
	}
	if hasCur {
		push(cur, 0)
	}
	if len(texts) == 0 {
		push("", 0)
	}
	return
}

// listSeqOf: シート名の「月.日②」の番号（印がなければ1）
func listSeqOf(sheet string) int {
	m := reDaySheet.FindStringSubmatch(toHalfDigits(sheet))
	if m == nil || m[3] == "" {
		return 1
	}
	for i, c := range keikaXCirc {
		if string(c) == m[3] {
			return i + 2
		}
	}
	return 1
}

// listKeyOf: 並びキー（年月日-番号）
func listKeyOf(d *kxDay) string {
	m := reDaySheet.FindStringSubmatch(d.sheet)
	mo, dd := d.m, d.d
	if m != nil {
		mo, dd = atoi(m[1]), atoi(m[2])
	}
	return fmt.Sprintf("%08d-%02d", (d.y+2018)*10000+mo*100+dd, listSeqOf(d.sheet))
}

// listRecsOf: 1日分を、一覧の行（数行になることもある）にする。日付・区分は最初の行だけ
func listRecsOf(d *kxDay) []listRec {
	m := reDaySheet.FindStringSubmatch(d.sheet)
	mo, dd := d.m, d.d
	if m != nil {
		mo, dd = atoi(m[1]), atoi(m[2])
	}
	heading := "（" + strconv.Itoa(mo) + "." + strconv.Itoa(dd)
	if seq := listSeqOf(d.sheet); seq > 1 {
		heading += string(keikaXCirc[seq-2])
	}
	heading += "）"
	if purpose := listOneLine(d.mokuteki); purpose != "" {
		if strings.HasPrefix(purpose, "【") {
			heading += purpose
		} else {
			heading += "【" + purpose + "】"
		}
	}
	text := heading
	if body := listCleanText(d.body); body != "" {
		text += "\n" + body
	}
	key := listKeyOf(d)
	texts, flags := listSplitChunks(text)
	var out []listRec
	for i, t := range texts {
		r := listRec{c: t, d: key, e: strconv.Itoa(flags[i]), end: i == len(texts)-1}
		if i == 0 {
			r.a = "R" + strconv.Itoa(d.y) + "." + strconv.Itoa(mo) + "." + strconv.Itoa(dd)
			r.b = listOneLine(d.kubun)
		}
		out = append(out, r)
	}
	return out
}

// ---------------- 一覧の見た目（styles.xml） ----------------

type listStyles struct{ hdr, hdrDE, plain, row, rowEnd, rowTop, rowTopEnd int }

const (
	lsFontHdr  = `<font><b/><sz val="10"/><color theme="1"/><name val="游ゴシック"/><family val="3"/><charset val="128"/><scheme val="minor"/></font>`
	lsFontData = `<font><sz val="10"/><color theme="1"/><name val="游明朝"/><family val="1"/><charset val="128"/></font>`
	lsFillGray = `<fill><patternFill patternType="solid"><fgColor rgb="FFEDEDED"/><bgColor indexed="64"/></patternFill></fill>`
)

func lsBorder(l, r, t, b bool) string {
	side := func(tag string, on bool) string {
		if on {
			return `<` + tag + ` style="thin"><color rgb="FF7F7F7F"/></` + tag + `>`
		}
		return `<` + tag + `/>`
	}
	return `<border>` + side("left", l) + side("right", r) + side("top", t) + side("bottom", b) + `<diagonal/></border>`
}

// styleSection: styles.xml の <fonts>・<fills>・<borders>・<cellXfs> の、開きタグ・中身・閉じタグの位置
func styleSection(styles, section string) (openStart, openEnd, closeStart int, ok bool) {
	loc := regexp.MustCompile(`<` + section + `\b[^>]*>`).FindStringIndex(styles)
	if loc == nil {
		return 0, 0, 0, false
	}
	c := strings.Index(styles[loc[1]:], "</"+section+">")
	if c < 0 {
		return 0, 0, 0, false
	}
	return loc[0], loc[1], loc[1] + c, true
}

// styleItems: 部品の中の1つ1つ（<font>…</font>・<xf …/> など）
func styleItems(body, item string) []string {
	re := regexp.MustCompile(`(?s)<` + item + `\b[^>]*?/>|<` + item + `\b[^>]*?>.*?</` + item + `>`)
	return re.FindAllString(body, -1)
}

var reCountAttr = regexp.MustCompile(`\scount="\d+"`)

// ensureStyleItem: その部品が styles.xml にあればその番号、なければ最後に足して番号を返す（もとの部品は触らない・順番も変えない）
func ensureStyleItem(styles, section, item, xml string) (string, int, error) {
	os_, oe, cs, ok := styleSection(styles, section)
	if !ok {
		return styles, 0, fmt.Errorf("styles.xml に %s が見つかりません", section)
	}
	items := styleItems(styles[oe:cs], item)
	for i, it := range items {
		if it == xml {
			return styles, i, nil
		}
	}
	openTag := styles[os_:oe]
	cnt := ` count="` + strconv.Itoa(len(items)+1) + `"`
	if reCountAttr.MatchString(openTag) {
		openTag = reCountAttr.ReplaceAllString(openTag, cnt)
	} else {
		openTag = strings.TrimSuffix(openTag, ">") + cnt + ">"
	}
	return styles[:os_] + openTag + styles[oe:cs] + xml + styles[cs:], len(items), nil
}

func ensureListStyles(styles string) (string, listStyles, error) {
	var ls listStyles
	var err error
	idx := func(section, item, xml string) int {
		if err != nil {
			return 0
		}
		var i int
		styles, i, err = ensureStyleItem(styles, section, item, xml)
		return i
	}
	fHdr := idx("fonts", "font", lsFontHdr)
	fData := idx("fonts", "font", lsFontData)
	fill := idx("fills", "fill", lsFillGray)
	bHdr := idx("borders", "border", lsBorder(true, true, true, false))
	bHdrDE := idx("borders", "border", lsBorder(true, true, true, true))
	bRow := idx("borders", "border", lsBorder(true, true, false, false))
	bRowEnd := idx("borders", "border", lsBorder(true, true, false, true))
	bRowTop := idx("borders", "border", lsBorder(true, true, true, false))
	bRowTopEnd := idx("borders", "border", lsBorder(true, true, true, true))
	xf := func(font, fl, border int, h, v string) string {
		fillAttr, applyFill := "0", ""
		if fl >= 0 {
			fillAttr, applyFill = strconv.Itoa(fl), ` applyFill="1"`
		}
		return fmt.Sprintf(`<xf numFmtId="49" fontId="%d" fillId="%s" borderId="%d" xfId="0" applyNumberFormat="1" applyFont="1"%s applyBorder="1" applyAlignment="1"><alignment horizontal="%s" vertical="%s" wrapText="1"/></xf>`, font, fillAttr, border, applyFill, h, v)
	}
	ls.hdr = idx("cellXfs", "xf", xf(fHdr, fill, bHdr, "center", "center"))
	ls.hdrDE = idx("cellXfs", "xf", xf(fHdr, fill, bHdrDE, "center", "center"))
	ls.plain = idx("cellXfs", "xf", xf(fData, -1, 0, "left", "top"))
	ls.row = idx("cellXfs", "xf", xf(fData, -1, bRow, "left", "top"))
	ls.rowEnd = idx("cellXfs", "xf", xf(fData, -1, bRowEnd, "left", "top"))
	ls.rowTop = idx("cellXfs", "xf", xf(fData, -1, bRowTop, "left", "top"))
	ls.rowTopEnd = idx("cellXfs", "xf", xf(fData, -1, bRowTopEnd, "left", "top"))
	return styles, ls, err
}

// ---------------- 一覧シートのXML ----------------

func listStrCell(ref string, s int, text string) string {
	if text == "" {
		return `<c r="` + ref + `" s="` + strconv.Itoa(s) + `"/>`
	}
	return `<c r="` + ref + `" s="` + strconv.Itoa(s) + `" t="inlineStr">` + inlineStrXML(text) + `</c>`
}

// listRowXML: 一覧のデータ1行（row は行の番号。top は、一覧の一番上の行（見出しの下の線を引く））
func listRowXML(row int, r listRec, st listStyles, top bool) string {
	s := st.row
	switch {
	case top && r.end:
		s = st.rowTopEnd
	case top:
		s = st.rowTop
	case r.end:
		s = st.rowEnd
	}
	n := strconv.Itoa(row)
	return `<row r="` + n + `" spans="1:5">` + listStrCell("A"+n, s, r.a) + listStrCell("B"+n, s, r.b) + listStrCell("C"+n, s, r.c) +
		listStrCell("D"+n, st.plain, r.d) + listStrCell("E"+n, st.plain, r.e) + `</row>`
}

// listSheetXML: 新しい「一覧」シート（見出し＋行）。形は原紙の「一覧」と同じ
func listSheetXML(recs []listRec, st listStyles) string {
	var b strings.Builder
	last := len(recs) + 1
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`)
	b.WriteString(`<sheetPr><pageSetUpPr fitToPage="1"/></sheetPr>`)
	b.WriteString(`<dimension ref="A1:E` + strconv.Itoa(last) + `"/>`)
	b.WriteString(`<sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/><selection pane="bottomLeft" activeCell="A2" sqref="A2"/></sheetView></sheetViews>`)
	b.WriteString(`<sheetFormatPr defaultRowHeight="17.649999999999999"/>`)
	p := strconv.Itoa(st.plain)
	b.WriteString(`<cols><col min="1" max="1" width="9.5" style="` + p + `" customWidth="1"/><col min="2" max="2" width="12" style="` + p + `" customWidth="1"/><col min="3" max="3" width="56" style="` + p + `" customWidth="1"/><col min="4" max="5" width="12" style="` + p + `" hidden="1" customWidth="1"/></cols>`)
	b.WriteString(`<sheetData><row r="1" spans="1:5" ht="17.45" customHeight="1">`)
	h, hde := strconv.Itoa(st.hdr), strconv.Itoa(st.hdrDE)
	for i, t := range []string{"日付", "区分", "本文"} {
		b.WriteString(`<c r="` + string(rune('A'+i)) + `1" s="` + h + `" t="inlineStr">` + inlineStrXML(t) + `</c>`)
	}
	for i, t := range []string{"並びキー", "続き"} {
		b.WriteString(`<c r="` + string(rune('D'+i)) + `1" s="` + hde + `" t="inlineStr">` + inlineStrXML(t) + `</c>`)
	}
	b.WriteString(`</row>`)
	for i, r := range recs {
		b.WriteString(listRowXML(i+2, r, st, i == 0))
	}
	b.WriteString(`</sheetData>`)
	b.WriteString(`<pageMargins left="0.7" right="0.7" top="0.75" bottom="0.75" header="0.3" footer="0.3"/><pageSetup paperSize="9" fitToHeight="0" orientation="portrait"/><headerFooter><oddFooter>&amp;C&amp;P / &amp;N</oddFooter></headerFooter></worksheet>`)
	return b.String()
}

// ---------------- 一覧シートを読む ----------------

var reListCell = regexp.MustCompile(`(?s)<c r="([A-Z]+)(\d+)"((?:\s[^>]*?)?)(?:/>|>(.*?)</c>)`)

// listCells: 行の番号 → 列（A〜E）→ 文字
func listCells(sheet string, ss []string) map[int]map[string]string {
	out := map[int]map[string]string{}
	for _, m := range reListCell.FindAllStringSubmatch(sheet, -1) {
		row, _ := strconv.Atoi(m[2])
		typ := attr("<c"+m[3]+">", "t")
		inner := m[4]
		var v string
		switch typ {
		case "s":
			if g := regexp.MustCompile(`<v>(\d+)</v>`).FindStringSubmatch(inner); g != nil {
				if i, _ := strconv.Atoi(g[1]); i < len(ss) {
					v = ss[i]
				}
			}
		case "inlineStr":
			var b strings.Builder
			for _, t := range regexp.MustCompile(`(?s)<t(?:\s[^>]*)?>(.*?)</t>`).FindAllStringSubmatch(inner, -1) {
				b.WriteString(xmlUnesc(t[1]))
			}
			v = b.String()
		default:
			if g := regexp.MustCompile(`(?s)<v>(.*?)</v>`).FindStringSubmatch(inner); g != nil {
				v = xmlUnesc(g[1])
			}
		}
		if out[row] == nil {
			out[row] = map[string]string{}
		}
		out[row][m[1]] = v
	}
	return out
}

// listHeaderOK: 1行目の見出しが 日付・区分・本文・並びキー・続き か
func listHeaderOK(sheet string, ss []string) bool {
	h := listCells(sheet, ss)[1]
	return h["A"] == "日付" && h["B"] == "区分" && h["C"] == "本文" && h["D"] == "並びキー" && h["E"] == "続き"
}

func listLastRow(cells map[int]map[string]string) int {
	n := 1
	for r := range cells {
		if r > n {
			n = r
		}
	}
	return n
}

// ---------------- 今ある一覧の一番上に足す ----------------

var reListForbidden = regexp.MustCompile(`<mergeCell\b|<conditionalFormatting\b|<dataValidation\b|<autoFilter\b|<hyperlink\b|<tablePart\b|<drawing\b|<legacyDrawing\b|<f[ >/]`)

var (
	reSheetRow   = regexp.MustCompile(`<row r="(\d+)"`)
	reSheetCellR = regexp.MustCompile(`<c r="([A-Z]+)(\d+)"`)
	reRow1       = regexp.MustCompile(`(?s)<row r="1"[^>]*?(?:/>|>.*?</row>)`)
	reDimension  = regexp.MustCompile(`<dimension ref="([A-Z]+)(\d+):([A-Z]+)(\d+)"/>`)
)

// listInsertTop: 一覧の見出し（1行目）のすぐ下に行を足す。今ある行は、下へずらすだけで中身はそのまま
func listInsertTop(sheet string, recs []listRec, st listStyles) (string, error) {
	if reListForbidden.MatchString(sheet) {
		return "", fmt.Errorf("「一覧」に、結合・式・入力規則などがあるため、行を足せません")
	}
	loc := reRow1.FindStringIndex(sheet)
	if loc == nil {
		return "", fmt.Errorf("「一覧」の見出し行が見つかりません")
	}
	n := len(recs)
	sd := strings.Index(sheet, "<sheetData")
	ed := strings.LastIndex(sheet, "</sheetData>")
	if sd < 0 || ed < 0 || loc[0] < sd || loc[1] > ed {
		return "", fmt.Errorf("「一覧」の形が思ったものと違います")
	}
	after := sheet[loc[1]:ed]
	after = reSheetRow.ReplaceAllStringFunc(after, func(m string) string {
		r, _ := strconv.Atoi(reSheetRow.FindStringSubmatch(m)[1])
		if r >= 2 {
			return `<row r="` + strconv.Itoa(r+n) + `"`
		}
		return m
	})
	after = reSheetCellR.ReplaceAllStringFunc(after, func(m string) string {
		g := reSheetCellR.FindStringSubmatch(m)
		r, _ := strconv.Atoi(g[2])
		if r >= 2 {
			return `<c r="` + g[1] + strconv.Itoa(r+n) + `"`
		}
		return m
	})
	var nb strings.Builder
	for i, r := range recs {
		nb.WriteString(listRowXML(i+2, r, st, i == 0))
	}
	out := sheet[:loc[1]] + nb.String() + after + sheet[ed:]
	out = reDimension.ReplaceAllStringFunc(out, func(m string) string {
		g := reDimension.FindStringSubmatch(m)
		r, _ := strconv.Atoi(g[4])
		if r < 1 {
			r = 1
		}
		return `<dimension ref="` + g[1] + g[2] + `:` + g[3] + strconv.Itoa(r+n) + `"/>`
	})
	return out, nil
}

// ---------------- 一覧のシートを足す・印刷範囲を直す ----------------

// listSheetEntry: 「一覧」のシートの部品の名前
func listSheetEntry(p *ooPkg) (string, bool) {
	sheets, err := wbSheets(p)
	if err != nil {
		return "", false
	}
	for _, sh := range sheets {
		if sh.name == keikaXList {
			return sh.target, true
		}
	}
	return "", false
}

// addListSheetFront: 新しい「一覧」シートを、いちばん左に足す（印刷の見出し行つき）
func addListSheetFront(p *ooPkg, recs []listRec) error {
	if _, has := listSheetEntry(p); has {
		return fmt.Errorf("「一覧」というシートがすでにあります")
	}
	sheets, err := wbSheets(p)
	if err != nil {
		return err
	}
	styles, ok := p.get("xl/styles.xml")
	if !ok {
		return fmt.Errorf("Excelの中身（styles.xml）が見つかりません")
	}
	styles, ls, err := ensureListStyles(styles)
	if err != nil {
		return err
	}
	wb, _ := p.get("xl/workbook.xml")
	wbRels, ok := p.get("xl/_rels/workbook.xml.rels")
	if !ok {
		return fmt.Errorf("Excelの中身（workbook.xml.rels）が見つかりません")
	}
	ct, ok := p.get("[Content_Types].xml")
	if !ok {
		return fmt.Errorf("Excelの中身（[Content_Types].xml）が見つかりません")
	}
	if !strings.Contains(wb, "<sheets>") {
		return fmt.Errorf("Excelの中身（workbook.xml）の形が思ったものと違います")
	}
	maxSheetID := 0
	for _, s := range sheets {
		if n, _ := strconv.Atoi(s.sheetID); n > maxSheetID {
			maxSheetID = n
		}
	}
	maxRid := 0
	for id := range relsMap(wbRels) {
		if strings.HasPrefix(id, "rId") {
			if n, _ := strconv.Atoi(strings.TrimPrefix(id, "rId")); n > maxRid {
				maxRid = n
			}
		}
	}
	target := ""
	for i := 1; ; i++ {
		t := fmt.Sprintf("xl/worksheets/sheet%d.xml", i)
		if !p.has(t) {
			target = t
			break
		}
	}
	rid := fmt.Sprintf("rId%d", maxRid+1)
	p.set(target, listSheetXML(recs, ls))
	p.set("xl/styles.xml", styles)
	wbRels = strings.Replace(wbRels, "</Relationships>", `<Relationship Id="`+rid+`" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="`+strings.TrimPrefix(target, "xl/")+`"/></Relationships>`, 1)
	ct = strings.Replace(ct, "</Types>", `<Override PartName="/`+target+`" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`, 1)
	wb = strings.Replace(wb, "<sheets>", `<sheets><sheet name="`+xmlAttrEscape(keikaXList)+`" sheetId="`+strconv.Itoa(maxSheetID+1)+`" r:id="`+rid+`"/>`, 1)
	wb = shiftSheetIndex(wb, 0)
	def := `<definedName name="_xlnm.Print_Titles" localSheetId="0">` + encodeText(keikaXList) + `!$1:$1</definedName>`
	if strings.Contains(wb, "</definedNames>") {
		wb = strings.Replace(wb, "</definedNames>", def+"</definedNames>", 1)
	} else {
		wb = strings.Replace(wb, "</sheets>", "</sheets><definedNames>"+def+"</definedNames>", 1)
	}
	p.set("xl/workbook.xml", wb)
	p.set("xl/_rels/workbook.xml.rels", wbRels)
	p.set("[Content_Types].xml", ct)
	if app, ok := p.get("docProps/app.xml"); ok {
		app = appAddSheet(app, keikaXList, 0)
		app = appAddNamed(app, keikaXList+"!Print_Titles")
		p.set("docProps/app.xml", app)
	}
	return nil
}

// fixPrintAreasToSelf: 印刷範囲が、ないシート（原紙!…）を指しているものを、そのシート自身に直す（まとめて1冊にするときだけ）
func fixPrintAreasToSelf(p *ooPkg) error {
	names, err := kxSheetNames(p)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	wb, _ := p.get("xl/workbook.xml")
	changed := false
	wb = reDefName.ReplaceAllStringFunc(wb, func(d string) string {
		if attr(d, "name") != "_xlnm.Print_Area" {
			return d
		}
		i, err := strconv.Atoi(attr(d, "localSheetId"))
		if err != nil || i < 0 || i >= len(names) {
			return d
		}
		open := d[:strings.Index(d, ">")+1]
		txt := xmlUnesc(d[len(open):strings.LastIndex(d, "<")])
		k := strings.LastIndex(txt, "!")
		if k < 0 {
			return d
		}
		ref := strings.Trim(strings.ReplaceAll(txt[:k], "''", "'"), "'")
		if have[ref] {
			return d
		}
		changed = true
		q := "'" + strings.ReplaceAll(names[i], "'", "''") + "'"
		return open + encodeText(q) + "!" + encodeText(txt[k+1:]) + "</definedName>"
	})
	if changed {
		p.set("xl/workbook.xml", wb)
	}
	return nil
}

// bookDayRecs: ブックの中の日のシート（月.日）をすべて読んで、新しい日が上の一覧の行にする
func bookDayRecs(p *ooPkg) ([]listRec, error) {
	sheets, err := wbSheets(p)
	if err != nil {
		return nil, err
	}
	ss := sharedStrings(p)
	type dayRec struct {
		key  string
		recs []listRec
	}
	var ds []dayRec
	for _, sh := range sheets {
		if !reDaySheet.MatchString(toHalfDigits(sh.name)) {
			continue
		}
		x, ok := p.get(sh.target)
		if !ok {
			return nil, fmt.Errorf("シート「%s」の中身が見つかりません", sh.name)
		}
		d, err := parseDaySheet(x, ss, sh.name)
		if err != nil {
			return nil, fmt.Errorf("シート「%s」を読めません：%v", sh.name, err)
		}
		ds = append(ds, dayRec{listKeyOf(d), listRecsOf(d)})
	}
	sort.SliceStable(ds, func(a, b int) bool { return ds[a].key > ds[b].key })
	var out []listRec
	for _, d := range ds {
		out = append(out, d.recs...)
	}
	return out, nil
}

// ---------------- ブックを作る ----------------

// buildBookNew: 日のシートだけのブック（届いた1日分・今ある1日分のExcel）に、「一覧」を足して「支援経過_氏名.xlsx」の形にする
func buildBookNew(base []byte) ([]byte, error) {
	p, err := readPkg(base)
	if err != nil {
		return nil, err
	}
	recs, err := bookDayRecs(p)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("日のシート（月.日）が見つかりません")
	}
	if err := fixPrintAreasToSelf(p); err != nil {
		return nil, err
	}
	if err := addListSheetFront(p, recs); err != nil {
		return nil, err
	}
	out, err := p.bytes()
	if err != nil {
		return nil, err
	}
	if err := verifyBook(base, out, recs, nil); err != nil {
		return nil, err
	}
	return openAtList(out)
}

// buildBookAppend: 今ある「支援経過_氏名.xlsx」に、この日のシートを足し、一覧の一番上に1行足す
func buildBookAppend(base []byte, day *kxDay, sheetName, tmplName, after string) ([]byte, error) {
	step1, err := buildDayAppend(base, day, sheetName, tmplName, after) // シートを足す（今あるシート・ほかの部品はバイトのまま。足したあと自分で確かめる）
	if err != nil {
		return nil, err
	}
	p, err := readPkg(step1)
	if err != nil {
		return nil, err
	}
	lt, ok := listSheetEntry(p)
	if !ok {
		return nil, fmt.Errorf("「%s」のシートがありません", keikaXList)
	}
	lx, _ := p.get(lt)
	ss := sharedStrings(p)
	if !listHeaderOK(lx, ss) {
		return nil, fmt.Errorf("「%s」の見出しが 日付・区分・本文・並びキー・続き の形ではありません", keikaXList)
	}
	styles, _ := p.get("xl/styles.xml")
	ns, ls, err := ensureListStyles(styles)
	if err != nil {
		return nil, err
	}
	d2 := *day
	d2.sheet = sheetName
	recs := listRecsOf(&d2)
	nx, err := listInsertTop(lx, recs, ls)
	if err != nil {
		return nil, err
	}
	p.set(lt, nx)
	if ns != styles {
		p.set("xl/styles.xml", ns)
	}
	out, err := p.bytes()
	if err != nil {
		return nil, err
	}
	if err := verifyBookListInsert(step1, out, lt, recs); err != nil {
		return nil, err
	}
	return openAtList(out)
}

// buildBookMerge: 今ある「支援経過1日分_…xlsx」（日のシートだけ）に、この日のシートを足して、「一覧」を足す
func buildBookMerge(legacy []byte, day *kxDay, sheetName, tmplName string) ([]byte, error) {
	step1, err := buildDayAppend(legacy, day, sheetName, tmplName, "") // いちばん左にこの日のシート
	if err != nil {
		return nil, err
	}
	return buildBookNew(step1)
}

// ---------------- 確かめる（だめなら何も書かない） ----------------

// stylesOnlyAppended: styles.xml の項目（フォント・塗り・罫線・書式）は、もとの項目が同じ順で先頭に残り、足したものは最後だけ。ほかの部分は同じ
func stylesOnlyAppended(oldS, newS string) bool {
	for _, sec := range [][2]string{{"fonts", "font"}, {"fills", "fill"}, {"borders", "border"}, {"cellXfs", "xf"}} {
		os_, oe, cs, ok1 := styleSection(oldS, sec[0])
		ns_, ne, ncs, ok2 := styleSection(newS, sec[0])
		if !ok1 || !ok2 {
			return false
		}
		oi, ni := styleItems(oldS[oe:cs], sec[1]), styleItems(newS[ne:ncs], sec[1])
		if len(ni) < len(oi) {
			return false
		}
		for i := range oi {
			if oi[i] != ni[i] {
				return false
			}
		}
		// 項目の外側は同じ（開きタグの count 以外）
		if reCountAttr.ReplaceAllString(oldS[os_:oe], "") != reCountAttr.ReplaceAllString(newS[ns_:ne], "") {
			return false
		}
	}
	cut := func(s string) string {
		for _, sec := range []string{"fonts", "fills", "borders", "cellXfs"} {
			a, _, c, ok := styleSection(s, sec)
			if ok {
				s = s[:a] + "<" + sec + "/>" + s[c+len("</"+sec+">"):]
			}
		}
		return s
	}
	return cut(oldS) == cut(newS)
}

// verifyBookRecs: 一覧の 2行目から recs が並び、そのあとに「もとの一覧の行」（oldCells。なければなし）が同じ並びで続く
func verifyListRows(lx string, ss []string, recs []listRec, oldCells map[int]map[string]string) error {
	cells := listCells(lx, ss)
	if !listHeaderOK(lx, ss) {
		return fmt.Errorf("一覧の見出しが正しくありません")
	}
	n := len(recs)
	for i, r := range recs {
		c := cells[i+2]
		if c["A"] != r.a || c["B"] != r.b || c["C"] != r.c || c["D"] != r.d || c["E"] != r.e {
			return fmt.Errorf("一覧の %d 行目が思ったとおりになりません", i+2)
		}
	}
	if oldCells != nil {
		last := listLastRow(oldCells)
		if listLastRow(cells) != last+n {
			return fmt.Errorf("一覧の行の数が合いません")
		}
		for r := 2; r <= last; r++ {
			for _, col := range []string{"A", "B", "C", "D", "E"} {
				if cells[r+n][col] != oldCells[r][col] {
					return fmt.Errorf("もとの一覧の %d 行目が変わってしまうため、書きません", r)
				}
			}
		}
	} else if listLastRow(cells) != n+1 {
		return fmt.Errorf("一覧の行の数が合いません")
	}
	return nil
}

// verifyBook: 新しく作ったブックを確かめる。もとの部品はバイトのまま、シートの並びは「一覧」＋もとの並び（extraFront があればその名前が次）
func verifyBook(oldData, newData []byte, recs []listRec, extraFront []string) error {
	if err := checkPkgXML(newData); err != nil {
		return err
	}
	op, err := readPkg(oldData)
	if err != nil {
		return err
	}
	np, err := readPkg(newData)
	if err != nil {
		return err
	}
	changed := map[string]bool{"xl/workbook.xml": true, "xl/_rels/workbook.xml.rels": true, "[Content_Types].xml": true, "docProps/app.xml": true, "xl/styles.xml": true}
	for _, e := range op.entries {
		if changed[e.name] {
			continue
		}
		a, _ := op.get(e.name)
		b, ok := np.get(e.name)
		if !ok || a != b {
			return fmt.Errorf("もとの部品（%s）が変わってしまうため、書きません", e.name)
		}
	}
	oldSt, _ := op.get("xl/styles.xml")
	nsx, _ := np.get("xl/styles.xml")
	if !stylesOnlyAppended(oldSt, nsx) {
		return fmt.Errorf("書式（styles.xml）のもとの部分が変わってしまうため、書きません")
	}
	on, err := kxSheetNames(op)
	if err != nil {
		return err
	}
	nn, err := kxSheetNames(np)
	if err != nil {
		return err
	}
	want := append([]string{keikaXList}, on...)
	if len(nn) != len(want) {
		return fmt.Errorf("シートの数が合いません")
	}
	for i := range want {
		if nn[i] != want[i] {
			return fmt.Errorf("シートの並びが思ったとおりになりません（%v）", nn)
		}
	}
	// シートの部品：もとのシートは同じ部品のまま（バイトのまま）
	osh, _ := wbSheets(op)
	nsh, _ := wbSheets(np)
	for i := range osh {
		a, _ := op.get(osh[i].target)
		b, _ := np.get(nsh[i+1].target)
		if a != b {
			return fmt.Errorf("シート「%s」の中身が変わってしまうため、書きません", osh[i].name)
		}
	}
	// 一覧
	lx, _ := np.get(nsh[0].target)
	if err := verifyListRows(lx, sharedStrings(np), recs, nil); err != nil {
		return err
	}
	return verifyBookWorkbook(np, nn)
}

// verifyBookWorkbook: シートの番号・部品・型・開くシートの番号・印刷範囲が、つじつまの合う形か
func verifyBookWorkbook(np *ooPkg, nn []string) error {
	wb, _ := np.get("xl/workbook.xml")
	rels, _ := np.get("xl/_rels/workbook.xml.rels")
	ct, _ := np.get("[Content_Types].xml")
	rm := relsMap(rels)
	ids, rids := map[string]bool{}, map[string]bool{}
	for _, t := range reSheetTag.FindAllString(wb, -1) {
		id, rid := attr(t, "sheetId"), attr(t, "r:id")
		if ids[id] || rids[rid] {
			return fmt.Errorf("シートの番号が重なっています")
		}
		ids[id], rids[rid] = true, true
		tg := resolveTarget("xl", rm[rid][1])
		if !np.has(tg) {
			return fmt.Errorf("シートの部品（%s）がありません", tg)
		}
		if !strings.Contains(ct, `PartName="/`+tg+`"`) {
			return fmt.Errorf("[Content_Types].xml にシート（%s）がありません", tg)
		}
	}
	if m := regexp.MustCompile(`<workbookView\b[^>]*?\bactiveTab="(\d+)"`).FindStringSubmatch(wb); m != nil && atoi(m[1]) >= len(nn) {
		return fmt.Errorf("開くシートの番号が正しくありません")
	}
	// 開いたとき選ばれているシートは、ちょうど1つ（2つ以上だと、複数のシートをいっしょに書き換えてしまう）
	sheets, _ := wbSheets(np)
	selected := 0
	for _, sh := range sheets {
		if x, ok := np.get(sh.target); ok && strings.Contains(x, `tabSelected="1"`) {
			selected++
		}
	}
	if selected > 1 {
		return fmt.Errorf("開いたとき選ばれているシートが2つ以上になります")
	}
	for _, d := range reDefName.FindAllString(wb, -1) {
		if v := attr(d, "localSheetId"); v != "" {
			if i, err := strconv.Atoi(v); err != nil || i < 0 || i >= len(nn) {
				return fmt.Errorf("名前（%s）の localSheetId=%s がシートの数を超えています", attr(d, "name"), v)
			}
		}
	}
	app, _ := np.get("docProps/app.xml")
	if app != "" {
		pos := strings.Index(app, "<TitlesOfParts>")
		for _, n := range nn {
			i := strings.Index(app[pos:], ">"+encodeText(n)+"</vt:lpstr>")
			if i < 0 {
				return fmt.Errorf("docProps/app.xml にシート名（%s）がありません", n)
			}
			pos += i + 1
		}
	}
	return nil
}

// verifyBookListInsert: 一覧に行を足した結果（step1 → out）を確かめる。違うのは、一覧のシートと styles.xml だけ
func verifyBookListInsert(step1, out []byte, listPart string, recs []listRec) error {
	if err := checkPkgXML(out); err != nil {
		return err
	}
	op, err := readPkg(step1)
	if err != nil {
		return err
	}
	np, err := readPkg(out)
	if err != nil {
		return err
	}
	for _, e := range op.entries {
		if e.name == listPart || e.name == "xl/styles.xml" {
			continue
		}
		a, _ := op.get(e.name)
		b, ok := np.get(e.name)
		if !ok || a != b {
			return fmt.Errorf("もとの部品（%s）が変わってしまうため、書きません", e.name)
		}
	}
	if len(np.entries) != len(op.entries) {
		return fmt.Errorf("部品の数が変わってしまうため、書きません")
	}
	oldSt, _ := op.get("xl/styles.xml")
	nsx, _ := np.get("xl/styles.xml")
	if !stylesOnlyAppended(oldSt, nsx) {
		return fmt.Errorf("書式（styles.xml）のもとの部分が変わってしまうため、書きません")
	}
	ox, _ := op.get(listPart)
	nx, _ := np.get(listPart)
	return verifyListRows(nx, sharedStrings(np), recs, listCells(ox, sharedStrings(op)))
}

// ---------------- 開いたとき最初に「一覧」を出す（改善29 統括リーダーの指示） ----------------

var reTabSel = regexp.MustCompile(`\s+tabSelected="[01]"`)

// openAtList: workbook.xml の activeTab を「一覧」に、tabSelected を「一覧」だけにする。直すのはこの2点だけで、シートの中身は変えない
// （確かめる：workbook.xml は activeTab・firstSheet 以外が同じ／各シートは tabSelected 以外が同じ）
func openAtList(data []byte) ([]byte, error) {
	p, err := readPkg(data)
	if err != nil {
		return nil, err
	}
	lt, ok := listSheetEntry(p)
	if !ok {
		return nil, fmt.Errorf("「%s」のシートがありません", keikaXList)
	}
	sheets, err := wbSheets(p)
	if err != nil {
		return nil, err
	}
	if len(sheets) == 0 || sheets[0].target != lt {
		return nil, fmt.Errorf("「%s」がいちばん左にありません", keikaXList)
	}
	wb, _ := p.get("xl/workbook.xml")
	oldWb := wb
	reView := regexp.MustCompile(`<workbookView\b[^>]*?/?>`)
	loc := reView.FindStringIndex(wb)
	if loc == nil {
		return nil, fmt.Errorf("workbook.xml に workbookView がありません")
	}
	tag := wb[loc[0]:loc[1]]
	set := func(tag, name, val string) string {
		re := regexp.MustCompile(`\b` + name + `="\d+"`)
		if re.MatchString(tag) {
			return re.ReplaceAllString(tag, name+`="`+val+`"`)
		}
		if name == "firstSheet" {
			return tag
		}
		if strings.HasSuffix(tag, "/>") {
			return strings.TrimSuffix(tag, "/>") + ` ` + name + `="` + val + `"/>`
		}
		return strings.TrimSuffix(tag, ">") + ` ` + name + `="` + val + `">`
	}
	ntag := set(set(tag, "activeTab", "0"), "firstSheet", "0")
	wb = wb[:loc[0]] + ntag + wb[loc[1]:]
	p.set("xl/workbook.xml", wb)
	for i, sh := range sheets {
		x, ok := p.get(sh.target)
		if !ok {
			return nil, fmt.Errorf("シートの部品（%s）がありません", sh.target)
		}
		nx := reTabSel.ReplaceAllString(x, "")
		if i == 0 {
			re := regexp.MustCompile(`<sheetView\b`)
			if !re.MatchString(nx) {
				return nil, fmt.Errorf("「一覧」に sheetView がありません")
			}
			nx = re.ReplaceAllString(nx, `<sheetView tabSelected="1"`)
			nx = strings.Replace(nx, `<sheetView tabSelected="1" `, `<sheetView tabSelected="1" `, 1)
		}
		if nx != x {
			p.set(sh.target, nx)
		}
		if reTabSel.ReplaceAllString(nx, "") != reTabSel.ReplaceAllString(x, "") {
			return nil, fmt.Errorf("シートの中身が変わってしまうため、書きません")
		}
	}
	if reView.ReplaceAllString(wb, "") != reView.ReplaceAllString(oldWb, "") {
		return nil, fmt.Errorf("workbook.xml が思ったとおりになりません")
	}
	out, err := p.bytes()
	if err != nil {
		return nil, err
	}
	if err := checkPkgXML(out); err != nil {
		return nil, err
	}
	return out, nil
}
