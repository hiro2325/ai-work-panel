package main

// 支援経過（1日分）：2件目からは、サーバーの同じExcelにシートを足す（改善28 No.50・R8.10.7 承認）
//
//   - ⑥からサーバーへ保存するとき、その方の利用者フォルダ（いちばん上）に、パネルが前に保存した
//     「支援経過1日分_…xlsx」がちょうど1つあれば、新しいファイルを作らず、そのファイルに今回の日のシートを足す。
//   - シート名は「月.日」（同じ日の2件目は「月.日②」…）。並びは新しい日が左（「一覧」があればそのすぐ右。なければいちばん左）。
//   - 足す前に、そのファイルの写しを利用者フォルダの「控え」へ日時付きで取る。今あるシートは消さない・変えない。
//   - Excelで開いている・2つ以上ある・壊れているときは書かずに知らせる（別のファイルとして保存するかを選べる）。
//   - まだファイルがないとき（1件目）は今までどおり別ファイルで保存する。
//   - パネルのコードには、サーバーで消す操作を入れない（失敗しても .part や控えの途中のファイルは消さずに知らせる）。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const dayFilePrefix = "支援経過1日分_"

// srvAppend: 確認画面に出す「このファイルは、どのファイルにシートを足すか」
type srvAppend struct {
	Target   string       `json:"target"`             // 足す先のファイル名（見つけたのが1つのとき）
	Found    []string     `json:"found"`              // 見つけた「支援経過1日分_…xlsx」
	Sheet    string       `json:"sheet,omitempty"`    // 足すシートの名前
	Head     string       `json:"head,omitempty"`     // 1行の見出し（R8.10.7（水）…）
	Place    string       `json:"place,omitempty"`    // 「一覧のすぐ右」「いちばん左」
	Sheets   []string     `json:"sheets,omitempty"`   // 今あるシート
	Blocked  string       `json:"blocked,omitempty"`  // 足せない理由（書かずに知らせる）
	Dup      string       `json:"dup,omitempty"`      // この1日分がもう入っているシートの名前
	Warnings []string     `json:"warnings,omitempty"` // 注意
	Choices  []*srvAppend `json:"choices,omitempty"`  // 足す先が2つ以上のとき：足す先ごとの調べ結果（選んでもらう）
	Kind     string       `json:"kind,omitempty"`     // 改善29 No.57：book＝今ある「支援経過_氏名.xlsx」に足す／create＝「支援経過_氏名.xlsx」を新しく作る（なければ、前の形の「支援経過1日分_…xlsx」に足す）
	BookName string       `json:"bookName,omitempty"` // create のとき、作るファイルの名前
}

// srvMerge: 今ある「支援経過1日分_…xlsx」を「支援経過_氏名.xlsx」にまとめる計画（確認画面に出す。改善29 No.57）
type srvMerge struct {
	Legacy   string   `json:"legacy"`             // 今ある1日分のExcel
	Book     string   `json:"book"`               // まとめてできる「支援経過_氏名.xlsx」
	Sheets   int      `json:"sheets"`             // 今ある日のシートの数
	Names    []string `json:"names,omitempty"`    // 今あるシート
	Sheet    string   `json:"sheet,omitempty"`    // 足すシート
	Blocked  string   `json:"blocked,omitempty"`  // まとめられない理由（まとめずに、今までどおり足す形にする）
	Warnings []string `json:"warnings,omitempty"` // 注意
}

type dayAppendPlan struct {
	info      *srvAppend
	day       *kxDay
	srcHash   string
	targetSig string
	tmpl      string
	after     string // 「一覧」の右に入れるときだけ。空ならいちばん左
	sheetName string
	cands     map[string]*dayAppendPlan // 足す先が2つ以上のとき：足す先ごとの計画
	merge     *dayMergePlan             // 今ある1日分のExcelをまとめる計画（改善29 No.57。足す先が前の形の1日分Excelちょうど1つのとき）
}

type dayMergePlan struct {
	info      *srvMerge
	legacySig string
	tmpl      string
	sheetName string
}

// dayXlsxFiles: 利用者フォルダのいちばん上にある「支援経過1日分_」で始まる .xlsx（ロックファイル・途中のファイルは除く）
func dayXlsxFiles(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, "~$") || strings.HasPrefix(n, ".") || strings.HasSuffix(n, ".part") {
			continue
		}
		if strings.HasPrefix(n, dayFilePrefix) && strings.EqualFold(filepath.Ext(n), ".xlsx") {
			if st, err := os.Lstat(filepath.Join(dir, n)); err == nil && st.Mode().IsRegular() {
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

func dayShape(x string, ss []string) bool {
	return kxCell(x, "A4", ss) == "区分" && kxCell(x, "A6", ss) == "目的" && strings.Contains(x, `<mergeCell ref="A7:T41"/>`)
}

var reAnyCellRef = regexp.MustCompile(`<c r="([A-Z]+\d+)"`)

func allCellTexts(x string, ss []string) map[string]string {
	m := map[string]string{}
	for _, g := range reAnyCellRef.FindAllStringSubmatch(x, -1) {
		if _, ok := m[g[1]]; ok {
			continue
		}
		if t := kxCell(x, g[1], ss); t != "" {
			m[g[1]] = t
		}
	}
	return m
}

// 1日ごとに変わるセル（この日の値で入れ直す）
var dayVarCells = map[string]bool{"D2": true, "L2": true, "N2": true, "P2": true, "S2": true, "C4": true, "M4": true, "C5": true, "M5": true, "C6": true, "A7": true}

// dayTemplate: 足す先の中から、複製もとにするシートを選ぶ。今ある日のシート（いちばん左）か、「原紙」
// 届いた1日分（incoming）と、日ごとに変わるセル以外が同じ形であることも確かめる（違えば足さない）
func dayTemplate(p *ooPkg, incoming *ooPkg) (tmpl string, hasList bool, err error) {
	sheets, e := wbSheets(p)
	if e != nil {
		return "", false, fmt.Errorf("Excelの中身（シートの一覧）を読めません")
	}
	ss := sharedStrings(p)
	for _, sh := range sheets {
		if sh.name == keikaXList {
			hasList = true
		}
	}
	var inCells map[string]string
	if incoming != nil {
		isheets, e := wbSheets(incoming)
		if e != nil || len(isheets) != 1 {
			return "", hasList, fmt.Errorf("届いた1日分の形を読めません")
		}
		ix, _ := incoming.get(isheets[0].target)
		inCells = allCellTexts(ix, sharedStrings(incoming))
	}
	pick := ""
	for _, sh := range sheets {
		if reDaySheet.MatchString(toHalfDigits(sh.name)) {
			if x, ok := p.get(sh.target); ok && dayShape(x, ss) {
				pick = sh.name
				break
			}
		}
	}
	if pick == "" {
		for _, sh := range sheets {
			if sh.name == keikaXTemplate {
				if x, ok := p.get(sh.target); ok && dayShape(x, ss) {
					pick = sh.name
				}
			}
		}
	}
	if pick == "" {
		return "", hasList, fmt.Errorf("このExcelには、支援経過（1日分）の形のシート（月.日）が見つかりません。支援経過の1日分のExcelではないか、形が変わっています")
	}
	if pick != keikaXTemplate && inCells != nil {
		for _, sh := range sheets {
			if sh.name != pick {
				continue
			}
			x, _ := p.get(sh.target)
			tc := allCellTexts(x, ss)
			for ref, t := range tc {
				if !dayVarCells[ref] && inCells[ref] != t {
					return "", hasList, fmt.Errorf("今あるシート（%s）と、届いた1日分とで、固定の部分（%s）の中身が違うため、足しません", pick, ref)
				}
			}
			for ref, t := range inCells {
				if !dayVarCells[ref] && tc[ref] != t {
					return "", hasList, fmt.Errorf("今あるシート（%s）と、届いた1日分とで、固定の部分（%s）の中身が違うため、足しません", pick, ref)
				}
			}
		}
	}
	return pick, hasList, nil
}

func dayInsertAt(on []string) int {
	for i, n := range on {
		if n == keikaXList {
			return i + 1
		}
	}
	return 0
}

// buildDayAppend: 足す先のExcelに、1日分のシートを足した中身を作る（ほかの部品はそのまま。作ったあと自分で確かめる）
func buildDayAppend(base []byte, day *kxDay, sheetName, tmplName, after string) ([]byte, error) {
	p, err := readPkg(base)
	if err != nil {
		return nil, err
	}
	cells := map[string][2]string{
		"L2": xlsmNumCell(day.y), "N2": xlsmNumCell(day.m), "P2": xlsmNumCell(day.d),
		"A7": {"inlineStr", inlineStrXML(day.body)},
	}
	for ref, v := range map[string]string{"D2": day.code, "C4": day.kubun, "M4": day.jikan, "C5": day.basho, "M5": day.aite, "C6": day.mokuteki} {
		if v == "" {
			cells[ref] = [2]string{"", ""} // 複製もとの値を残さない（空にする）
		} else {
			cells[ref] = [2]string{"inlineStr", inlineStrXML(strings.ReplaceAll(v, "\n", "　"))}
		}
	}
	wd := day.weekday
	if wd == "" {
		wd = wdays[day.date.Weekday()]
	}
	ns := newSheet{name: sheetName, cells: cells, cache: map[string]string{"S2": wd}}
	if after == "" {
		err = addSheetsFromTemplateAt(p, tmplName, "", true, []newSheet{ns})
	} else {
		err = addSheetsFromTemplateAt(p, tmplName, after, false, []newSheet{ns})
	}
	if err != nil {
		return nil, err
	}
	// 印刷範囲：足したシート自身を指すようにする（複製もとの名前のままにしない）
	names, err := kxSheetNames(p)
	if err != nil {
		return nil, err
	}
	ni := -1
	for i, n := range names {
		if n == sheetName {
			ni = i
		}
	}
	if ni < 0 {
		return nil, fmt.Errorf("足したシートが見つかりません")
	}
	wb, _ := p.get("xl/workbook.xml")
	q := "'" + strings.ReplaceAll(sheetName, "'", "''") + "'"
	wb = reDefName.ReplaceAllStringFunc(wb, func(d string) string {
		if attr(d, "name") != "_xlnm.Print_Area" || attr(d, "localSheetId") != strconv.Itoa(ni) {
			return d
		}
		open := d[:strings.Index(d, ">")+1]
		txt := d[len(open):strings.LastIndex(d, "<")]
		rng := txt
		if i := strings.LastIndex(txt, "!"); i >= 0 {
			rng = txt[i+1:]
		}
		return open + encodeText(q) + "!" + rng + "</definedName>"
	})
	p.set("xl/workbook.xml", wb)
	out, err := p.bytes()
	if err != nil {
		return nil, err
	}
	if err := verifyKeikaXAt(base, out, sheetName, day, dayInsertAt); err != nil {
		return nil, err
	}
	return out, nil
}

// planDayAppend: 確認画面のために、足せるかどうかを調べる（何も書かない）。足す対象がなければ nil（今までどおり別ファイル）
// 改善29 No.57：利用者フォルダに「支援経過_氏名.xlsx」があればそれに足す。何もなければ（前の形の .xlsm も1日分Excelも）新しく作る。
// 今ある「支援経過1日分_…xlsx」がちょうど1つなら、足す形（今までどおり）に加えて「まとめる」の計画も出す。.xlsm がある方は今までどおり
func (s *panelServer) planDayAppend(dir, outName, userName string) *dayAppendPlan {
	data, err := os.ReadFile(filepath.Join(s.p.out, outName))
	if err != nil {
		return nil
	}
	day, err := parseDayXlsx(data)
	if err != nil {
		return nil
	}
	files := dayXlsxFiles(dir)
	books := bookXlsxFiles(dir)
	base := reDupSuffix.ReplaceAllString(outName, "$1")
	for _, f := range files {
		if sameFile(filepath.Join(dir, f), data) { // 同じ中身がもう保存されている（今までどおり「同じもの」で止める）
			return nil
		}
	}
	if sameFile(filepath.Join(dir, base), data) {
		return nil
	}
	sum := sha256.Sum256(data)
	srcHash := hex.EncodeToString(sum[:])
	switch {
	case len(books) > 1:
		return s.planChoices(dir, books, data, day, srcHash, true)
	case len(books) == 1:
		return s.planDayAppendOne(dir, books, books[0], data, day, srcHash, true)
	case len(keikaXlsmFiles(dir)) > 0: // 前の形（.xlsm）の方：今までの動きのまま
		if len(files) == 0 {
			return nil
		}
	case len(files) == 0:
		return s.planCreateBook(bookNameFor(dir, userName), data, day, srcHash)
	}
	pl := func() *dayAppendPlan {
		if len(files) > 1 {
			return s.planChoices(dir, files, data, day, srcHash, false)
		}
		return s.planDayAppendOne(dir, files, files[0], data, day, srcHash, false)
	}()
	if pl != nil && len(files) == 1 && len(keikaXlsmFiles(dir)) == 0 {
		pl.merge = s.planMerge(dir, userName, files[0], data, day, srcHash)
	}
	return pl
}

// planChoices: 足す先が2つ以上：どれに足すかは確認画面で選んでもらう（選ばなければ足さない）。選択肢ごとに、足せるかを調べておく
func (s *panelServer) planChoices(dir string, files []string, data []byte, day *kxDay, srcHash string, book bool) *dayAppendPlan {
	ap := &srvAppend{Found: files, Sheets: []string{}, Head: day.head(), Choices: []*srvAppend{}}
	if book {
		ap.Kind = "book"
	}
	pl := &dayAppendPlan{info: ap, day: day, srcHash: srcHash, cands: map[string]*dayAppendPlan{}}
	for _, f := range files {
		sub := s.planDayAppendOne(dir, files, f, data, day, srcHash, book)
		if sub == nil {
			continue
		}
		ap.Choices = append(ap.Choices, sub.info)
		pl.cands[f] = sub
	}
	return pl
}

// planCreateBook: 利用者フォルダに支援経過のExcelがまだないとき、「支援経過_氏名.xlsx」を新しく作る計画（何も書かない。作る中身を試しに作って確かめる）
func (s *panelServer) planCreateBook(name string, data []byte, day *kxDay, srcHash string) *dayAppendPlan {
	ap := &srvAppend{Kind: "create", Target: name, BookName: name, Found: []string{}, Sheets: []string{}, Head: day.head(), Sheet: day.sheet, Place: "「一覧」の右（新しい日が左）"}
	pl := &dayAppendPlan{info: ap, day: day, srcHash: srcHash}
	if n := day.bodyLines(); n > keikaXMaxLines {
		ap.Warnings = append(ap.Warnings, fmt.Sprintf("本文が約 %d 行あり、枠（%d行・1行47字）に入りきらない見込みです", n, keikaXMaxLines))
	}
	if _, err := buildBookNew(data); err != nil {
		ap.Blocked = "作る中身を作れませんでした：" + err.Error()
	}
	return pl
}

// planMerge: 今ある「支援経過1日分_…xlsx」（日のシートだけ）を「支援経過_氏名.xlsx」にまとめる計画（何も書かない）
func (s *panelServer) planMerge(dir, userName, legacy string, data []byte, day *kxDay, srcHash string) *dayMergePlan {
	bookName := bookNameFor(dir, userName)
	info := &srvMerge{Legacy: legacy, Book: bookName}
	mp := &dayMergePlan{info: info}
	path := filepath.Join(dir, legacy)
	if xlsmLocked(dir, legacy) {
		info.Blocked = legacy + " がExcelで開かれています。閉じてからもう一度開くと、まとめられます"
		return mp
	}
	mp.legacySig = fileSig(path)
	if _, err := os.Lstat(filepath.Join(dir, bookName)); err == nil {
		info.Blocked = "「" + bookName + "」という名前のものがすでにあるため、まとめられません"
		return mp
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() > 30<<20 {
		info.Blocked = legacy + " を読めません（大きすぎる・Excelで開いている など）"
		return mp
	}
	b, err := os.ReadFile(path)
	if err != nil {
		info.Blocked = legacy + " を読めません（Excelで開いていたら閉じてください）"
		return mp
	}
	if err := checkPkgXML(b); err != nil {
		info.Blocked = legacy + " が壊れているようです（Excelとして読めません）"
		return mp
	}
	tp, err := readPkg(b)
	if err != nil {
		info.Blocked = legacy + " をExcelとして開けません"
		return mp
	}
	ip, err := readPkg(data)
	if err != nil {
		return nil
	}
	names, _ := kxSheetNames(tp)
	info.Names = names
	if _, has := listSheetEntry(tp); has {
		info.Blocked = legacy + " に「" + keikaXList + "」のシートがすでにあるため、まとめられません"
		return mp
	}
	tmpl, _, err := dayTemplate(tp, ip)
	if err != nil {
		info.Blocked = err.Error()
		return mp
	}
	sheets, _ := wbSheets(tp)
	for _, sh := range sheets {
		if reDaySheet.MatchString(toHalfDigits(sh.name)) {
			info.Sheets++
		}
	}
	if info.Sheets != len(sheets) {
		info.Blocked = legacy + " に、日のシート（月.日）ではないシートが入っているため、まとめられません"
		return mp
	}
	nm, dup, err := kxChooseName(tp, day)
	if err != nil {
		info.Blocked = err.Error()
		return mp
	}
	if dup != "" {
		info.Blocked = "この1日分（" + day.head() + "）は、すでにシート「" + dup + "」に入っています"
		return mp
	}
	mp.tmpl, mp.sheetName, info.Sheet = tmpl, nm, nm
	if nm != day.sheet {
		info.Warnings = append(info.Warnings, "シート名「"+day.sheet+"」がすでにあるので、「"+nm+"」にして足します")
	}
	if _, err := buildBookMerge(b, day, nm, tmpl); err != nil {
		info.Blocked = "まとめる中身を作れませんでした：" + err.Error()
	}
	return mp
}

// planDayAppendOne: 足す先を1つ決めて、足せるかどうかを調べる（何も書かない）
func (s *panelServer) planDayAppendOne(dir string, files []string, target string, data []byte, day *kxDay, srcHash string, book bool) *dayAppendPlan {
	ap := &srvAppend{Found: files, Sheets: []string{}, Head: day.head()}
	if book {
		ap.Kind = "book"
	}
	pl := &dayAppendPlan{info: ap, day: day, srcHash: srcHash}
	ap.Target = target
	path := filepath.Join(dir, target)
	if xlsmLocked(dir, target) {
		ap.Blocked = target + " がExcelで開かれています。閉じてからもう一度開くと、足せます"
		return pl
	}
	pl.targetSig = fileSig(path)
	st, err := os.Stat(path)
	if err != nil || st.Size() > 30<<20 {
		ap.Blocked = target + " を読めません（大きすぎる・Excelで開いている など）"
		return pl
	}
	b, err := os.ReadFile(path)
	if err != nil {
		ap.Blocked = target + " を読めません（Excelで開いていたら閉じてください）"
		return pl
	}
	if err := checkPkgXML(b); err != nil {
		ap.Blocked = target + " が壊れているようです（Excelとして読めません）"
		return pl
	}
	tp, err := readPkg(b)
	if err != nil {
		ap.Blocked = target + " をExcelとして開けません"
		return pl
	}
	ip, err := readPkg(data)
	if err != nil {
		return nil
	}
	tmpl, hasList, err := dayTemplate(tp, ip)
	if err != nil {
		ap.Blocked = target + "：" + err.Error()
		return pl
	}
	if book && !hasList {
		ap.Blocked = target + " に「" + keikaXList + "」のシートがありません（支援経過をまとめたExcelの形ではありません）"
		return pl
	}
	pl.tmpl = tmpl
	if hasList {
		pl.after = keikaXList
		ap.Place = "「一覧」のすぐ右"
	} else {
		ap.Place = "いちばん左"
	}
	names, _ := kxSheetNames(tp)
	ap.Sheets = names
	nm, dup, err := kxChooseName(tp, day)
	if err != nil {
		ap.Blocked = err.Error()
		return pl
	}
	if dup != "" {
		ap.Dup = dup
		ap.Blocked = "この1日分（" + day.head() + "）は、すでにシート「" + dup + "」に入っています。2回目は足しません"
		return pl
	}
	pl.sheetName = nm
	ap.Sheet = nm
	if nm != day.sheet {
		ap.Warnings = append(ap.Warnings, "シート名「"+day.sheet+"」がすでにあるので、「"+nm+"」にして足します")
	}
	if n := day.bodyLines(); n > keikaXMaxLines {
		ap.Warnings = append(ap.Warnings, fmt.Sprintf("本文が約 %d 行あり、枠（%d行・1行47字）に入りきらない見込みです", n, keikaXMaxLines))
	}
	// 試し書き（メモリの中だけ）：できないなら、ここで知らせる
	var terr error
	if book {
		_, terr = buildBookAppend(b, day, nm, tmpl, pl.after)
	} else {
		_, terr = buildDayAppend(b, day, nm, tmpl, pl.after)
	}
	if terr != nil {
		ap.Blocked = "足す中身を作れませんでした：" + terr.Error()
		return pl
	}
	return pl
}

type dayAppendResult struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Sheet  string `json:"sheet"`
	Backup string `json:"backup"`
	Head   string `json:"head"`
	Kind   string `json:"kind,omitempty"`   // book（一覧にも1行足した）／create（新しく作った）／merge（まとめた）。なければ今までの足し方
	Merged int    `json:"merged,omitempty"` // まとめたとき：移したシートの数
	Moved  string `json:"moved,omitempty"`  // まとめたとき：古いファイルを移した先（控え＼…）
	Warn   string `json:"warn,omitempty"`   // 注意（できたけれど、知らせたいこと）
}

// doDayAppend: 確認画面で見せたとおりに、足す先のExcelにシートを足す。
// 書く前にもう一度：1つだけ・開かれていない・確認のあとで変わっていない・元の書類も変わっていない。
// 控え（同じフォルダの「控え」）→ 一時ファイル（.part）に書いて確かめる → 元の名前へ入れ替える。サーバーでは何も消さない
func (s *panelServer) doDayAppend(dir, outName string, pl *dayAppendPlan, now time.Time) (*dayAppendResult, error) {
	ap := pl.info
	if ap.Blocked != "" || ap.Target == "" {
		return nil, fmt.Errorf("%s", ap.Blocked)
	}
	data, err := os.ReadFile(filepath.Join(s.p.out, outName))
	sum := sha256.Sum256(data)
	if err != nil || hex.EncodeToString(sum[:]) != pl.srcHash {
		return nil, fmt.Errorf("確認の画面のあとで、元の書類（%s）が変わりました。もう一度開き直してください（まだ何も書いていません）", outName)
	}
	isBook := ap.Kind == "book"
	cur := dayXlsxFiles(dir)
	if isBook {
		cur = bookXlsxFiles(dir)
	}
	if strings.Join(cur, "\n") != strings.Join(ap.Found, "\n") {
		if isBook {
			return nil, fmt.Errorf("確認の画面のあとで、利用者フォルダの「支援経過_…」のExcelが変わりました。もう一度開き直してください（まだ何も書いていません）")
		}
		return nil, fmt.Errorf("確認の画面のあとで、利用者フォルダの「支援経過1日分」のExcelが変わりました。もう一度開き直してください（まだ何も書いていません）")
	}
	tpath := filepath.Join(dir, ap.Target)
	if filepath.Dir(tpath) != dir {
		return nil, fmt.Errorf("書く先が正しくありません（まだ何も書いていません）")
	}
	if xlsmLocked(dir, ap.Target) {
		return nil, fmt.Errorf("%s がExcelで開かれています。閉じてから、もう一度押してください（まだ何も書いていません）", ap.Target)
	}
	if fileSig(tpath) != pl.targetSig {
		return nil, fmt.Errorf("確認の画面のあとで、%s が変わりました。もう一度開き直してください（まだ何も書いていません）", ap.Target)
	}
	orig, err := os.ReadFile(tpath)
	if err != nil {
		return nil, fmt.Errorf("%s を読めませんでした（Excelで開いていたら閉じてください）。まだ何も書いていません", ap.Target)
	}
	if fileSig(tpath) != pl.targetSig {
		return nil, fmt.Errorf("読んでいる間に %s が変わりました。もう一度開き直してください（まだ何も書いていません）", ap.Target)
	}
	tp, err := readPkg(orig)
	if err != nil {
		return nil, fmt.Errorf("%s をExcelとして開けません（まだ何も書いていません）", ap.Target)
	}
	ip, err := readPkg(data)
	if err != nil {
		return nil, fmt.Errorf("元の書類をExcelとして開けません（まだ何も書いていません）")
	}
	tmpl, hasList, err := dayTemplate(tp, ip)
	if err != nil {
		return nil, fmt.Errorf("%v（まだ何も書いていません）", err)
	}
	after := ""
	if hasList {
		after = keikaXList
	}
	nm, dup, err := kxChooseName(tp, pl.day)
	if err != nil {
		return nil, fmt.Errorf("%v（まだ何も書いていません）", err)
	}
	if dup != "" {
		return nil, fmt.Errorf("この1日分は、すでにシート「%s」に入っています。2回目は足しません（まだ何も書いていません）", dup)
	}
	var newData []byte
	if isBook {
		if !hasList {
			return nil, fmt.Errorf("%s に「%s」のシートがありません（まだ何も書いていません）", ap.Target, keikaXList)
		}
		newData, err = buildBookAppend(orig, pl.day, nm, tmpl, after)
	} else {
		newData, err = buildDayAppend(orig, pl.day, nm, tmpl, after)
	}
	if err != nil {
		return nil, fmt.Errorf("足す中身を作れませんでした（まだ何も書いていません）：%v", err)
	}
	// 控え（同じフォルダの「控え」。消さない・上書きしない）
	bdir := filepath.Join(dir, keikaBackupDir)
	if err := os.MkdirAll(bdir, 0755); err != nil {
		return nil, fmt.Errorf("「控え」のフォルダを作れませんでした（まだ何も書いていません）：%v", err)
	}
	stamp := now.Format("20060102_150405")
	bp, err := writeNoDelete(bdir, stamp+"_"+ap.Target, orig)
	if err != nil || !sameFile(bp, orig) {
		return nil, fmt.Errorf("控えを正しく取れませんでした。%s は書き換えていません。控えの途中のファイルが残っていたら、あとで手で消してください（パネルはサーバーで消しません）", ap.Target)
	}
	part, err := writeNoDelete(dir, ap.Target+".part", newData)
	if err != nil || !sameFile(part, newData) {
		return nil, fmt.Errorf("足したファイルを正しく作れませんでした。%s はそのままです（控え：%s）。途中のファイル（%s）が残っていたら、あとで手で消してください", ap.Target, filepath.Base(bp), filepath.Base(part))
	}
	if xlsmLocked(dir, ap.Target) || fileSig(tpath) != pl.targetSig {
		return nil, fmt.Errorf("書く直前に %s が開かれたか変わったため、入れ替えませんでした。%s はそのままです。途中のファイル（%s）はそのまま残しています", ap.Target, ap.Target, filepath.Base(part))
	}
	if err := os.Rename(part, tpath); err != nil {
		return nil, fmt.Errorf("%s に入れ替えられませんでした（Excelで開いていたら閉じてください）。%s はそのままです。途中のファイル（%s）はそのまま残しています：%v", ap.Target, ap.Target, filepath.Base(part), err)
	}
	r := &dayAppendResult{Name: outName, Target: ap.Target, Sheet: nm, Backup: keikaBackupDir + "＼" + filepath.Base(bp), Head: ap.Head}
	if isBook {
		r.Kind = "book"
	}
	return r, nil
}

func (s *panelServer) writeDayAppendHistory(user, sheet string) {
	line := time.Now().Format("2006/01/02 15:04") + "\t【" + user + "】\t支援経過（1日分）\tシート「" + sheet + "」を支援経過1日分のExcelに足した"
	path := filepath.Join(s.p.report, serverHistoryName)
	_, statErr := os.Stat(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	if os.IsNotExist(statErr) {
		f.WriteString(bom + "日付\t記号\t書類\t件数\r\n")
	}
	f.WriteString(line + "\r\n")
}
