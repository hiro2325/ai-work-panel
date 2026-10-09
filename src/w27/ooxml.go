package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

func stackHas(stack []elem, name string) bool {
	for _, e := range stack {
		if e.name == name {
			return true
		}
	}
	return false
}

func setOf(names ...string) func(string) bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return func(s string) bool { return m[s] }
}

var (
	reWordPart       = regexp.MustCompile(`^word/(document|header\d*|footer\d*|footnotes|endnotes|comments)\.xml$`)
	reSheetPart      = regexp.MustCompile(`^xl/worksheets/sheet\d+\.xml$`)
	reOtherSheetPart = regexp.MustCompile(`^xl/(chartsheets|dialogsheets|macrosheets)/sheet\d+\.xml$`) // ヘッダー・フッターだけ調べる（改善5）
	reCmtPart        = regexp.MustCompile(`^xl/comments[^/]*\.xml$|^xl/comments/[^/]+\.xml$`)
	reThrPart        = regexp.MustCompile(`^xl/threadedComments/.*\.xml$`)
	reTypeAttr       = regexp.MustCompile(`\bt="([^"]*)"`)
	reDiagPart       = regexp.MustCompile(`^(word|xl)/(charts|diagrams)/[^/]+\.xml$`)
)

// ruleFor returns the scan rule for a zip entry, or nil to leave the entry as-is.
func ruleFor(name string) *partRule {
	switch {
	case name == "docProps/core.xml":
		return &partRule{isTarget: func(n, a string, s []elem) bool {
			return n == "dc:title" || n == "dc:subject" || n == "cp:keywords" || n == "dc:description" || n == "cp:category"
		}}
	case reWordPart.MatchString(name):
		r := &partRule{
			isTarget: func(n, a string, s []elem) bool {
				return n == "w:t" || n == "w:delText" || n == "a:t" || n == "w:instrText" || n == "w:delInstrText"
			},
			isGroup:       setOf("w:p", "a:p"),
			preserveSpace: setOf("w:t", "w:delText", "w:instrText", "w:delInstrText"),
			isBlankCtx:    func(s []elem) bool { return stackHas(s, "w:rt") },
		}
		if reWordHdrPart.MatchString(name) {
			r.keepEntities = func(string) bool { return true }
		}
		return r
	case reDiagPart.MatchString(name):
		return &partRule{
			isTarget: func(n, a string, s []elem) bool {
				return n == "a:t" || (n == "c:v" && stackHas(s, "c:strCache"))
			},
			isGroup: setOf("a:p", "c:pt"),
		}
	case name == "xl/sharedStrings.xml":
		return &partRule{
			isTarget: func(n, a string, s []elem) bool {
				return n == "t" && !stackHas(s, "rPh")
			},
			isGroup:       setOf("si"),
			isRemovable:   setOf("rPh"),
			preserveSpace: setOf("t"),
		}
	case reSheetPart.MatchString(name):
		return &partRule{
			isTarget: func(n, a string, s []elem) bool {
				switch n {
				case "t":
					return stackHas(s, "is") && !stackHas(s, "rPh")
				case "v":
					if len(s) > 0 && s[len(s)-1].name == "c" {
						m := reTypeAttr.FindStringSubmatch(s[len(s)-1].attrs)
						return m != nil && m[1] == "str"
					}
				case "oddHeader", "oddFooter", "evenHeader", "evenFooter", "firstHeader", "firstFooter":
					return true
				}
				return false
			},
			isGroup:       setOf("is"),
			isRemovable:   setOf("rPh"),
			preserveSpace: setOf("t"),
			keepEntities:  func(n string) bool { return hdrElems[n] },
		}
	case reOtherSheetPart.MatchString(name):
		return &partRule{
			isTarget:     func(n, a string, s []elem) bool { return hdrElems[n] },
			keepEntities: func(n string) bool { return hdrElems[n] },
		}
	case reCmtPart.MatchString(name):
		return &partRule{
			isTarget: func(n, a string, s []elem) bool {
				return n == "t" && stackHas(s, "commentList") && !stackHas(s, "rPh")
			},
			isGroup:       setOf("text"),
			isRemovable:   setOf("rPh"),
			preserveSpace: setOf("t"),
		}
	case reThrPart.MatchString(name):
		return &partRule{isTarget: func(n, a string, s []elem) bool { return n == "text" }}
	case strings.HasPrefix(name, "xl/drawings/drawing") && strings.HasSuffix(name, ".xml"):
		return &partRule{
			isTarget: func(n, a string, s []elem) bool { return n == "a:t" },
			isGroup:  setOf("a:p"),
		}
	}
	return nil
}

type fileResult struct {
	reports       []groupReport
	mediaCount    int
	changed       bool
	numSus        []string
	rawCount      int
	regChanged    bool
	leftovers     []string
	unprocessable []string
	birthSus      []string
	thumbRemoved  bool
	leftHits      []leftHit // 残りの場所と種類（改善12 No.33）
}

// rawFunc replaces literal strings in the raw XML of every part (sheet names, formulas, attributes …).
type rawFunc func(part, xml string) (string, int)

// checkFunc reports forbidden strings found in a part's (entity-decoded) raw text.
type checkFunc func(part, text string) []string

var reBadDefinedName = regexp.MustCompile(`<definedName\s[^>]*name="[^"]*【`)

func isXMLPart(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".xml") || strings.HasSuffix(l, ".rels") || strings.HasSuffix(l, ".vml")
}

var reNumCell = regexp.MustCompile(`<c r="([A-Z]+[0-9]+)"(?:\s[^>]*?)?>(?:<f[^>]*/>|<f[^>]*>[^<]*</f>)?<v>(-?[0-9]{8,})</v>`)

// processOOXML reads src (docx/xlsx/xlsm), transforms text, writes dst.
// zipOpts carries mode-specific helpers (both may be nil).
type zipOpts struct {
	m         *masker    // 置き換え：登録セルの処理に使う
	cfg       cellConfig // 置き換え：登録セル
	restoreCB *codeBook  // 元に戻す：数値セルの復元・再計算
}

func processOOXML(src, dst string, tf transformFunc, raw rawFunc, check checkFunc, opts ...*zipOpts) (*fileResult, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("ファイルを読めません: %v", err)
	}
	res := &fileResult{}
	var o *zipOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	out, err := processZip(data, "", tf, raw, check, res, 0, o)
	if err != nil {
		return nil, err
	}
	if o != nil && o.m != nil {
		// 置き換えた後のファイルで「生年月日」の隣に数字が残っていないか確認
		if zr2, err := zip.NewReader(bytes.NewReader(out), int64(len(out))); err == nil {
			hid2, _, names := sheetVisibility(zr2)
			for p, n := range names {
				if hid2[p] && masterSheetNames[n] {
					delete(names, p)
				}
			}
			res.birthSus = append(res.birthSus, birthCells(zr2, names, "")...)
		}
	}
	if err := os.WriteFile(dst, out, 0644); err != nil {
		return nil, fmt.Errorf("保存できません: %v", err)
	}
	return res, nil
}

func readSST(zr *zip.Reader) []string {
	sstData := zipRead(zr, "xl/sharedStrings.xml")
	if sstData == nil {
		return nil
	}
	nodes, _, ok := scanXML(sstData, *ruleFor("xl/sharedStrings.xml"))
	if !ok {
		return nil
	}
	texts := map[int]string{}
	maxg := -1
	for _, n := range nodes {
		texts[n.group] += n.text
		if n.group > maxg {
			maxg = n.group
		}
	}
	sst := make([]string, maxg+1)
	for g, t := range texts {
		sst[g] = t
	}
	return sst
}

var (
	reThumbRel  = regexp.MustCompile(`<Relationship\s[^>]*Target="[^"]*thumbnail[^"]*"[^>]*/>`)
	reFormulaEl = regexp.MustCompile(`(<(?:f|definedName|formula1|formula2|formula|xm:f|c:f)(?:\s[^>]*)?>)([^<]*)(</)`)
	reUnquoted  = regexp.MustCompile(`(^|[^'A-Za-z0-9_.])([^\s'!(),=+\-*/&<>:;"{}\[\]^%]*【[^\s'!(),=+\-*/&<>:;"{}\[\]^%]*)!`)
)

// quoteSheetRefs puts quotes around sheet names that now contain 【】 inside formulas.
func quoteSheetRefs(x string) string {
	if !strings.Contains(x, "【") {
		return x
	}
	return reFormulaEl.ReplaceAllStringFunc(x, func(el string) string {
		m := reFormulaEl.FindStringSubmatch(el)
		inner := reUnquoted.ReplaceAllString(m[2], "${1}'${2}'!")
		return m[1] + inner + m[3]
	})
}

func processZip(data []byte, prefix string, tf transformFunc, raw rawFunc, check checkFunc, res *fileResult, depth int, o *zipOpts) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("ファイルを開けません（壊れているか、形式が違います）: %v", err)
	}
	hiddenSheets, visibleSST, allSheetNames := sheetVisibility(zr)
	// 様式に入っている隠しのマスタ（郵便番号マスタなど）は中身を調べずにそのまま写す（改善9：置き換えが遅い対策）
	masterParts, masterOnlySST := masterSheets(zr, hiddenSheets, allSheetNames)
	sheetNames := allSheetNames
	if len(masterParts) > 0 {
		sheetNames = map[string]string{}
		for k, v := range allSheetNames {
			if !masterParts[k] {
				sheetNames[k] = v
			}
		}
	}
	var regTargets map[cellKey]string
	var sstTexts []string
	var orphans map[int]bool
	var restoreSST []string
	if o != nil && o.m != nil && len(sheetNames) > 0 {
		regTargets = registeredTargets(zr, sheetNames, o.cfg)
		sstTexts = readSST(zr)
		orphans = orphanSST(zr, sheetNames, regTargets, sstTexts)
		for i := range orphans {
			if masterOnlySST[i] || masterUsedSST(zr, masterParts)[i] {
				delete(orphans, i)
			}
		}
		// 登録セルの文字は、ブック全体で置き換える語に加える
		for _, rv := range registeredValues(zr, sheetNames, regTargets, sstTexts) {
			o.m.addDynamic(rv[0], rv[1])
		}
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		lower := strings.ToLower(f.Name)
		if strings.Contains(lower, "/media/") {
			res.mediaCount++
		}
		if strings.HasPrefix(lower, "docprops/thumbnail") {
			res.thumbRemoved = true
			continue // 1ページ目の縮小画像は削除
		}
		if masterParts[f.Name] {
			if err := zw.Copy(f); err != nil {
				return nil, err
			}
			continue
		}
		if !isXMLPart(f.Name) {
			ext := strings.ToLower(path.Ext(f.Name))
			switch {
			case ext == ".docx" || ext == ".xlsx" || ext == ".xlsm" || ext == ".docm":
				if depth >= 2 {
					res.unprocessable = append(res.unprocessable, prefix+f.Name+"（入れ子が深すぎる埋め込み）")
					break
				}
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				inner, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					return nil, err
				}
				nout, err := processZip(inner, prefix+f.Name+" の中の ", tf, raw, check, res, depth+1, o)
				if err != nil {
					return nil, fmt.Errorf("埋め込まれたファイル（%s）: %v", f.Name, err)
				}
				w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: f.Method, Modified: f.Modified})
				if err != nil {
					return nil, err
				}
				if _, err := w.Write(nout); err != nil {
					return nil, err
				}
				continue
			case strings.Contains(lower, "/embeddings/"):
				res.unprocessable = append(res.unprocessable, prefix+f.Name+"（中を読めない埋め込みファイル）")
			case strings.HasSuffix(lower, "vbaproject.bin"):
				res.unprocessable = append(res.unprocessable, prefix+"マクロ（中を確認できません）")
			}
			if err := zw.Copy(f); err != nil {
				return nil, err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		if reSheetPart.MatchString(f.Name) {
			for _, mm := range reNumCell.FindAllSubmatch(data, -1) {
				open := mm[0][:bytes.IndexByte(mm[0], '>')]
				if bytes.Contains(open, []byte(" t=")) && !bytes.Contains(open, []byte(` t="n"`)) {
					continue
				}
				hid := ""
				if hiddenSheets[f.Name] {
					hid = "（隠しシート）"
				}
				res.numSus = append(res.numSus, prefix+"「"+sheetNames[f.Name]+"」シート"+hid+"の "+string(mm[1])+" セル："+string(mm[2]))
			}
		}
		partName := f.Name
		orig := data
		if sheet, isSheet := sheetNames[partName]; isSheet && len(regTargets) > 0 {
			nd, reps := o.m.maskRegisteredCells(data, sheet, regTargets, sstTexts)
			if len(reps) > 0 {
				res.reports = append(res.reports, groupReport{matches: reps})
				data = nd
				res.regChanged = true
			}
		}
		if partName == "xl/sharedStrings.xml" && len(orphans) > 0 {
			data = blankSST(data, orphans)
		}
		if o != nil && o.restoreCB != nil {
			if _, isSheet := sheetNames[partName]; isSheet {
				if restoreSST == nil {
					restoreSST = readSST(zr)
				}
				if nd, n := restoreNumericCells(string(data), o.restoreCB, restoreSST); n > 0 {
					data = []byte(nd)
					res.rawCount += n
				}
			}
			if partName == "xl/workbook.xml" {
				data = []byte(forceRecalc(string(data)))
			}
		}
		rule := ruleFor(f.Name)
		out := data
		if rule != nil {
			var reps []groupReport
			var ok bool
			wordHdr := reWordHdrPart.MatchString(partName)
			out, reps, ok = processXML(data, *rule, func(t string, g int, name string) []match {
				sus := true
				if partName == "xl/sharedStrings.xml" && masterOnlySST[g] {
					return nil // マスタだけが使う文字（地名など）は調べない
				}
				if partName == "xl/sharedStrings.xml" && visibleSST != nil {
					sus = visibleSST[g]
				} else if hiddenSheets[partName] {
					sus = false
				}
				if hdrElems[name] {
					// Excel のヘッダー・フッター：書式記号に触れずに調べる（改善5）
					return hdrTransform(t, sus, tf)
				}
				return tf(t, sus, wordHdr)
			})
			if !ok {
				return nil, fmt.Errorf("中身の読み取りに失敗しました（%s）", f.Name)
			}
			res.reports = append(res.reports, reps...)
		}
		if raw != nil {
			ns, n := raw(partName, string(out))
			if n > 0 {
				out = []byte(ns)
				res.rawCount += n
				if reSheetPart.MatchString(partName) || partName == "xl/workbook.xml" || strings.Contains(partName, "/charts/") {
					out = []byte(quoteSheetRefs(string(out)))
				}
			}
		}
		if partName == "_rels/.rels" {
			out = reThumbRel.ReplaceAll(out, nil)
		}
		if partName == "xl/workbook.xml" && reBadDefinedName.Match(out) {
			return nil, fmt.Errorf("Excelの「名前の定義」に氏名が使われています。名前の定義を変えてからやり直してください")
		}
		if check != nil {
			chk := out
			if partName == "xl/sharedStrings.xml" && len(masterOnlySST) > 0 {
				// マスタ（郵便番号・医療機関など）だけが使う文字は、残りの確認からも外す（改善9の続き）
				n := -1
				chk = reSI.ReplaceAllFunc(out, func(si []byte) []byte {
					n++
					if masterOnlySST[n] {
						return []byte("<si/>")
					}
					return si
				})
			}
			txt := decodeEntities(string(chk))
			hits := check(prefix+partName, txt)
			res.leftovers = append(res.leftovers, hits...)
			if len(hits) > 0 && o != nil && o.m != nil {
				// 何シートの何セルか・何段落目か（改善12 No.33。xml のファイル名は画面に出さない）
				for _, h := range hits {
					res.leftHits = append(res.leftHits, o.m.locateLeft(zr, allSheetNames, prefix, partName, txt, leftWord(h, prefix+partName)))
				}
			}
		}
		if bytes.Equal(out, orig) {
			if err := zw.Copy(f); err != nil {
				return nil, err
			}
			continue
		}
		res.changed = true
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: f.Method, Modified: f.Modified})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(out); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func isTargetExt(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".docx", ".xlsx", ".xlsm":
		return true
	}
	return false
}

func isOldExt(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".doc", ".xls":
		return true
	}
	return false
}

var (
	reWbSheet = regexp.MustCompile(`<sheet\s[^>]*>`)
	reAttr    = func(name string) *regexp.Regexp { return regexp.MustCompile(`\s` + name + `="([^"]*)"`) }
	reAState  = reAttr("state")
	reARid    = reAttr("r:id")
	reAName   = reAttr("name")
	reRel     = regexp.MustCompile(`<Relationship\s[^>]*>`)
	reAId     = reAttr("Id")
	reATarget = reAttr("Target")
	reSSTCell = regexp.MustCompile(`(?s)<(?:\w+:)?c\s[^>]*?\bt="s"[^>]*>\s*(?:<(?:\w+:)?f[^>]*/>|<(?:\w+:)?f[^>]*>.*?</(?:\w+:)?f>)?\s*<(?:\w+:)?v>\s*([0-9]+)\s*</(?:\w+:)?v>`) // 改行・空白・x: 付きも拾う（点検役 R8.10.1）
)

func zipRead(zr *zip.Reader, name string) []byte {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil
			}
			b, _ := io.ReadAll(rc)
			rc.Close()
			return b
		}
	}
	return nil
}

// sheetVisibility returns hidden sheet part names and the set of shared-string
// indices used on visible sheets (nil when not a workbook).
func sheetVisibility(zr *zip.Reader) (map[string]bool, map[int]bool, map[string]string) {
	hidden := map[string]bool{}
	names := map[string]string{}
	wb := zipRead(zr, "xl/workbook.xml")
	if wb == nil {
		return hidden, nil, names
	}
	rels := zipRead(zr, "xl/_rels/workbook.xml.rels")
	idToTarget := map[string]string{}
	for _, r := range reRel.FindAll(rels, -1) {
		id, tg := reAId.FindSubmatch(r), reATarget.FindSubmatch(r)
		if id != nil && tg != nil {
			t := string(tg[1])
			if strings.HasPrefix(t, "/") {
				t = strings.TrimPrefix(t, "/")
			} else {
				t = path.Clean("xl/" + t)
			}
			idToTarget[string(id[1])] = t
		}
	}
	var visible []string
	for _, sh := range reWbSheet.FindAll(wb, -1) {
		rid := reARid.FindSubmatch(sh)
		if rid == nil {
			continue
		}
		target := idToTarget[string(rid[1])]
		if nm := reAName.FindSubmatch(sh); nm != nil {
			names[target] = decodeEntities(string(nm[1]))
		}
		st := reAState.FindSubmatch(sh)
		if st != nil && (string(st[1]) == "hidden" || string(st[1]) == "veryHidden") {
			hidden[target] = true
		} else {
			visible = append(visible, target)
		}
	}
	if len(hidden) == 0 {
		return hidden, nil, names // 全部見えている：すべて確認対象
	}
	used := map[int]bool{}
	for _, v := range visible {
		for _, m := range reSSTCell.FindAllSubmatch(zipRead(zr, v), -1) {
			n := 0
			for _, c := range m[1] {
				n = n*10 + int(c-'0')
			}
			used[n] = true
		}
	}
	return hidden, used, names
}

var (
	reCellFull = regexp.MustCompile(`(?s)<c r="([A-Z]+)([0-9]+)"([^>]*?)(?:/>|>(.*?)</c>)`)
	reCellV    = regexp.MustCompile(`<v>([^<]*)</v>`)
	reCellIs   = regexp.MustCompile(`(?s)<is>(.*?)</is>`)
	reTagStrip = regexp.MustCompile(`<[^>]+>`)
	reHasDigit = regexp.MustCompile(`[0-9０-９]`)
)

type cellInfo struct {
	typ, v, text string
}

func colNum(s string) int {
	n := 0
	for _, c := range s {
		n = n*26 + int(c-'A'+1)
	}
	return n
}

func colName(n int) string {
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}

// birthCells finds values placed next to (right of / below) a 「生年月日」 label cell.
func birthCells(zr *zip.Reader, sheetNames map[string]string, prefix string) []string {
	sst := readSST(zr)
	var out []string
	for part, sheet := range sheetNames {
		data := zipRead(zr, part)
		if data == nil || !bytes.Contains(data, []byte("<c ")) {
			continue
		}
		grid := map[[2]int]cellInfo{}
		var labels [][2]int
		for _, m := range reCellFull.FindAllSubmatch(data, -1) {
			col, row := colNum(string(m[1])), 0
			fmt.Sscanf(string(m[2]), "%d", &row)
			typ := ""
			if t := reTypeAttr.FindSubmatch(m[3]); t != nil {
				typ = string(t[1])
			}
			ci := cellInfo{typ: typ}
			if v := reCellV.FindSubmatch(m[4]); v != nil {
				ci.v = string(v[1])
			}
			switch typ {
			case "s":
				var i int
				if _, err := fmt.Sscanf(ci.v, "%d", &i); err == nil && i < len(sst) {
					ci.text = sst[i]
				}
			case "inlineStr":
				if is := reCellIs.FindSubmatch(m[4]); is != nil {
					ci.text = decodeEntities(reTagStrip.ReplaceAllString(string(is[1]), ""))
				}
			case "str":
				ci.text = decodeEntities(ci.v)
			}
			if ci.v == "" && ci.text == "" {
				continue
			}
			grid[[2]int{row, col}] = ci
			if strings.Contains(strings.NewReplacer(" ", "", "　", "", "\n", "").Replace(ci.text), "生年月日") && utf8.RuneCountInString(ci.text) <= 20 && !strings.Contains(ci.text, "【") { // 【生年月日002】（置き換えた記号）は見出しではない（改善27 試験役 Q1）
				labels = append(labels, [2]int{row, col})
			}
		}
		for _, l := range labels {
			var cands [][2]int
			// 右へ：次の見出し（数字を含まない文字）が来るまで
			for c := l[1] + 1; c <= l[1]+12; c++ {
				ci, ok := grid[[2]int{l[0], c}]
				if !ok {
					continue
				}
				if ci.text != "" && !reHasDigit.MatchString(ci.text) && !isEraOrUnit(ci.text) {
					break
				}
				cands = append(cands, [2]int{l[0], c})
			}
			// 下へ：すぐ下の行（右へ数列まで）
			for c := l[1]; c <= l[1]+12; c++ {
				if ci, ok := grid[[2]int{l[0] + 1, c}]; ok {
					if ci.text != "" && !reHasDigit.MatchString(ci.text) && !isEraOrUnit(ci.text) {
						break
					}
					cands = append(cands, [2]int{l[0] + 1, c})
				}
			}
			for _, pos := range cands {
				ci := grid[pos]
				isNum := ci.typ == "" || ci.typ == "n"
				if ci.text != "" && strings.Contains(ci.text, "【") {
					continue
				}
				if isNum || (ci.text != "" && reHasDigit.MatchString(ci.text)) {
					out = append(out, prefix+"「"+sheet+"」シートの "+colName(pos[1])+fmt.Sprint(pos[0])+" セル（「生年月日」の隣）")
				}
			}
		}
	}
	return out
}

var eraWords = map[string]bool{"明治": true, "大正": true, "昭和": true, "平成": true, "令和": true, "年": true, "月": true, "日": true, "生": true, "歳": true, "才": true, "西暦": true, "～": true, "〜": true}

func isEraOrUnit(s string) bool { return eraWords[strings.TrimSpace(s)] }

// 様式に入っている隠しのマスタシート（名前で決める）
var masterSheetNames = map[string]bool{"郵便番号マスタ": true, "市区町村マスタ": true, "医療機関マスタ": true, "プルダウンリスト": true}

var masterUsedCache = map[*zip.Reader]map[int]bool{}

// masterUsedSST: マスタのシートが使う共有文字の番号
func masterUsedSST(zr *zip.Reader, masterParts map[string]bool) map[int]bool {
	if u, ok := masterUsedCache[zr]; ok {
		return u
	}
	if len(masterUsedCache) > 8 { // 一日中開いていても増え続けないように（点検役 R8.10.1）
		masterUsedCache = map[*zip.Reader]map[int]bool{}
	}
	u := map[int]bool{}
	for p := range masterParts {
		for _, m := range reSSTCell.FindAllSubmatch(zipRead(zr, p), -1) {
			n := 0
			for _, c := range m[1] {
				n = n*10 + int(c-'0')
			}
			u[n] = true
		}
	}
	masterUsedCache[zr] = u
	return u
}

// masterSheets: 隠しになっているマスタのシートと、マスタだけが使う共有文字の番号
func masterSheets(zr *zip.Reader, hidden map[string]bool, names map[string]string) (map[string]bool, map[int]bool) {
	parts := map[string]bool{}
	for p, n := range names {
		if hidden[p] && masterSheetNames[n] {
			parts[p] = true
		}
	}
	only := map[int]bool{}
	if len(parts) == 0 {
		return parts, only
	}
	used := masterUsedSST(zr, parts)
	other := map[int]bool{}
	for p := range names {
		if parts[p] {
			continue
		}
		for _, m := range reSSTCell.FindAllSubmatch(zipRead(zr, p), -1) {
			n := 0
			for _, c := range m[1] {
				n = n*10 + int(c-'0')
			}
			other[n] = true
		}
	}
	for i := range used {
		if !other[i] {
			only[i] = true
		}
	}
	return parts, only
}
