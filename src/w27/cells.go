package main

import (
	"archive/zip"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// ---- 登録セル（様式ごとの個人情報セル） ----

const defaultCellConfig = `# 登録セル：様式ごとに、個人情報が入るセルを書いておきます。
# ここに書いたセルは、中身が何であっても必ず【記号】に置き換えます。
# 書き方：シート名 ／ 項目名 ／ セル（「,」区切り）をタブで区切ります。「#」で始まる行はメモです。
# 結合セルは左上のセルを書きます。このセルを参照している計算式のセル（年齢・自動転記など）も自動で対象になります。
#
# ■ アセスメントシート（4ページ版）
フェイスシート	氏名	G5,G6
フェイスシート	生年月日	W6,Y6,AA6,AC6
フェイスシート	郵便番号	H7,K7,H9,K9,H20,K20,H23,K23,H26,K26
フェイスシート	住所	N7,N8,N9,N10,N20,N21,N23,N24,N26,N27
フェイスシート	電話	AB7,AB8,AB9,AB10,AB20,AB21,AB23,AB24,AB26,AB27
フェイスシート	家族氏名	B14,B15,B16,B17
フェイスシート	緊急連絡先氏名	G19,G22,G25
フェイスシート	番号	Y36,AG36,Y37,AG37
アセスメント2	被保険者番号	L10
アセスメント4	主な介護者氏名	H24
#
# ■ ケアプラン様式（要介護）
2表	利用者名	E3
3表	利用者名	J2
要点	利用者名	D3
モニタリング表	利用者名	D3
モニタリング表	生年月日	D4
モニタリング表	住所	T4,T5
#
# ■ 要支援様式
D表	利用者名	J2
E表	利用者名	D3
評価表	利用者名	D3
評価表	生年月日	D4
評価表	住所	T4,T5
`

type cellConfig map[string]map[string]string // sheet -> cell -> 項目名

// builtinCells: 登録セル.txt に書いていなくても、いつも個人情報として置き換えるセル（改善27の2）。
// 第1表（ケアプランのブックの「1表」シート・別ファイルの第1表_原紙の「第1表」シート）：利用者名・生年月日・住所・署名
var builtinCells = map[string]map[string]string{
	"1表":  {"C3": "利用者名", "L3": "生年月日", "U3": "住所", "AB19": "署名"},
	"第1表": {"C3": "利用者名", "L3": "生年月日", "U3": "住所", "AB19": "署名"},
}


func loadCellConfig(path string) (cellConfig, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if werr := os.WriteFile(path, []byte(bom+strings.ReplaceAll(defaultCellConfig, "\n", "\r\n")), 0600); werr != nil {
			return nil, werr
		}
		data = []byte(defaultCellConfig)
	} else if err != nil {
		return nil, err
	}
	for _, w := range cellConfigWarnings(string(data)) {
		fmt.Println("※ 登録セル.txt の", w)
	}
	return parseCellConfig(strings.TrimPrefix(string(data), bom)), nil
}

var reCellRef = regexp.MustCompile(`^[A-Z]{1,3}[0-9]+$`)

func parseCellConfig(s string) cellConfig {
	cfg := cellConfig{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(strings.TrimSpace(line), "#") || strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		sheet, label := strings.TrimSpace(f[0]), sanitizePrefix(f[1])
		for _, c := range strings.FieldsFunc(f[2], func(r rune) bool { return r == ',' || r == '、' || r == ' ' || r == '，' }) {
			c = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(c), "$", ""))
			if !reCellRef.MatchString(c) {
				continue
			}
			if cfg[sheet] == nil {
				cfg[sheet] = map[string]string{}
			}
			cfg[sheet][c] = label
		}
	}
	return cfg
}

var (
	reFText   = regexp.MustCompile(`(?s)<f([^>]*)>(.*?)</f>|<f([^>]*)/>`)
	reSharedI = regexp.MustCompile(`\bsi="([0-9]+)"`)
	reRefIn   = regexp.MustCompile(`(?:(?:'((?:[^']|'')+)'|([^\s'!(),=+\-*/&<>:;"{}\[\]^%]+))!)?\$?([A-Z]{1,3})\$?([0-9]+)`)
)

type cellKey struct{ sheet, ref string }

type fcell struct {
	sheet, ref, formula, si string
	shared                  bool
}

// registeredTargets returns all cells to mask: the configured ones plus formula
// cells that (directly or indirectly) refer to them. Value = 項目名.
func registeredTargets(zr *zip.Reader, sheetNames map[string]string, cfg cellConfig) map[cellKey]string {
	targets := map[cellKey]string{}
	nameToPart := map[string]string{}
	var sst []string
	for part, n := range sheetNames {
		nameToPart[n] = part
		for ref, label := range cfg[n] {
			targets[cellKey{n, ref}] = label
		}
		// 第1表のシート（「1表」「第1表」）：見出しの位置が原紙どおりのときだけ、利用者名・生年月日・住所・署名をいつも記号にする（改善27の2・点検役 3）
		if bc := builtinCells[n]; bc != nil {
			x := string(zipRead(zr, part))
			if sst == nil {
				sst = readSST(zr)
			}
			if kxCell(x, "A3", sst) == "利用者名" && kxCell(x, "K3", sst) == "生年月日" && kxCell(x, "T3", sst) == "住所" {
				for ref, label := range bc {
					if _, ok := targets[cellKey{n, ref}]; !ok {
						targets[cellKey{n, ref}] = label
					}
				}
			}
		}
	}
	if len(targets) == 0 {
		return targets
	}
	var formulas []fcell
	for part, sheet := range sheetNames {
		data := zipRead(zr, part)
		for _, m := range reCellFull.FindAllSubmatch(data, -1) {
			inner := m[4]
			fm := reFText.FindSubmatch(inner)
			if fm == nil {
				continue
			}
			fc := fcell{sheet: sheet, ref: string(m[1]) + string(m[2])}
			attrs := string(fm[1]) + string(fm[3])
			fc.formula = decodeEntities(string(fm[2]))
			if strings.Contains(attrs, `t="shared"`) {
				fc.shared = true
				if s := reSharedI.FindStringSubmatch(attrs); s != nil {
					fc.si = s[1]
				}
			}
			formulas = append(formulas, fc)
		}
	}
	// shared formula masters: si -> formula text
	sharedText := map[string]string{}
	for _, f := range formulas {
		if f.shared && f.formula != "" {
			sharedText[f.sheet+"|"+f.si] = f.formula
		}
	}
	for iter := 0; iter < 6; iter++ {
		added := false
		for _, f := range formulas {
			k := cellKey{f.sheet, f.ref}
			if _, done := targets[k]; done {
				continue
			}
			text := f.formula
			if text == "" && f.shared {
				text = sharedText[f.sheet+"|"+f.si]
			}
			for _, r := range reRefIn.FindAllStringSubmatch(text, -1) {
				sh := f.sheet
				if r[1] != "" {
					sh = strings.ReplaceAll(r[1], "''", "'")
				} else if r[2] != "" {
					sh = r[2]
				}
				if lbl, ok := targets[cellKey{sh, r[3] + r[4]}]; ok {
					_ = lbl
					targets[k] = "自動計算"
					added = true
					break
				}
			}
		}
		if !added {
			break
		}
	}
	return targets
}

var reCellOpen = regexp.MustCompile(`^<c r="([A-Z]+[0-9]+)"([^>]*?)(/?)>`)

// maskRegisteredCells rewrites registered cells of one sheet part.
func (m *masker) maskRegisteredCells(data []byte, sheet string, targets map[cellKey]string, sst []string) ([]byte, []match) {
	var reps []match
	out := reCellFull.ReplaceAllFunc(data, func(cell []byte) []byte {
		mm := reCellFull.FindSubmatch(cell)
		ref := string(mm[1]) + string(mm[2])
		label, ok := targets[cellKey{sheet, ref}]
		if !ok {
			return cell
		}
		attrs := string(mm[3])
		inner := string(mm[4])
		typ := ""
		if t := reTypeAttr.FindStringSubmatch(attrs); t != nil {
			typ = t[1]
		}
		if typ == "b" || typ == "e" {
			return cell
		}
		attrsNoT := strings.TrimRight(reTypeAttrSp.ReplaceAllString(attrs, ""), "/")
		v := ""
		if x := reCellV.FindStringSubmatch(inner); x != nil {
			v = decodeEntities(x[1])
		}
		f := reFText.FindString(inner)
		text := v
		numeric := typ == "" || typ == "n"
		switch typ {
		case "s":
			var i int
			if _, err := fmt.Sscanf(v, "%d", &i); err == nil && i < len(sst) {
				text = sst[i]
			}
		case "inlineStr":
			if is := reCellIs.FindStringSubmatch(inner); is != nil {
				text = decodeEntities(reTagStrip.ReplaceAllString(is[1], ""))
			}
		}
		if strings.TrimSpace(text) == "" || isCommonValue(text) {
			return cell // 空欄や「なし」「同上」などは個人情報ではないのでそのまま
		}
		if f != "" {
			// 計算式のセル：式は残し、表示されている値だけ記号にする
			// 文字の値は、式でないセルと同じく、まず本文と同じ置き換えを試す。全部が記号になれば本文と同じ記号
			// （【利用者001】など）にする。本文と別の記号（【利用者名001】）だとAIが別人と誤解するため（改善13 No.9）
			key := "値|"
			if numeric {
				key = "数|"
			}
			tok := ""
			if !numeric {
				if masked, ok := m.maskAllByText(text); ok {
					tok = masked
				}
			}
			if tok == "" {
				tok = m.cb.get(key+label+"|"+text, text, label)
			}
			reps = append(reps, match{kind: "登録セル（" + label + "）", orig: text, repl: tok})
			return []byte(`<c r="` + ref + `"` + attrsNoT + ` t="str">` + f + `<v>` + encodeText(tok) + `</v></c>`)
		}
		var repl string
		if numeric {
			repl = m.cb.get("数|"+label+"|"+text, text, label)
		} else {
			if masked, ok := m.maskAllByText(text); ok {
				repl = masked
			} else {
				repl = m.cb.get("文|"+label+"|"+text, text, label)
			}
		}
		reps = append(reps, match{kind: "登録セル（" + label + "）", orig: text, repl: repl})
		return []byte(`<c r="` + ref + `"` + attrsNoT + ` t="inlineStr"><is><t xml:space="preserve">` + encodeText(repl) + `</t></is></c>`)
	})
	return out, reps
}

// maskAllByText: 本文と同じ置き換えを試し、残りが空（全部が記号になった）ならその結果を返す
func (m *masker) maskAllByText(text string) (string, bool) {
	masked := applyMatches(text, m.findMatches(text, true))
	rest := strings.TrimSpace(reTokenAny.ReplaceAllString(masked, ""))
	rest = strings.Trim(rest, " 　、，,・()（）")
	return masked, masked != text && rest == ""
}

var (
	reTypeAttrSp   = regexp.MustCompile(`\s+t="[^"]*"`)
	reInlineTokCel = regexp.MustCompile(`<c r="([A-Z]+[0-9]+)"([^>]*?) t="inlineStr"([^>]*)><is><t[^>]*>([^<]+)</t></is></c>`)
	reCalcPr       = regexp.MustCompile(`<calcPr\b[^>]*?/?>`)
	reSCell        = regexp.MustCompile(`<c r="([A-Z]+[0-9]+)"([^>]*?) t="s"([^>]*)><v>([0-9]+)</v></c>`)
	reStrTokCel    = regexp.MustCompile(`<c r="([A-Z]+[0-9]+)"([^>]*?) t="str"([^>]*)>(<f[^>]*/>|<f[^>]*>[^<]*</f>)<v>([^<]+)</v></c>`)
)

// restoreNumericCells turns cells holding only a numeric-origin token back into numbers.
func restoreNumericCells(x string, cb *codeBook, sst []string) (string, int) {
	n := 0
	// 共有文字列になった記号（AIがExcelを保存し直した場合）
	x = reSCell.ReplaceAllStringFunc(x, func(c string) string {
		mm := reSCell.FindStringSubmatch(c)
		var i int
		if _, err := fmt.Sscanf(mm[4], "%d", &i); err != nil || i >= len(sst) {
			return c
		}
		tok := strings.TrimSpace(sst[i])
		if !cb.numeric[tok] {
			return c
		}
		n++
		return `<c r="` + mm[1] + `"` + mm[2] + mm[3] + `><v>` + cb.codeToOrig[tok] + `</v></c>`
	})
	x = reStrTokCel.ReplaceAllStringFunc(x, func(c string) string {
		mm := reStrTokCel.FindStringSubmatch(c)
		tok := strings.TrimSpace(decodeEntities(mm[5]))
		if !cb.numeric[tok] {
			return c
		}
		n++
		return `<c r="` + mm[1] + `"` + mm[2] + mm[3] + `>` + mm[4] + `<v>` + cb.codeToOrig[tok] + `</v></c>`
	})
	out := reInlineTokCel.ReplaceAllStringFunc(x, func(c string) string {
		mm := reInlineTokCel.FindStringSubmatch(c)
		tok := strings.TrimSpace(decodeEntities(mm[4]))
		if !cb.numeric[tok] {
			return c
		}
		n++
		return `<c r="` + mm[1] + `"` + mm[2] + mm[3] + `><v>` + cb.codeToOrig[tok] + `</v></c>`
	})
	return out, n
}

// forceRecalc asks Excel to recalculate all formulas when the file is opened.
func forceRecalc(x string) string {
	if m := reCalcPr.FindString(x); m != "" {
		if strings.Contains(m, "fullCalcOnLoad") {
			return x
		}
		nm := strings.Replace(m, "<calcPr", `<calcPr fullCalcOnLoad="1"`, 1)
		return strings.Replace(x, m, nm, 1)
	}
	for _, next := range []string{"<oleSize", "<customWorkbookViews", "<pivotCaches", "<smartTagPr", "<smartTagTypes", "<webPublishing", "<fileRecoveryPr", "<webPublishObjects", "<extLst", "</workbook>"} {
		if i := strings.Index(x, next); i >= 0 {
			return x[:i] + `<calcPr fullCalcOnLoad="1"/>` + x[i:]
		}
	}
	return x
}

var reSI = regexp.MustCompile(`(?s)<si>.*?</si>|<si/>`)

// orphanSST returns shared-string indices used only by registered cells.
func orphanSST(zr *zip.Reader, sheetNames map[string]string, targets map[cellKey]string, sst []string) map[int]bool {
	reg := map[int]bool{}
	other := map[int]bool{}
	for part, sheet := range sheetNames {
		for _, m := range reCellFull.FindAllSubmatch(zipRead(zr, part), -1) {
			t := reTypeAttr.FindSubmatch(m[3])
			if t == nil || string(t[1]) != "s" {
				continue
			}
			v := reCellV.FindSubmatch(m[4])
			if v == nil {
				continue
			}
			var i int
			if _, err := fmt.Sscanf(string(v[1]), "%d", &i); err != nil {
				continue
			}
			if _, ok := targets[cellKey{sheet, string(m[1]) + string(m[2])}]; ok && !(i < len(sst) && isCommonValue(sst[i])) && !reFText.Match(m[4]) {
				reg[i] = true
			} else {
				other[i] = true
			}
		}
	}
	out := map[int]bool{}
	for i := range reg {
		if !other[i] {
			out[i] = true
		}
	}
	return out
}

// blankSST empties the given shared strings (they are no longer referenced).
func blankSST(data []byte, idx map[int]bool) []byte {
	if len(idx) == 0 {
		return data
	}
	n := -1
	return reSI.ReplaceAllFunc(data, func(si []byte) []byte {
		n++
		if idx[n] {
			return []byte("<si><t></t></si>")
		}
		return si
	})
}

// registeredValues returns [text, 項目名] of non-formula text cells that are registered.
func registeredValues(zr *zip.Reader, sheetNames map[string]string, targets map[cellKey]string, sst []string) [][2]string {
	var out [][2]string
	parts := make([]string, 0, len(sheetNames))
	for p := range sheetNames {
		parts = append(parts, p)
	}
	sort.Slice(parts, func(a, b int) bool {
		if len(parts[a]) != len(parts[b]) {
			return len(parts[a]) < len(parts[b])
		}
		return parts[a] < parts[b]
	})
	for _, part := range parts {
		sheet := sheetNames[part]
		for _, m := range reCellFull.FindAllSubmatch(zipRead(zr, part), -1) {
			label, ok := targets[cellKey{sheet, string(m[1]) + string(m[2])}]
			if !ok || label == "自動計算" || reFText.Match(m[4]) {
				continue
			}
			typ := ""
			if t := reTypeAttr.FindSubmatch(m[3]); t != nil {
				typ = string(t[1])
			}
			text := ""
			switch typ {
			case "s":
				if v := reCellV.FindSubmatch(m[4]); v != nil {
					var i int
					if _, err := fmt.Sscanf(string(v[1]), "%d", &i); err == nil && i < len(sst) {
						text = sst[i]
					}
				}
			case "inlineStr":
				if is := reCellIs.FindSubmatch(m[4]); is != nil {
					text = decodeEntities(reTagStrip.ReplaceAllString(string(is[1]), ""))
				}
			case "str":
				if v := reCellV.FindSubmatch(m[4]); v != nil {
					text = decodeEntities(string(v[1]))
				}
			}
			if strings.TrimSpace(text) != "" {
				out = append(out, [2]string{text, label})
			}
		}
	}
	return out
}

// cellConfigWarnings lists lines that could not be read.
func cellConfigWarnings(s string) []string {
	var w []string
	for i, line := range strings.Split(strings.TrimPrefix(s, bom), "\n") {
		line = strings.TrimRight(line, "\r")
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if len(strings.Split(line, "\t")) < 3 {
			w = append(w, fmt.Sprintf("%d行目：タブで3つに区切られていません（%s）", i+1, t))
		}
	}
	return w
}
