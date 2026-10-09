package main

// 置き換えた後の残りチェック（赤の「処理できませんでした」）の出し方（改善12 No.33・No.36）
//  - 何シートの何セルか、Wordなら何段落目か・ヘッダーなどを出す（xml のファイル名は出さない）
//  - 同じ1文字の数字だけが6文字以上並ぶもの（33333333・00000000 など）は問題にしない
//  - 氏名一覧の「除外」に入れた数字・語は問題にしない（名前・住所として登録した語は除外しても止める）
//  - 赤の種類（数式・名前の定義・リンク・コメント・埋め込み・PDFの残り・その他）ごとに原因と直し方を出す
// 名前の見つけ方（rules.go の leftoverCheck）そのものは変えない。結果をふるい分けて、場所を付けるだけ。

import (
	"archive/zip"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// 赤の種類（No.36）
const (
	lkFormula = "数式"
	lkDefName = "名前の定義"
	lkLink    = "リンク"
	lkComment = "コメント"
	lkEmbed   = "埋め込み"
	lkPDF     = "PDFの残り"
	lkOther   = "その他"
)

type leftHit struct {
	Word   string `json:"word"`           // 残った語（この画面だけに出す）
	Loc    string `json:"loc"`            // 場所（「3表」シートの D12 セル など）
	Kind   string `json:"kind"`           // 赤の種類
	Excl   bool   `json:"excl,omitempty"` // 「これは個人情報ではない（除外して置き換え直す）」を出してよい
	Reg    bool   `json:"reg,omitempty"`  // 氏名一覧に登録した名前・住所そのもの
	Shape  string `json:"-"`              // エラー記録用の形（語そのものは書かない）
	LogLoc string `json:"-"`              // エラー記録用の場所（シート名は様式の名前だけ）
}

type fixGuide struct {
	Kind  string   `json:"kind"`
	Cause string   `json:"cause"`
	Steps []string `json:"steps"`
}

// sameDigitRun: 同じ1文字の数字だけが6文字以上続くもの（33333333・００００００ など）
func sameDigitRun(w string) bool {
	rs := []rune(strings.TrimSpace(w))
	if len(rs) < 6 {
		return false
	}
	norm := func(r rune) rune {
		if r >= '０' && r <= '９' {
			return r - '０' + '0'
		}
		return r
	}
	f := norm(rs[0])
	if f < '0' || f > '9' {
		return false
	}
	for _, r := range rs[1:] {
		if norm(r) != f {
			return false
		}
	}
	return true
}

// leftWord: leftoverCheck の1件（「部品：「語」」）から語を取り出す
func leftWord(hit, part string) string {
	w := strings.TrimPrefix(hit, part+"：「")
	return strings.TrimSuffix(w, "」")
}

// isRegisteredWord: 氏名一覧に登録した名前・住所（そのもの）か
func (m *masker) isRegisteredWord(w string) bool {
	return len(m.nameMatches(w)) > 0 || len(m.addrRegLumps(w)) > 0
}

// leftoverCheckFiltered: 残りチェックの結果から、問題にしないものを外す（No.33 ②③）
func (m *masker) leftoverCheckFiltered(part, text string) []string {
	var out []string
	for _, h := range m.leftoverCheck(part, text) {
		w := leftWord(h, part)
		if m.isRegisteredWord(w) {
			out = append(out, h) // 名前・住所として登録した語は、除外に入れても止める
			continue
		}
		if sameDigitRun(w) || m.exclude[strings.TrimSpace(w)] {
			continue
		}
		out = append(out, h)
	}
	return out
}

// wordShape: エラー記録に書く「形」（語そのものは書かない。No.43）
func wordShape(w string, reg bool) string {
	t := strings.TrimSpace(w)
	n := utf8.RuneCountInString(t)
	switch {
	case reg:
		return "氏名一覧に登録した名前・住所"
	case t == "":
		return "（語なし）"
	case reMail.MatchString(t):
		return "メールアドレスの形"
	case reOnlyDigitsFull.MatchString(t):
		return fmt.Sprintf("%dけたの数字", n)
	case reOldDate.MatchString(t) || reDateLike.MatchString(t):
		return "生年月日・日付の形"
	case rePhoneHy.MatchString(t) || rePhoneSp.MatchString(t):
		return "電話番号の形"
	case reZip.MatchString(t):
		return "郵便番号の形"
	case reHasDigit.MatchString(t):
		return fmt.Sprintf("数字を含む語（%d字）", n)
	}
	return fmt.Sprintf("文字（%d字）", n)
}

var (
	reOnlyDigitsFull = regexp.MustCompile(`^[0-9０-９]+$`)
	reDateLike       = regexp.MustCompile(`[0-9０-９]{1,4}\s*[年./／]\s*[0-9０-９]{1,2}\s*[月./／]\s*[0-9０-９]{1,2}`)
	reWPara          = regexp.MustCompile(`<w:p[ >]`)
	reCellRefAttr    = regexp.MustCompile(`\br="([A-Z]{1,3}[0-9]{1,7})"`)
	reSqref          = regexp.MustCompile(`\b(?:sqref|ref)="([^"]{1,40})"`)
	reDefNameAttr    = regexp.MustCompile(`\bname="([^"]{1,60})"`)
	reSSTCellRef     = regexp.MustCompile(`<c r="([A-Z]{1,3}[0-9]{1,7})"[^>]*\bt="s"[^>]*>(?:<f[^>]*/>|<f[^>]*>[^<]*</f>)?<v>([0-9]+)</v>`)
	reSIOpen         = regexp.MustCompile(`<si[ >]`)
)

// enclosing: text[i] が <tag ...> … </tag>（または <tag …/>）の中なら、その開始位置
func enclosing(text string, i int, tag string) int {
	st := strings.LastIndex(text[:i], "<"+tag)
	for st >= 0 {
		rest := text[st+1+len(tag):]
		if rest != "" && (rest[0] == ' ' || rest[0] == '>' || rest[0] == '/') {
			break
		}
		st = strings.LastIndex(text[:st], "<"+tag)
	}
	if st < 0 {
		return -1
	}
	gt := strings.Index(text[st:], ">")
	if gt < 0 {
		return -1
	}
	if text[st+gt-1] == '/' { // <tag …/>
		if i < st+gt {
			return st
		}
		return -1
	}
	end := strings.Index(text[st:], "</"+tag+">")
	if end < 0 || i < st+end {
		return st
	}
	return -1
}

// 様式でよく使うシート名だけを、エラー記録にそのまま書く（利用者の名前をシート名にしている場合があるため）
var reSafeSheet = regexp.MustCompile(`^(?:第?[0-9０-９]{1,2}表(?:[（(][^）)]{0,6}[）)])?|[0-9０-９]{1,2}表|フェイスシート|アセスメント[^\s]{0,8}|モニタリング[^\s]{0,8}|評価表|要点|支援経過[^\s]{0,6}|別紙[0-9０-９]{0,2}|家族構成図|間取り図|基本情報|課題整理総括表|週間サービス計画表|サービス利用票|提供票|Sheet[0-9]{1,3}|シート[0-9０-９]{1,3}|表紙|記入例)$`)

func safeSheetLabel(name string, idx int) string {
	if reSafeSheet.MatchString(strings.TrimSpace(name)) {
		return "「" + strings.TrimSpace(name) + "」シート"
	}
	if idx > 0 {
		return fmt.Sprintf("%d枚目のシート（名前は書きません）", idx)
	}
	return "シート（名前は書きません）"
}

// sheetOrder: シートの部品名 → 何枚目か
func sheetOrder(zr *zip.Reader) map[string]int {
	out := map[string]int{}
	if zr == nil {
		return out
	}
	wb := zipRead(zr, "xl/workbook.xml")
	rels := zipRead(zr, "xl/_rels/workbook.xml.rels")
	idToTarget := map[string]string{}
	for _, r := range reRel.FindAll(rels, -1) {
		id, tg := reAId.FindSubmatch(r), reATarget.FindSubmatch(r)
		if id != nil && tg != nil {
			t := string(tg[1])
			if strings.HasPrefix(t, "/") {
				t = strings.TrimPrefix(t, "/")
			} else {
				t = "xl/" + t
			}
			idToTarget[string(id[1])] = t
		}
	}
	for n, sh := range reWbSheet.FindAll(wb, -1) {
		if rid := reARid.FindSubmatch(sh); rid != nil {
			out[idToTarget[string(rid[1])]] = n + 1
		}
	}
	return out
}

// locateLeft: 残った語の場所と種類（No.33 ①・No.36）
func (m *masker) locateLeft(zr *zip.Reader, sheetNames map[string]string, prefix, part, text, w string) leftHit {
	h := leftHit{Word: w, Kind: lkOther}
	h.Reg = m.isRegisteredWord(w)
	h.Excl = !h.Reg && w != ""
	h.Shape = wordShape(w, h.Reg)
	i := strings.Index(text, w)
	if i < 0 {
		i = 0
	}
	order := sheetOrder(zr)
	sheetLab := func(p string) (string, string) {
		n := sheetNames[p]
		if n == "" {
			n = fmt.Sprintf("%d枚目", order[p])
			return n + "のシート", safeSheetLabel("", order[p])
		}
		return "「" + n + "」シート", safeSheetLabel(n, order[p])
	}
	set := func(kind, loc, logLoc string) {
		h.Kind, h.Loc, h.LogLoc = kind, loc, logLoc
	}
	low := strings.ToLower(part)
	switch {
	case reSheetPart.MatchString(part):
		sl, sll := sheetLab(part)
		if c := enclosing(text, i, "c"); c >= 0 {
			ref := ""
			if mm := reCellRefAttr.FindStringSubmatch(text[c:min(len(text), c+120)]); mm != nil {
				ref = mm[1]
			}
			if f := enclosing(text, i, "f"); f >= c {
				set(lkFormula, sl+"の "+ref+" セルの数式", sll+" "+ref+" セルの数式")
			} else {
				set(lkOther, sl+"の "+ref+" セル", sll+" "+ref+" セル")
			}
		} else if hl := enclosing(text, i, "hyperlink"); hl >= 0 {
			ref := ""
			if mm := reSqref.FindStringSubmatch(text[hl:min(len(text), hl+200)]); mm != nil {
				ref = mm[1]
			}
			set(lkLink, sl+"の "+ref+" セルのリンク", sll+" "+ref+" セルのリンク")
		} else if dv := enclosing(text, i, "dataValidation"); dv >= 0 {
			set(lkFormula, sl+"の入力規則（プルダウンなど）", sll+"の入力規則")
		} else if cf := enclosing(text, i, "conditionalFormatting"); cf >= 0 {
			set(lkFormula, sl+"の条件付き書式", sll+"の条件付き書式")
		} else if strings.Contains(text[max(0, i-400):i], "Header") || strings.Contains(text[max(0, i-400):i], "Footer") {
			set(lkOther, sl+"のヘッダー・フッター", sll+"のヘッダー・フッター")
		} else {
			set(lkOther, sl+"の中（セル以外の所）", sll+"の中（セル以外）")
		}
	case part == "xl/sharedStrings.xml":
		idx := len(reSIOpen.FindAllStringIndex(text[:i], -1)) - 1
		loc, logLoc := "Excelの文字の置き場（どのセルか分かりません）", "Excelの文字の置き場"
		if zr != nil && idx >= 0 {
			for _, f := range zr.File {
				if !reSheetPart.MatchString(f.Name) {
					continue
				}
				for _, mm := range reSSTCellRef.FindAllSubmatch(zipRead(zr, f.Name), -1) {
					if string(mm[2]) == fmt.Sprint(idx) {
						sl, sll := sheetLab(f.Name)
						loc, logLoc = sl+"の "+string(mm[1])+" セル", sll+" "+string(mm[1])+" セル"
						break
					}
				}
				if !strings.HasPrefix(loc, "Excelの") {
					break
				}
			}
		}
		set(lkOther, loc, logLoc)
	case part == "xl/workbook.xml":
		if d := enclosing(text, i, "definedName"); d >= 0 {
			nm := ""
			if mm := reDefNameAttr.FindStringSubmatch(text[d:min(len(text), d+200)]); mm != nil {
				nm = "「" + mm[1] + "」"
			}
			set(lkDefName, "名前の定義"+nm, "名前の定義")
		} else if enclosing(text, i, "sheet") >= 0 {
			set(lkOther, "シートの名前", "シートの名前")
		} else {
			set(lkOther, "ブックの設定", "ブックの設定")
		}
	case strings.HasSuffix(low, ".rels") || strings.HasPrefix(part, "xl/externalLinks/"):
		set(lkLink, "リンク（ほかのファイル・ホームページへのつながり）", "リンク")
	case reCmtPart.MatchString(part) || reThrPart.MatchString(part) || strings.HasSuffix(low, ".vml") || part == "word/comments.xml":
		ref := ""
		if c := enclosing(text, i, "comment"); c >= 0 {
			if mm := reSqref.FindStringSubmatch(text[c:min(len(text), c+200)]); mm != nil {
				ref = "（" + mm[1] + " セル）"
			}
		}
		set(lkComment, "コメント（メモ）"+ref, "コメント"+ref)
	case part == "word/document.xml":
		n := len(reWPara.FindAllStringIndex(text[:i], -1))
		if n < 1 {
			n = 1
		}
		loc := fmt.Sprintf("本文の %d 段落目（表の中も数えます）", n)
		if enclosing(text, i, "w:hyperlink") >= 0 || (enclosing(text, i, "w:instrText") >= 0 && strings.Contains(text[max(0, i-200):i], "HYPERLINK")) {
			set(lkLink, loc+"のリンク", fmt.Sprintf("本文の %d 段落目のリンク", n))
		} else if enclosing(text, i, "w:instrText") >= 0 {
			set(lkOther, loc+"の差し込みの欄（フィールド）", fmt.Sprintf("本文の %d 段落目のフィールド", n))
		} else {
			set(lkOther, loc, fmt.Sprintf("本文の %d 段落目", n))
		}
	case strings.HasPrefix(part, "word/header"):
		set(lkOther, "Wordのヘッダー（ページの上の余白）", "ヘッダー")
	case strings.HasPrefix(part, "word/footer"):
		set(lkOther, "Wordのフッター（ページの下の余白）", "フッター")
	case strings.HasPrefix(part, "word/footnotes") || strings.HasPrefix(part, "word/endnotes"):
		set(lkOther, "Wordの脚注", "脚注")
	case strings.HasPrefix(part, "docProps/"):
		set(lkOther, "ファイルの情報（タイトル・作成者など）", "ファイルの情報")
	case strings.Contains(part, "/charts/"):
		set(lkOther, "グラフの中の文字", "グラフ")
	case strings.Contains(part, "/drawings/") || strings.Contains(part, "/diagrams/"):
		set(lkOther, "図形・テキストボックスの中の文字", "図形・テキストボックス")
	default:
		set(lkOther, "ファイルの中の設定部分", "ファイルの設定部分")
	}
	if prefix != "" {
		h.Kind = lkEmbed
		h.Loc = "埋め込まれたファイルの中（" + h.Loc + "）"
		h.LogLoc = "埋め込まれたファイルの中"
	}
	return h
}

// pdfLeftHits: PDF の読み取りで残ったもの（「3ページ目：「語」」）
func (m *masker) pdfLeftHits(hits []string) []leftHit {
	var out []leftHit
	for _, h := range hits {
		i := strings.Index(h, "：「")
		if i < 0 {
			continue
		}
		pg, w := h[:i], leftWord(h, h[:i])
		lh := leftHit{Word: w, Kind: lkPDF, Loc: "PDFの " + pg + "（読み取った文）", LogLoc: "PDFの " + pg}
		lh.Reg = m.isRegisteredWord(w)
		lh.Excl = !lh.Reg && w != ""
		lh.Shape = wordShape(w, lh.Reg)
		out = append(out, lh)
	}
	return out
}

// leftGuides: 赤の種類ごとの原因と、押すボタン・やること（1〜3手。No.36）
func leftGuides(hits []leftHit, err string) []fixGuide {
	kinds := []string{}
	seen := map[string]bool{}
	anyExcl, anyReg := false, false
	for _, h := range hits {
		if !seen[h.Kind] {
			seen[h.Kind] = true
			kinds = append(kinds, h.Kind)
		}
		anyExcl = anyExcl || h.Excl
		anyReg = anyReg || h.Reg
	}
	if len(hits) == 0 {
		k := lkOther
		if strings.Contains(err, "名前の定義") {
			k = lkDefName
		}
		kinds = append(kinds, k)
	}
	var out []fixGuide
	for _, k := range kinds {
		g := fixGuide{Kind: k}
		switch k {
		case lkFormula:
			g.Cause = "セルの数式（=で始まる計算）の中に、名前や番号がそのまま書かれています。数式の中は置き換えられません。"
			g.Steps = []string{"元のExcelで、下に出ているセルを開く", "数式をやめて、値だけにする（コピー →「値の貼り付け」）", "保存して、もう一度入れる"}
		case lkDefName:
			g.Cause = "Excelの「名前の定義」に、名前が使われています。"
			g.Steps = []string{"元のExcelで［数式］→［名前の管理］を開く", "名前の入った定義を消すか、別の名前に変える", "保存して、もう一度入れる"}
		case lkLink:
			g.Cause = "リンク（ほかのファイルやホームページへのつながり）の中に、名前や番号が入っています。"
			g.Steps = []string{"元のファイルで、そのセル・文字を右クリック →［リンクの削除］", "保存して、もう一度入れる"}
		case lkComment:
			g.Cause = "コメント（メモ）の中に、置き換えられない形で残っています。"
			g.Steps = []string{"元のファイルで、そのセルのコメントを開く", "名前を消すか、コメントを削除する", "保存して、もう一度入れる"}
		case lkEmbed:
			g.Cause = "ファイルの中に貼り付けたExcel・Wordなど（埋め込み）の中に残っています。"
			g.Steps = []string{"元のファイルで、貼り付けた表・図を消す（または画像にする）", "保存して、もう一度入れる"}
		case lkPDF:
			g.Cause = "PDFの読み取った文に、置き換えられない形で残っています（生年月日・番号など）。"
			g.Steps = []string{"個人情報でなければ、下の「これは個人情報ではない」を押す", "個人情報なら「このファイルは取り消す」を押し、Excel・Wordで作り直すか、手で消したPDFを入れ直す"}
			if anyReg {
				g.Steps = []string{"氏名一覧で、その方の呼び名に、読み取った書き方も書き足す", "「もう一度置き換える」を押す", "それでも残るときは「このファイルは取り消す」"}
			}
		default:
			g.Cause = "置き換えた後も、名前や番号が残る所がありました。"
			switch {
			case anyExcl && !anyReg:
				g.Steps = []string{"個人情報でなければ、下の「これは個人情報ではない（除外して置き換え直す）」を押す", "個人情報なら、元のファイルのその場所を直して、もう一度入れる"}
			case strings.Contains(err, "開けません") || strings.Contains(err, "読み取り"):
				g.Cause = "ファイルが開けませんでした（壊れているか、形が違います）。"
				g.Steps = []string{"元のファイルをExcel・Wordで開いて、名前を付けて保存し直す（.xlsx／.docx）", "もう一度入れる"}
			default:
				g.Steps = []string{"元のファイルの、下に出ている場所を開く", "名前や番号を消すか書き直して保存する", "もう一度入れる"}
			}
		}
		out = append(out, g)
	}
	return out
}
