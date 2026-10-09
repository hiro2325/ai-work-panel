package main

// 支援経過（改善4）で使う Word・Excel の部品。どれもXMLの文字列のまま扱い、
// 触らない部分（書式・印刷設定・入力規則・条件付き書式・式・ほかのシート）はバイトのまま残す。

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ---------------- zip（パッケージ）の読み書き ----------------

type pkgEntry struct {
	f    *zip.File // 元のまま写すもの（nil なら data を書く）
	name string
	data []byte
}

type ooPkg struct {
	entries []*pkgEntry
}

func readPkg(data []byte) (*ooPkg, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	p := &ooPkg{}
	for _, f := range zr.File {
		p.entries = append(p.entries, &pkgEntry{f: f, name: f.Name})
	}
	return p, nil
}

func (p *ooPkg) get(name string) (string, bool) {
	for _, e := range p.entries {
		if e.name != name {
			continue
		}
		if e.f == nil {
			return string(e.data), true
		}
		rc, err := e.f.Open()
		if err != nil {
			return "", false
		}
		b, err := io.ReadAll(io.LimitReader(rc, 64<<20))
		rc.Close()
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return "", false
}

func (p *ooPkg) has(name string) bool {
	for _, e := range p.entries {
		if e.name == name {
			return true
		}
	}
	return false
}

// set: 中身を差し替える（なければ最後に足す）
func (p *ooPkg) set(name string, data string) {
	for _, e := range p.entries {
		if e.name == name {
			e.f, e.data = nil, []byte(data)
			return
		}
	}
	p.entries = append(p.entries, &pkgEntry{name: name, data: []byte(data)})
}

func (p *ooPkg) bytes() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range p.entries {
		if e.f != nil {
			// 変えないものは、圧縮したデータをそのまま写す
			rc, err := e.f.OpenRaw()
			if err != nil {
				return nil, err
			}
			h := e.f.FileHeader
			w, err := zw.CreateRaw(&h)
			if err != nil {
				return nil, err
			}
			if _, err := io.Copy(w, rc); err != nil {
				return nil, err
			}
			continue
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Deflate})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// checkPkgXML: パッケージの中の .xml・.rels をすべて読み通せるか（壊れた部品がないか）を確かめる
func checkPkgXML(data []byte) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("ファイルの形が正しくありません: %v", err)
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".xml") && !strings.HasSuffix(f.Name, ".rels") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("%s を読めません: %v", f.Name, err)
		}
		d := xml.NewDecoder(rc)
		for {
			_, err := d.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				rc.Close()
				return fmt.Errorf("%s のXMLが正しくありません: %v", f.Name, err)
			}
		}
		rc.Close()
	}
	return nil
}

func xmlUnesc(s string) string { return html.UnescapeString(s) }

func newGUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	h := strings.ToUpper(hex.EncodeToString(b))
	return "{" + h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32] + "}"
}

var reAttrVal = func(name string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `="([^"]*)"`)
}

func attr(tag, name string) string {
	if m := reAttrVal(name).FindStringSubmatch(tag); m != nil {
		return xmlUnesc(m[1])
	}
	return ""
}

// ---------------- Word：本文の段落 ----------------

type wPara struct {
	start, end int    // document.xml の中の位置
	xml        string // <w:p …>…</w:p>
	text       string // 文字（w:tab は \t、w:br は \n）
	style      string // w:pStyle
}

var reWTag = regexp.MustCompile(`<(/?)(w:[A-Za-z]+)((?:\s[^>]*?)?)(/?)>`)

// bodyParas: <w:body> の直下の段落（表の中は含めない）
func bodyParas(doc string) (bodyIn, bodyEnd int, paras []wPara, err error) {
	bs := strings.Index(doc, "<w:body>")
	if bs < 0 {
		return 0, 0, nil, fmt.Errorf("Wordの本文が見つかりません")
	}
	bodyIn = bs + len("<w:body>")
	bodyEnd = strings.LastIndex(doc, "</w:body>")
	if bodyEnd < bodyIn {
		return 0, 0, nil, fmt.Errorf("Wordの本文が見つかりません")
	}
	depthTbl, depthP := 0, 0
	pStart := -1
	for _, ix := range reWTag.FindAllStringSubmatchIndex(doc[bodyIn:bodyEnd], -1) {
		a, b := ix[0]+bodyIn, ix[1]+bodyIn
		closing := ix[3] > ix[2]
		name := doc[ix[4]+bodyIn : ix[5]+bodyIn]
		self := ix[9] > ix[8]
		switch name {
		case "w:tbl", "w:txbxContent", "w:sdtContent":
			if name != "w:tbl" {
				continue
			}
			if self {
				continue
			}
			if closing {
				depthTbl--
			} else {
				depthTbl++
			}
		case "w:p":
			if depthTbl > 0 {
				continue
			}
			if self {
				if depthP == 0 {
					paras = append(paras, wPara{start: a, end: b, xml: doc[a:b]})
				}
				continue
			}
			if closing {
				depthP--
				if depthP == 0 && pStart >= 0 {
					px := doc[pStart:b]
					paras = append(paras, wPara{start: pStart, end: b, xml: px})
					pStart = -1
				}
			} else {
				if depthP == 0 {
					pStart = a
				}
				depthP++
			}
		}
	}
	for i := range paras {
		paras[i].text = paraText(paras[i].xml)
		if m := rePStyle.FindStringSubmatch(paras[i].xml); m != nil {
			paras[i].style = m[1]
		}
	}
	return bodyIn, bodyEnd, paras, nil
}

var rePStyle = regexp.MustCompile(`<w:pStyle w:val="([^"]*)"`)

// paraText: 段落の文字。w:t の中身、w:tab→\t、w:br・w:cr→\n（削除した文字・フィールドの命令は除く）
func paraText(px string) string {
	var b strings.Builder
	inT := false
	tStart := 0
	for _, ix := range reWTag.FindAllStringSubmatchIndex(px, -1) {
		closing := ix[3] > ix[2]
		name := px[ix[4]:ix[5]]
		self := ix[9] > ix[8]
		switch name {
		case "w:t":
			if self {
				continue
			}
			if closing {
				if inT {
					b.WriteString(xmlUnesc(px[tStart:ix[0]]))
				}
				inT = false
			} else {
				inT, tStart = true, ix[1]
			}
		case "w:tab":
			if self && !inT {
				b.WriteString("\t")
			}
		case "w:br", "w:cr":
			if self {
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// tSeg: 段落の中の w:t（中身の位置）・w:tab・w:br の並び
type tSeg struct {
	kind       byte // 't' / 'x'（tab・br）
	tagA, tagB int  // w:t の開始タグの位置
	cs, ce     int  // 中身の位置
	text       string
}

func paraSegs(px string) []tSeg {
	var out []tSeg
	cur := -1
	for _, ix := range reWTag.FindAllStringSubmatchIndex(px, -1) {
		closing := ix[3] > ix[2]
		name := px[ix[4]:ix[5]]
		self := ix[9] > ix[8]
		switch name {
		case "w:t":
			if self {
				continue
			}
			if closing {
				if cur >= 0 {
					out[cur].ce = ix[0]
					out[cur].text = xmlUnesc(px[out[cur].cs:ix[0]])
				}
				cur = -1
			} else {
				out = append(out, tSeg{kind: 't', tagA: ix[0], tagB: ix[1], cs: ix[1]})
				cur = len(out) - 1
			}
		case "w:tab", "w:br", "w:cr":
			if self && cur < 0 {
				out = append(out, tSeg{kind: 'x'})
			}
		}
	}
	return out
}

// replaceParaPrefix: 段落の文字の先頭 n 字を newHead に置き換える（書式の run はそのまま）。
// 先頭 n 字の途中に w:tab・w:br があれば置き換えない（false）
func replaceParaPrefix(px string, n int, newHead string) (string, bool) {
	segs := paraSegs(px)
	need := n
	type edit struct {
		seg  tSeg
		text string
	}
	var edits []edit
	first := -1
	for _, sg := range segs {
		if need <= 0 {
			break
		}
		if sg.kind != 't' {
			return px, false
		}
		rs := []rune(sg.text)
		rest := ""
		if need >= len(rs) {
			need -= len(rs)
		} else {
			rest = string(rs[need:])
			need = 0
		}
		if first < 0 {
			first = len(edits)
		}
		edits = append(edits, edit{sg, rest})
	}
	if need > 0 || first < 0 {
		return px, false
	}
	edits[first].text = newHead + edits[first].text
	out := px
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		tag := out[e.seg.tagA:e.seg.tagB]
		if !strings.Contains(tag, "xml:space") && e.text != strings.TrimSpace(e.text) {
			tag = strings.Replace(tag, "<w:t", `<w:t xml:space="preserve"`, 1)
		}
		out = out[:e.seg.tagA] + tag + encodeText(e.text) + out[e.seg.ce:]
	}
	return out, true
}

// setParaStyle: 段落に段落スタイルが付いていなければ付ける
func setParaStyle(px, styleID string) string {
	if rePStyle.MatchString(px) {
		return px
	}
	st := `<w:pStyle w:val="` + styleID + `"/>`
	if i := strings.Index(px, "<w:pPr/>"); i >= 0 {
		return px[:i] + "<w:pPr>" + st + "</w:pPr>" + px[i+len("<w:pPr/>"):]
	}
	if i := strings.Index(px, "<w:pPr>"); i >= 0 {
		return px[:i+len("<w:pPr>")] + st + px[i+len("<w:pPr>"):]
	}
	if m := regexp.MustCompile(`^<w:pPr\s[^>]*>`).FindStringIndex(px[strings.Index(px, ">")+1:]); m != nil {
		k := strings.Index(px, ">") + 1 + m[1]
		return px[:k] + st + px[k:]
	}
	if strings.HasSuffix(px, "/>") && strings.HasPrefix(px, "<w:p") && !strings.Contains(px, "</w:p>") {
		return strings.TrimSuffix(px, "/>") + "><w:pPr>" + st + "</w:pPr></w:p>"
	}
	k := strings.Index(px, ">") + 1
	return px[:k] + "<w:pPr>" + st + "</w:pPr>" + px[k:]
}

const keikaStyleID = "KeikaEntry"

// ensureKeikaStyle: styles.xml に段落スタイル「経過1件」がなければ足す。使うスタイルIDを返す
func ensureKeikaStyle(styles string) (string, string) {
	if strings.Contains(styles, `w:styleId="`+keikaStyleID+`"`) {
		return styles, keikaStyleID
	}
	if m := regexp.MustCompile(`<w:style [^>]*w:styleId="([^"]*)"[^>]*>\s*<w:name w:val="経過1件"/>`).FindStringSubmatch(styles); m != nil {
		return styles, m[1]
	}
	base := ""
	for _, m := range regexp.MustCompile(`<w:style [^>]*>`).FindAllString(styles, -1) {
		if attr(m, "w:type") == "paragraph" && (attr(m, "w:default") == "1" || attr(m, "w:default") == "true") {
			base = attr(m, "w:styleId")
		}
	}
	st := `<w:style w:type="paragraph" w:customStyle="1" w:styleId="` + keikaStyleID + `"><w:name w:val="経過1件"/>`
	if base != "" {
		st += `<w:basedOn w:val="` + base + `"/>`
	}
	st += `<w:qFormat/></w:style>`
	i := strings.LastIndex(styles, "</w:styles>")
	if i < 0 {
		return styles, keikaStyleID
	}
	return styles[:i] + st + styles[i:], keikaStyleID
}

// entryParaXML: 一覧の1件（頭の行＋本文。本文の改行は段落内の改行、タブは w:tab）
func entryParaXML(styleID, head string, body []string) string {
	var b strings.Builder
	b.WriteString(`<w:p><w:pPr><w:pStyle w:val="` + styleID + `"/></w:pPr><w:r>`)
	writeRunText(&b, head)
	b.WriteString(`</w:r>`)
	if len(body) > 0 {
		b.WriteString(`<w:r>`)
		for _, l := range body {
			b.WriteString(`<w:br/>`)
			writeRunText(&b, l)
		}
		b.WriteString(`</w:r>`)
	}
	b.WriteString(`</w:p>`)
	return b.String()
}

func writeRunText(b *strings.Builder, t string) {
	for i, part := range strings.Split(xmlSafe(t), "\t") {
		if i > 0 {
			b.WriteString(`<w:tab/>`)
		}
		if part != "" {
			b.WriteString(`<w:t xml:space="preserve">` + encodeText(part) + `</w:t>`)
		}
	}
}

// plainDocx: 題名（太字）と段落の並びだけの Word（段落の中の \n は段落内の改行）
func plainDocx(title string, paras []string) ([]byte, error) {
	var body strings.Builder
	rpr := `<w:rPr><w:rFonts w:ascii="游明朝" w:eastAsia="游明朝" w:hAnsi="游明朝"/><w:sz w:val="21"/><w:szCs w:val="21"/></w:rPr>`
	body.WriteString(`<w:p><w:pPr><w:spacing w:after="120"/></w:pPr><w:r>` + strings.Replace(rpr, `<w:sz `, `<w:b/><w:sz `, 1) + `<w:t xml:space="preserve">` + encodeText(xmlSafe(title)) + `</w:t></w:r></w:p>`)
	for _, p := range paras {
		body.WriteString(`<w:p><w:pPr><w:spacing w:after="120"/></w:pPr>`)
		for li, line := range strings.Split(p, "\n") {
			body.WriteString(`<w:r>` + rpr)
			if li > 0 {
				body.WriteString(`<w:br/>`)
			}
			for i, part := range strings.Split(xmlSafe(line), "\t") {
				if i > 0 {
					body.WriteString(`<w:tab/>`)
				}
				if part != "" {
					body.WriteString(`<w:t xml:space="preserve">` + encodeText(part) + `</w:t>`)
				}
			}
			body.WriteString(`</w:r>`)
		}
		body.WriteString(`</w:p>`)
	}
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body.String() +
		`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134" w:header="567" w:footer="567" w:gutter="0"/></w:sectPr></w:body></w:document>`
	p := &ooPkg{}
	p.set("[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`)
	p.set("_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`)
	p.set("word/document.xml", doc)
	return p.bytes()
}

// docxLines: Word の本文の段落を1行ずつ（段落内の改行は分ける）
func docxParaTexts(data []byte) ([]string, error) {
	p, err := readPkg(data)
	if err != nil {
		return nil, fmt.Errorf("Wordとして開けません")
	}
	doc, ok := p.get("word/document.xml")
	if !ok {
		return nil, fmt.Errorf("Wordの本文が見つかりません")
	}
	_, _, paras, err := bodyParas(doc)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, pa := range paras {
		out = append(out, pa.text)
	}
	return out, nil
}

// ---------------- Excel：シートの複製 ----------------

type wbSheet struct {
	name, sheetID, rid, state string
	target                    string // xl/worksheets/sheet1.xml
}

var (
	reSheetTag = regexp.MustCompile(`<sheet\s[^>]*?/>`)
	reRelTag   = regexp.MustCompile(`<Relationship\s[^>]*?/>`)
	reDefName  = regexp.MustCompile(`<definedName\s[^>]*>[^<]*</definedName>`)
)

func relsMap(rels string) map[string][2]string {
	out := map[string][2]string{}
	for _, t := range reRelTag.FindAllString(rels, -1) {
		out[attr(t, "Id")] = [2]string{attr(t, "Type"), attr(t, "Target")}
	}
	return out
}

func resolveTarget(baseDir, target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(target, "/")
	}
	parts := strings.Split(baseDir, "/")
	if baseDir == "" {
		parts = nil
	}
	for _, seg := range strings.Split(target, "/") {
		switch seg {
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		case ".", "":
		default:
			parts = append(parts, seg)
		}
	}
	return strings.Join(parts, "/")
}

func wbSheets(p *ooPkg) ([]wbSheet, error) {
	wb, ok := p.get("xl/workbook.xml")
	if !ok {
		return nil, fmt.Errorf("Excelの中身（workbook.xml）が見つかりません")
	}
	rels, _ := p.get("xl/_rels/workbook.xml.rels")
	rm := relsMap(rels)
	var out []wbSheet
	for _, t := range reSheetTag.FindAllString(wb, -1) {
		s := wbSheet{name: attr(t, "name"), sheetID: attr(t, "sheetId"), rid: attr(t, "r:id"), state: attr(t, "state")}
		s.target = resolveTarget("xl", rm[s.rid][1])
		out = append(out, s)
	}
	return out, nil
}

// sharedStrings の文字（ふりがなは除く）
func sharedStrings(p *ooPkg) []string {
	x, ok := p.get("xl/sharedStrings.xml")
	if !ok {
		return nil
	}
	var out []string
	for _, si := range regexp.MustCompile(`(?s)<si>(.*?)</si>|<si/>`).FindAllStringSubmatch(x, -1) {
		body := regexp.MustCompile(`(?s)<rPh\b.*?</rPh>`).ReplaceAllString(si[1], "")
		var b strings.Builder
		for _, t := range regexp.MustCompile(`(?s)<t(?:\s[^>]*)?>(.*?)</t>`).FindAllStringSubmatch(body, -1) {
			b.WriteString(xmlUnesc(t[1]))
		}
		out = append(out, b.String())
	}
	return out
}

// cellText: シートXMLの中の1つのセルの文字（共有文字列・インライン文字列・数値）
func cellText(sheet, ref string, ss []string) string {
	re := regexp.MustCompile(`(?s)<c r="` + ref + `"((?:\s[^>]*?)?)(?:/>|>(.*?)</c>)`)
	m := re.FindStringSubmatch(sheet)
	if m == nil {
		return ""
	}
	typ := attr("<c"+m[1]+">", "t")
	inner := m[2]
	switch typ {
	case "s":
		if v := regexp.MustCompile(`<v>(\d+)</v>`).FindStringSubmatch(inner); v != nil {
			if i, _ := strconv.Atoi(v[1]); i < len(ss) {
				return ss[i]
			}
		}
		return ""
	case "inlineStr":
		var b strings.Builder
		for _, t := range regexp.MustCompile(`(?s)<t(?:\s[^>]*)?>(.*?)</t>`).FindAllStringSubmatch(inner, -1) {
			b.WriteString(xmlUnesc(t[1]))
		}
		return b.String()
	}
	if v := regexp.MustCompile(`(?s)<v>(.*?)</v>`).FindStringSubmatch(inner); v != nil {
		return xmlUnesc(v[1])
	}
	return ""
}

var reKCellRef = regexp.MustCompile(`^([A-Z]+)(\d+)$`)

// setCellXML: セル ref の中身を差し替える（書式 s はそのまま）。セルがなければ行の中の正しい位置に足す
func setCellXML(sheet, ref, typ, inner string) (string, error) {
	re := regexp.MustCompile(`(?s)<c r="` + ref + `"((?:\s[^>]*?)?)(?:/>|>(.*?)</c>)`)
	build := func(attrs string) string {
		attrs = regexp.MustCompile(`\s+t="[^"]*"`).ReplaceAllString(attrs, "")
		attrs = strings.TrimSuffix(attrs, "/")
		t := ""
		if typ != "" {
			t = ` t="` + typ + `"`
		}
		return `<c r="` + ref + `"` + attrs + t + `>` + inner + `</c>`
	}
	if loc := re.FindStringSubmatchIndex(sheet); loc != nil {
		attrs := sheet[loc[2]:loc[3]]
		return sheet[:loc[0]] + build(attrs) + sheet[loc[1]:], nil
	}
	m := reKCellRef.FindStringSubmatch(ref)
	if m == nil {
		return sheet, fmt.Errorf("セルの位置が正しくありません")
	}
	rowRe := regexp.MustCompile(`(?s)<row r="` + m[2] + `"[^>]*?(?:/>|>(.*?)</row>)`)
	loc := rowRe.FindStringSubmatchIndex(sheet)
	if loc == nil {
		return sheet, fmt.Errorf("行 %s が見つかりません", m[2])
	}
	if loc[2] < 0 { // <row …/>
		tag := sheet[loc[0]:loc[1]]
		nt := strings.TrimSuffix(tag, "/>") + ">" + build("") + "</row>"
		return sheet[:loc[0]] + nt + sheet[loc[1]:], nil
	}
	want := colNum(m[1])
	rowInner := sheet[loc[2]:loc[3]]
	pos := len(rowInner)
	for _, c := range regexp.MustCompile(`<c r="([A-Z]+)\d+"`).FindAllStringSubmatchIndex(rowInner, -1) {
		if colNum(rowInner[c[2]:c[3]]) > want {
			pos = c[0]
			break
		}
	}
	return sheet[:loc[2]+pos] + build("") + sheet[loc[2]+pos:], nil
}

func inlineStrXML(t string) string {
	return `<is><t xml:space="preserve">` + encodeText(xmlSafe(t)) + `</t></is>`
}

// setFormulaCache: 式のセルの計算結果（開く前にも見えるように。Excel は開いたときに計算し直す）
func setFormulaCache(sheet, ref, val string) string {
	re := regexp.MustCompile(`(?s)(<c r="` + ref + `"[^>]*>)(<f>.*?</f>)(?:<v>.*?</v>|<v/>)?(</c>)`)
	return re.ReplaceAllString(sheet, "${1}${2}<v>"+strings.ReplaceAll(encodeText(val), "$", "$$")+"</v>${3}")
}

type newSheet struct {
	name  string
	cells map[string][2]string // ref → {型, 中身のXML}
	cache map[string]string    // 式のセル → 計算結果
}

// addSheetsFromTemplate: 原紙のシートを XML のまま複製して、新しいシートを最後に足す。
// 印刷範囲などの名前（localSheetId が原紙のもの）も複製する。
func addSheetsFromTemplate(p *ooPkg, tmplName string, adds []newSheet) error {
	return addSheetsFromTemplateAfter(p, tmplName, "", adds)
}

// addSheetsFromTemplateAfter: afterName のシートのすぐ右に入れる（afterName が空なら最後）。
// 入れた位置より右にあるシートの印刷範囲などの名前（localSheetId）・開いているシートの番号（activeTab）もずらす。
// 支援経過のサーバー保存（改善18）：マクロ入り .xlsm に使うので、シートの codeName は外す（同じ名前が2つになるのを避ける）
func addSheetsFromTemplateAfter(p *ooPkg, tmplName, afterName string, adds []newSheet) error {
	return addSheetsFromTemplateAt(p, tmplName, afterName, false, adds)
}

// addSheetsFromTemplateAt: addSheetsFromTemplateAfter に「一番左に入れる」(front) を足したもの（改善28 No.50）。
// front=true のときは afterName を使わず、いちばん左（番号0）に入れる。ほかの動きは addSheetsFromTemplateAfter と同じ
func addSheetsFromTemplateAt(p *ooPkg, tmplName, afterName string, front bool, adds []newSheet) error {
	sheets, err := wbSheets(p)
	if err != nil {
		return err
	}
	srcIdx := -1
	for i, s := range sheets {
		if s.name == tmplName {
			srcIdx = i
		}
	}
	if srcIdx < 0 {
		return fmt.Errorf("記録のファイルに「%s」のシートが見つかりません", tmplName)
	}
	src := sheets[srcIdx]
	srcXML, ok := p.get(src.target)
	if !ok {
		return fmt.Errorf("「%s」のシートの中身が見つかりません", tmplName)
	}
	srcDir := src.target[:strings.LastIndex(src.target, "/")]
	srcBase := src.target[strings.LastIndex(src.target, "/")+1:]
	srcRelsName := srcDir + "/_rels/" + srcBase + ".rels"
	srcRels, hasRels := p.get(srcRelsName)

	wb, _ := p.get("xl/workbook.xml")
	wbRels, ok := p.get("xl/_rels/workbook.xml.rels")
	if !ok {
		return fmt.Errorf("Excelの中身（workbook.xml.rels）が見つかりません")
	}
	ct, ok := p.get("[Content_Types].xml")
	if !ok {
		return fmt.Errorf("Excelの中身（[Content_Types].xml）が見つかりません")
	}
	app, hasApp := p.get("docProps/app.xml")

	names := map[string]bool{}
	maxSheetID := 0
	for _, s := range sheets {
		names[strings.ToLower(s.name)] = true
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
	nextFile := func(dir, prefix, ext string) string {
		for i := 1; ; i++ {
			n := fmt.Sprintf("%s/%s%d%s", dir, prefix, i, ext)
			if !p.has(n) {
				return n
			}
		}
	}
	quoted := func(n string) string { return "'" + strings.ReplaceAll(n, "'", "''") + "'" }
	var defNames []string
	for _, d := range reDefName.FindAllString(wb, -1) {
		if attr(d, "localSheetId") == strconv.Itoa(srcIdx) {
			defNames = append(defNames, d)
		}
	}
	idx := len(sheets)
	lastTag := ""
	if front {
		idx = 0
		afterName = ""
	}
	if afterName != "" {
		ai := -1
		for i, sh := range sheets {
			if sh.name == afterName {
				ai = i
			}
		}
		if ai < 0 {
			return fmt.Errorf("記録のファイルに「%s」のシートが見つかりません", afterName)
		}
		idx = ai + 1
		for _, t := range reSheetTag.FindAllString(wb, -1) {
			if attr(t, "name") == afterName {
				lastTag = t
			}
		}
		if lastTag == "" {
			return fmt.Errorf("記録のファイルに「%s」のシートが見つかりません", afterName)
		}
	}
	for _, ad := range adds {
		if names[strings.ToLower(ad.name)] {
			return fmt.Errorf("「%s」というシートがすでにあります", ad.name)
		}
		names[strings.ToLower(ad.name)] = true
		// シートのXML
		x := srcXML
		x = regexp.MustCompile(`xr:uid="\{[0-9A-Fa-f-]+\}"`).ReplaceAllStringFunc(x, func(string) string { return `xr:uid="` + newGUID() + `"` })
		x = strings.Replace(x, ` tabSelected="1"`, "", 1)
		x = regexp.MustCompile(`(<sheetPr\b[^>]*?)\s+codeName="[^"]*"`).ReplaceAllString(x, "$1")
		for ref, cv := range ad.cells {
			if x, err = setCellXML(x, ref, cv[0], cv[1]); err != nil {
				return err
			}
		}
		for ref, v := range ad.cache {
			x = setFormulaCache(x, ref, v)
		}
		target := nextFile(srcDir, "sheet", ".xml")
		p.set(target, x)
		// シートの rels（プリンターの設定などは写しを作る）
		if hasRels {
			nr := srcRels
			for _, t := range reRelTag.FindAllString(srcRels, -1) {
				tg := attr(t, "Target")
				if attr(t, "TargetMode") == "External" {
					continue
				}
				full := resolveTarget(srcDir, tg)
				if strings.Contains(full, "printerSettings") {
					data, ok := p.get(full)
					if !ok {
						continue
					}
					ext := full[strings.LastIndex(full, "."):]
					pdir := full[:strings.LastIndex(full, "/")]
					np := nextFile(pdir, "printerSettings", ext)
					p.set(np, data)
					// 拡張子 .bin の既定がプリンター設定でないブック（例：既定が vbaProject）では、型を個別に書く（改善18 点検）
					if !regexp.MustCompile(`<Default Extension="` + regexp.QuoteMeta(strings.TrimPrefix(ext, ".")) + `" ContentType="application/vnd\.openxmlformats-officedocument\.spreadsheetml\.printerSettings"`).MatchString(ct) {
						ct = strings.Replace(ct, "</Types>", `<Override PartName="/`+np+`" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.printerSettings"/></Types>`, 1)
					}
					rel :="../" + np[strings.Index(np, "/")+1:]
					if strings.HasPrefix(np, "xl/") {
						rel = "../" + strings.TrimPrefix(np, "xl/")
					}
					nt := strings.Replace(t, `Target="`+tg+`"`, `Target="`+rel+`"`, 1)
					nr = strings.Replace(nr, t, nt, 1)
				} else if strings.Contains(attr(t, "Type"), "/drawing") || strings.Contains(attr(t, "Type"), "/comments") || strings.Contains(attr(t, "Type"), "/vmlDrawing") || strings.Contains(attr(t, "Type"), "/table") {
					return fmt.Errorf("「%s」のシートに図・コメント・テーブルがあるため、複製できません（様式係に相談してください）", tmplName)
				}
			}
			tb := target[strings.LastIndex(target, "/")+1:]
			p.set(srcDir+"/_rels/"+tb+".rels", nr)
		}
		// workbook.xml.rels
		maxRid++
		rid := fmt.Sprintf("rId%d", maxRid)
		relTarget := strings.TrimPrefix(target, "xl/")
		wbRels = strings.Replace(wbRels, "</Relationships>", `<Relationship Id="`+rid+`" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="`+relTarget+`"/></Relationships>`, 1)
		// [Content_Types].xml
		ct = strings.Replace(ct, "</Types>", `<Override PartName="/`+target+`" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`, 1)
		// workbook.xml：シートと印刷範囲など
		maxSheetID++
		newTag := `<sheet name="` + xmlAttrEscape(ad.name) + `" sheetId="` + strconv.Itoa(maxSheetID) + `" r:id="` + rid + `"/>`
		if lastTag != "" {
			wb = strings.Replace(wb, lastTag, lastTag+newTag, 1)
			lastTag = newTag
			wb = shiftSheetIndex(wb, idx)
		} else if front {
			if !strings.Contains(wb, "<sheets>") {
				return fmt.Errorf("Excelの中身（workbook.xml）の形が思ったものと違います")
			}
			wb = strings.Replace(wb, "<sheets>", "<sheets>"+newTag, 1)
			lastTag = newTag // 2つ目からは、いま入れたシートのすぐ右
			wb = shiftSheetIndex(wb, idx)
		} else {
			wb = strings.Replace(wb, "</sheets>", newTag+"</sheets>", 1)
		}
		var nd []string
		hasPA := false
		for _, d := range defNames {
			d2 := strings.Replace(d, `localSheetId="`+strconv.Itoa(srcIdx)+`"`, `localSheetId="`+strconv.Itoa(idx)+`"`, 1)
			d2 = strings.ReplaceAll(d2, xmlAttrEscape(quoted(tmplName))+"!", xmlAttrEscape(quoted(ad.name))+"!")
			d2 = strings.ReplaceAll(d2, quoted(tmplName)+"!", quoted(ad.name)+"!")
			d2 = strings.ReplaceAll(d2, ">"+tmplName+"!", ">"+quoted(ad.name)+"!")
			if attr(d2, "name") == "_xlnm.Print_Area" {
				hasPA = true
			}
			nd = append(nd, d2)
		}
		if !hasPA {
			nd = append(nd, `<definedName name="_xlnm.Print_Area" localSheetId="`+strconv.Itoa(idx)+`">`+encodeText(quoted(ad.name))+`!$A$1:$T$39</definedName>`)
		}
		if strings.Contains(wb, "</definedNames>") {
			wb = strings.Replace(wb, "</definedNames>", strings.Join(nd, "")+"</definedNames>", 1)
		} else {
			wb = strings.Replace(wb, "</sheets>", "</sheets><definedNames>"+strings.Join(nd, "")+"</definedNames>", 1)
		}
		// docProps/app.xml：シートの数と名前
		if hasApp {
			at := -1
			if afterName != "" || front {
				at = idx
			}
			app = appAddSheet(app, ad.name, at)
			for _, d := range nd {
				if nm := attr(d, "name"); strings.HasPrefix(nm, "_xlnm.") {
					app = appAddNamed(app, quoted(ad.name)+"!"+strings.TrimPrefix(nm, "_xlnm."))
				}
			}
		}
		idx++
	}
	p.set("xl/workbook.xml", wb)
	p.set("xl/_rels/workbook.xml.rels", wbRels)
	p.set("[Content_Types].xml", ct)
	if hasApp {
		p.set("docProps/app.xml", app)
	}
	return nil
}

// appAddSheet: docProps/app.xml の「ワークシート」の数を1つ増やし、題名の並びにシート名を足す
func appAddSheet(app, name string, at int) string {
	hp := regexp.MustCompile(`(?s)<HeadingPairs>(.*?)</HeadingPairs>`).FindStringSubmatchIndex(app)
	tp := regexp.MustCompile(`(?s)<TitlesOfParts>(.*?)</TitlesOfParts>`).FindStringSubmatchIndex(app)
	if hp == nil || tp == nil {
		return app
	}
	heads := app[hp[2]:hp[3]]
	vars := regexp.MustCompile(`(?s)<vt:variant>(.*?)</vt:variant>`).FindAllStringSubmatchIndex(heads, -1)
	pos, found := 0, false
	base := 0
	var newHeads string
	for i := 0; i+1 < len(vars); i += 2 {
		label := heads[vars[i][2]:vars[i][3]]
		cnt := heads[vars[i+1][2]:vars[i+1][3]]
		m := regexp.MustCompile(`<vt:i4>(\d+)</vt:i4>`).FindStringSubmatchIndex(cnt)
		if m == nil {
			return app
		}
		n, _ := strconv.Atoi(cnt[m[2]:m[3]])
		if !found && (strings.Contains(label, "ワークシート") || strings.Contains(label, "Worksheets")) {
			found = true
			base = pos
			pos += n
			abs := hp[2] + vars[i+1][2] + m[2]
			newHeads = app[:abs] + strconv.Itoa(n+1) + app[abs+(m[3]-m[2]):]
			break
		}
		pos += n
	}
	if !found {
		return app
	}
	// 数を直した分、TitlesOfParts の位置がずれるので探し直す
	app = newHeads
	tp = regexp.MustCompile(`(?s)<TitlesOfParts>(.*?)</TitlesOfParts>`).FindStringSubmatchIndex(app)
	titles := app[tp[2]:tp[3]]
	vec := regexp.MustCompile(`<vt:vector size="(\d+)"`).FindStringSubmatchIndex(titles)
	if vec == nil {
		return app
	}
	size, _ := strconv.Atoi(titles[vec[2]:vec[3]])
	items := regexp.MustCompile(`(?s)<vt:lpstr>.*?</vt:lpstr>|<vt:lpstr/>`).FindAllStringIndex(titles, -1)
	ins := len(titles)
	if at >= 0 && base+at <= pos {
		pos = base + at
	}
	if pos < len(items) {
		ins = items[pos][0]
	} else if len(items) > 0 {
		ins = items[len(items)-1][1]
	} else {
		ins = strings.Index(titles, "</vt:vector>")
	}
	nt := titles[:ins] + "<vt:lpstr>" + encodeText(name) + "</vt:lpstr>" + titles[ins:]
	nt = nt[:vec[2]] + strconv.Itoa(size+1) + nt[vec[3]:]
	return app[:tp[2]] + nt + app[tp[3]:]
}

// sortedKeys（試験・表示用）
func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runeWidth: 1行の幅（全角1・半角0.5）
func runeWidth(s string) float64 {
	w := 0.0
	for _, r := range s {
		if r == '\t' {
			w += 2
		} else if r < 0x100 || (r >= 0xFF61 && r <= 0xFF9F) {
			w += 0.5
		} else {
			w++
		}
	}
	return w
}

// shiftSheetIndex: 番号 idx にシートを1つ入れたので、idx 以上を指している localSheetId・activeTab・firstSheet を1つずらす
// （入れたシート自身の名前は、あとで付ける）
func shiftSheetIndex(wb string, idx int) string {
	wb = reDefName.ReplaceAllStringFunc(wb, func(d string) string {
		v := attr(d, "localSheetId")
		if v == "" {
			return d
		}
		if n, err := strconv.Atoi(v); err == nil && n >= idx {
			return strings.Replace(d, `localSheetId="`+v+`"`, `localSheetId="`+strconv.Itoa(n+1)+`"`, 1)
		}
		return d
	})
	for _, a := range []string{"activeTab", "firstSheet"} {
		re := regexp.MustCompile(`(<workbookView\b[^>]*?\b` + a + `=")(\d+)(")`)
		wb = re.ReplaceAllStringFunc(wb, func(m string) string {
			g := re.FindStringSubmatch(m)
			if n, _ := strconv.Atoi(g[2]); n >= idx {
				return g[1] + strconv.Itoa(n+1) + g[3]
			}
			return m
		})
	}
	return wb
}

// appAddNamed: docProps/app.xml の「名前付き一覧」の数を1つ増やし、題名の並びの最後に足す
func appAddNamed(app, title string) string {
	hp := regexp.MustCompile(`(?s)<HeadingPairs>(.*?)</HeadingPairs>`).FindStringSubmatchIndex(app)
	tp := regexp.MustCompile(`(?s)<TitlesOfParts>(.*?)</TitlesOfParts>`).FindStringSubmatchIndex(app)
	if hp == nil || tp == nil {
		return app
	}
	heads := app[hp[2]:hp[3]]
	vars := regexp.MustCompile(`(?s)<vt:variant>(.*?)</vt:variant>`).FindAllStringSubmatchIndex(heads, -1)
	for i := 0; i+1 < len(vars); i += 2 {
		label := heads[vars[i][2]:vars[i][3]]
		if !strings.Contains(label, "名前付き") && !strings.Contains(label, "Named Ranges") {
			continue
		}
		cnt := heads[vars[i+1][2]:vars[i+1][3]]
		m := regexp.MustCompile(`<vt:i4>(\d+)</vt:i4>`).FindStringSubmatchIndex(cnt)
		if m == nil {
			return app
		}
		n, _ := strconv.Atoi(cnt[m[2]:m[3]])
		abs := hp[2] + vars[i+1][2] + m[2]
		app = app[:abs] + strconv.Itoa(n+1) + app[abs+(m[3]-m[2]):]
		tp = regexp.MustCompile(`(?s)<TitlesOfParts>(.*?)</TitlesOfParts>`).FindStringSubmatchIndex(app)
		titles := app[tp[2]:tp[3]]
		vec := regexp.MustCompile(`<vt:vector size="(\d+)"`).FindStringSubmatchIndex(titles)
		end := strings.Index(titles, "</vt:vector>")
		if vec == nil || end < 0 {
			return app
		}
		size, _ := strconv.Atoi(titles[vec[2]:vec[3]])
		nt := titles[:end] + "<vt:lpstr>" + encodeText(title) + "</vt:lpstr>" + titles[end:]
		nt = nt[:vec[2]] + strconv.Itoa(size+1) + nt[vec[3]:]
		return app[:tp[2]] + nt + app[tp[3]:]
	}
	return app
}
