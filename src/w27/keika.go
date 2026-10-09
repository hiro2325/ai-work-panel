package main

// 支援経過の自動化（R8.9.27 改善4・パネル改善リスト No.12・No.6）
//
//   - 今日の支援経過：文字起こしを貼る → Word にして 1_元データ へ（AIに渡す書類は「支援経過（1日分）」）
//   - 書き足す：AIから届いた1日分（元に戻したWord）を、記録（Excel）の新しいシートと一覧（Word）の一番上に足す
//   - 切り出す：一覧から期間内の件を抜き出したWordを 1_元データ へ
//   - 写し直す：年が一部にしか書かれていない今の一覧から、各件の頭に年入りの日付を付けた新しい一覧を作る
//
// 置き場所（試運転）：マスキング作業\支援経過\（利用者の氏名）\ だけ。AI作業・OneDrive・サーバーには書かない。
// 何も消さない。書き足す前に 控え\ に写しを取る。どれもボタンを押したときだけ書く。

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed template_keika_record.xlsx
var templateKeikaRecord []byte

//go:embed template_keika_list.docx
var templateKeikaList []byte

const (
	keikaDirName    = "支援経過"
	keikaRecordName = "支援経過記録.xlsx"
	keikaListName   = "支援経過一覧.docx"
	keikaBackupDir  = "控え"
	keikaTemplate   = "原紙①"
	keikaDocLabel   = "支援経過（1日分）"
	keikaDocKey     = "支援経過1日分" // 30・40 のフォルダ名に使う（「（」を入れない）
	// 記録の本文の枠（A4:T39）に入る目安：1行の字数・行数（様式係の採寸：約49字×約34行。少し余裕を見る）
	keikaLineChars = 48.0
	keikaMaxLines  = 33
)

var wdays = []string{"日", "月", "火", "水", "木", "金", "土"}

func warekiDate(t time.Time) string {
	return fmt.Sprintf("R%d.%d.%d", t.Year()-2018, int(t.Month()), t.Day())
}

func warekiDateW(t time.Time) string { return warekiDate(t) + "（" + wdays[t.Weekday()] + "）" }

var reWarekiDate = regexp.MustCompile(`^R(\d{1,2})\.(\d{1,2})\.(\d{1,2})$`)

func parseWareki(s string) (time.Time, bool) {
	m := reWarekiDate.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return time.Time{}, false
	}
	return mkDate(atoi(m[1])+2018, atoi(m[2]), atoi(m[3]))
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func mkDate(y, m, d int) (time.Time, bool) {
	if m < 1 || m > 12 || d < 1 || d > 31 || y < 1990 || y > 2100 {
		return time.Time{}, false
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.Local)
	if t.Month() != time.Month(m) || t.Day() != d {
		return time.Time{}, false
	}
	return t, true
}

// 画面の日付（2026-09-27）
func parseISODate(s string) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(s), time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func toHalfDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '０' && r <= '９' {
			return r - '０' + '0'
		}
		if r == 'Ｒ' {
			return 'R'
		}
		if r == '．' {
			return '.'
		}
		return r
	}, s)
}

// ---------------- 場所 ----------------

func (s *panelServer) keikaRoot() string { return filepath.Join(s.p.work, keikaDirName) }

// keikaUser: 利用者の記号（利用者001）→ 氏名（氏名一覧の書き方）とフォルダ
func (s *panelServer) keikaUser(id string) (name, dir string, err error) {
	if !reUserID.MatchString(id) {
		return "", "", fmt.Errorf("利用者を選んでください")
	}
	people, excl, err := readNameList(s.p.nameList)
	if err != nil {
		return "", "", err
	}
	cb, err := loadCodeBook(s.p.codeBookF)
	if err != nil {
		return "", "", err
	}
	newMasker(people, excl, cb)
	for _, p := range people {
		if sanitizePrefix(p.kind) == "利用者" && cb.keyToCode["名|利用者|"+p.name] == "【"+id+"】" {
			name = cleanName(p.name)
		}
	}
	if name == "" {
		return "", "", fmt.Errorf("氏名一覧に %s の方が見つかりません", id)
	}
	folder := strings.TrimSpace(reBadFileChar.ReplaceAllString(normName(name), ""))
	folder = strings.Trim(folder, ". ")
	if folder == "" || !safeName(folder) {
		return "", "", fmt.Errorf("氏名からフォルダの名前を作れません")
	}
	dir = filepath.Join(s.keikaRoot(), folder)
	if err := s.keikaGuard(dir); err != nil {
		return "", "", err
	}
	return name, dir, nil
}

// keikaGuard: 書く場所の守り。マスキング作業\支援経過 の中だけ。AI作業・OneDrive の中は拒否
func (s *panelServer) keikaGuard(target string) error {
	root := s.keikaRoot()
	if !within(root, target) {
		return fmt.Errorf("支援経過のファイルは マスキング作業＼支援経過 の中にだけ置きます")
	}
	if s.p.ai != "" && within(s.p.ai, target) {
		return fmt.Errorf("安全のため中止します：支援経過のファイルを AI作業 の中には書きません")
	}
	abs, _ := filepath.Abs(target)
	for _, seg := range strings.Split(filepath.ToSlash(abs), "/") {
		if strings.HasPrefix(strings.ToLower(seg), "onedrive") {
			return fmt.Errorf("安全のため中止します：マスキング作業フォルダが OneDrive の中にあります。支援経過のファイルは OneDrive には書きません")
		}
	}
	if od := os.Getenv("OneDrive"); od != "" && within(od, target) {
		return fmt.Errorf("安全のため中止します：マスキング作業フォルダが OneDrive の中にあります。支援経過のファイルは OneDrive には書きません")
	}
	return nil
}

func officeOpen(path string) bool {
	dir, base := filepath.Dir(path), filepath.Base(path)
	cands := []string{"~$" + base}
	if rs := []rune(base); len(rs) > 2 {
		cands = append(cands, "~$"+string(rs[2:]))
	}
	for _, c := range cands {
		if _, err := os.Stat(filepath.Join(dir, c)); err == nil {
			return true
		}
	}
	return false
}

func fileSig(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return "none"
	}
	return fmt.Sprintf("%d|%d", st.Size(), st.ModTime().UnixNano())
}

// writeNew: 新しいファイルとして書く（すでにあれば書かない）
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// replaceFile: 控えを取ったあとで、書き足した中身に入れ替える（.part に書いてから名前を変える）
func replaceFile(path string, data []byte) error {
	part := path + ".part"
	if err := os.WriteFile(part, data, 0600); err != nil {
		os.Remove(part)
		return err
	}
	if err := os.Rename(part, path); err != nil {
		os.Remove(part)
		return err
	}
	return nil
}

// ---------------- 1日分の Word を読む ----------------

// 1行目：R8.9.27（日）16:00【見出し】（時刻・見出しはなくてもよい）
var reDayHead = regexp.MustCompile(`^R(\d{1,2})\.(\d{1,2})\.(\d{1,2})\s*[（(]\s*([日月火水木金土])?\s*(?:祝)?\s*[）)]\s*(\d{1,2}[:：]\d{2})?\s*(.*)$`)

type dayEntry struct {
	date    time.Time
	time    string   // 16:00
	heading string   // 【見出し】（1行目の日付・時刻のあとの全部）
	body    []string // 2行目以降
	wdFixed string   // 曜日を直したとき：元の曜日
}

func (e *dayEntry) head() string {
	h := warekiDateW(e.date)
	if e.time != "" {
		h += e.time
	}
	return h + e.heading
}

// listText: 一覧の1件としての文字（段落内の改行は \n）
func (e *dayEntry) listText() string {
	return strings.Join(append([]string{e.head()}, e.body...), "\n")
}

// recordText: 記録の本文の枠（A4）に入れる文字。1行目は「16:00　【見出し】」
func (e *dayEntry) recordFirst() string {
	f := strings.TrimSpace(e.heading)
	if e.time != "" {
		if f != "" {
			f = e.time + "　" + f
		} else {
			f = e.time
		}
	}
	return f
}

func parseDayDocx(data []byte) (*dayEntry, error) {
	lines, err := docxParaTexts(data)
	if err != nil {
		return nil, err
	}
	var all []string
	for _, l := range lines {
		all = append(all, strings.Split(strings.ReplaceAll(l, "\r", ""), "\n")...)
	}
	// 前後の空行は除く（中の空行はそのまま）
	for len(all) > 0 && strings.TrimSpace(all[0]) == "" {
		all = all[1:]
	}
	for len(all) > 0 && strings.TrimSpace(all[len(all)-1]) == "" {
		all = all[:len(all)-1]
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("Wordが空です")
	}
	first := strings.TrimSpace(toHalfDigits(all[0]))
	m := reDayHead.FindStringSubmatch(first)
	if m == nil {
		return nil, fmt.Errorf("1行目が「R8.9.27（日）16:00【見出し】」の形になっていません（1行目：%s）", truncRunes(all[0], 40))
	}
	d, ok := mkDate(atoi(m[1])+2018, atoi(m[2]), atoi(m[3]))
	if !ok {
		return nil, fmt.Errorf("1行目の日付（R%s.%s.%s）がありえない日付です", m[1], m[2], m[3])
	}
	e := &dayEntry{date: d, time: strings.Replace(m[5], "：", ":", 1), heading: strings.TrimSpace(m[6])}
	if m[4] != "" && m[4] != wdays[d.Weekday()] {
		e.wdFixed = m[4]
	}
	for _, l := range all[1:] {
		e.body = append(e.body, strings.TrimRight(l, " \t　"))
	}
	if len(e.body) == 0 {
		return nil, fmt.Errorf("2行目から下（本文）がありません")
	}
	return e, nil
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// splitRecordPages: 記録の枠に収まるように本文を分ける（1ページ目の1行目は見出し、2ページ目からは「見出し（続き）」）
func splitRecordPages(first string, body []string) [][]string {
	lineCost := func(l string) int {
		n := int((runeWidth(l) + keikaLineChars - 0.01) / keikaLineChars)
		if n < 1 {
			n = 1
		}
		return n
	}
	var pages [][]string
	cur := []string{first}
	used := lineCost(first)
	cont := strings.TrimSpace(first)
	if cont == "" {
		cont = "（続き）"
	} else {
		cont += "（続き）"
	}
	for _, l := range body {
		c := lineCost(l)
		if used+c > keikaMaxLines && len(cur) > 1 {
			pages = append(pages, cur)
			cur = []string{cont}
			used = lineCost(cont)
		}
		// 1行が1ページに入らないほど長いときは、字で分ける
		for c > keikaMaxLines-used && used < keikaMaxLines {
			rs := []rune(l)
			take, w := 0, 0.0
			limit := float64(keikaMaxLines-used) * keikaLineChars
			for take < len(rs) && w+runeWidth(string(rs[take])) <= limit {
				w += runeWidth(string(rs[take]))
				take++
			}
			if take == 0 {
				break
			}
			cur = append(cur, string(rs[:take]))
			pages = append(pages, cur)
			cur = []string{cont}
			used = lineCost(cont)
			l = string(rs[take:])
			c = lineCost(l)
		}
		cur = append(cur, l)
		used += c
	}
	pages = append(pages, cur)
	return pages
}

// ---------------- 記録（Excel）を読む ----------------

func normCmp(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '　' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
}

// recordHas: 記録のどれかのシートの本文（A4）が同じか
func recordHas(p *ooPkg, a4 string) (string, bool) {
	sheets, err := wbSheets(p)
	if err != nil {
		return "", false
	}
	ss := sharedStrings(p)
	want := normCmp(a4)
	for _, sh := range sheets {
		x, ok := p.get(sh.target)
		if !ok {
			continue
		}
		if normCmp(cellText(x, "A4", ss)) == want {
			return sh.name, true
		}
	}
	return "", false
}

// holidays: 記録の「祝日表」シートのA列の日付
func holidays(p *ooPkg) map[string]bool {
	out := map[string]bool{}
	sheets, err := wbSheets(p)
	if err != nil {
		return out
	}
	for _, sh := range sheets {
		if sh.name != "祝日表" {
			continue
		}
		x, _ := p.get(sh.target)
		for _, m := range regexp.MustCompile(`<c r="A\d+"[^>]*><v>(\d+)(?:\.0+)?</v>`).FindAllStringSubmatch(x, -1) {
			n := atoi(m[1])
			t := time.Date(1899, 12, 30, 0, 0, 0, 0, time.Local).AddDate(0, 0, n)
			out[t.Format("2006-01-02")] = true
		}
	}
	return out
}

// chooseSheetNames: 記録に足すシートの名前（1枚目と「続き」）。どれも今のシートと重ならない組を選ぶ。
// 同じ日の n 件目の仕事なら「（R8.9.27②）」を先に試し、ふさがっていれば（R8.9.27）（R8.9.27②）（R8.9.27③）…の順に空いているものを使う
func chooseSheetNames(p *ooPkg, d time.Time, seq, pages int) []string {
	sheets, _ := wbSheets(p)
	used := map[string]bool{}
	for _, sh := range sheets {
		used[sh.name] = true
	}
	names := func(inner string) []string {
		var out []string
		for i := 0; i < pages; i++ {
			switch i {
			case 0:
				out = append(out, "（"+inner+"）")
			case 1:
				out = append(out, "（"+inner+"続き）")
			default:
				out = append(out, fmt.Sprintf("（%s続き%d）", inner, i))
			}
		}
		return out
	}
	free := func(ns []string) bool {
		for _, n := range ns {
			if used[n] {
				return false
			}
		}
		return true
	}
	w := warekiDate(d)
	var cands []string
	if seq > 1 {
		cands = append(cands, w+keikaSeqMark(seq))
	}
	cands = append(cands, w)
	for n := 2; n <= 20; n++ {
		cands = append(cands, w+keikaSeqMark(n))
	}
	for _, c := range cands {
		if ns := names(c); free(ns) {
			return ns
		}
	}
	for i := 21; ; i++ {
		if ns := names(fmt.Sprintf("%s_%d", w, i)); free(ns) {
			return ns
		}
	}
}

func uniqueSheetName(p *ooPkg, base string) string {
	sheets, _ := wbSheets(p)
	used := map[string]bool{}
	for _, s := range sheets {
		used[s.name] = true
	}
	if !used[base] {
		return base
	}
	circ := []rune("②③④⑤⑥⑦⑧⑨⑩")
	inner := strings.TrimSuffix(strings.TrimPrefix(base, "（"), "）")
	for _, c := range circ {
		n := "（" + inner + string(c) + "）"
		if !used[n] {
			return n
		}
	}
	for i := 11; ; i++ {
		n := fmt.Sprintf("（%s_%d）", inner, i)
		if !used[n] {
			return n
		}
	}
}

// ---------------- 書き足しの計画 ----------------

type keikaPlan struct {
	ID         string   `json:"id"`
	User       string   `json:"user"`       // 利用者001
	UserLabel  string   `json:"userLabel"`  // 大和 一郎（利用者001）
	Folder     string   `json:"folder"`     // 支援経過＼大和一郎
	Source     string   `json:"source"`     // 2_完成 のファイル名
	Head       string   `json:"head"`       // 一覧に足す1件の頭の行
	BodyFirst  []string `json:"bodyFirst"`  // 本文の最初の数行
	BodyLines  int      `json:"bodyLines"`  // 本文の行数
	Sheets     []string `json:"sheets"`     // 記録に足すシート
	RecordNew  bool     `json:"recordNew"`  // 記録を様式から作る
	ListNew    bool     `json:"listNew"`    // 一覧を様式から作る
	RecordDone bool     `json:"recordDone"` // 記録にはもう入っている
	ListDone   bool     `json:"listDone"`   // 一覧にはもう入っている
	RecordAt   string   `json:"recordAt,omitempty"`
	Split      bool     `json:"split"`
	Warnings   []string `json:"warnings"`
	Blocked    string   `json:"blocked,omitempty"`
	Files      []string `json:"files"` // 書き足すファイル（表示用）
	created    time.Time
	srcPath    string
	srcHash    string
	dir        string
	name       string
	entry      *dayEntry
	recSig     string
	lstSig     string
}

func (s *panelServer) keikaSrc(folder, name string) (string, string, error) {
	if !safeName(name) || !safeName(folder) || !strings.EqualFold(filepath.Ext(name), ".docx") {
		return "", "", fmt.Errorf("名前が正しくありません")
	}
	jobs, _ := s.doneJobs(s.codeBook())
	for _, j := range jobs {
		if j.Folder != folder {
			continue
		}
		for _, f := range j.Files {
			if f.Name == name {
				m := reFolderUser.FindStringSubmatch(folder)
				if m == nil {
					return "", "", fmt.Errorf("この仕事の利用者が分かりません")
				}
				return filepath.Join(s.p.out, name), m[1], nil
			}
		}
	}
	return "", "", fmt.Errorf("⑥完成した書類 に、この仕事の書類として見つかりません")
}

func (s *panelServer) buildKeikaPlan(folder, name string) (*keikaPlan, error) {
	src, uid, err := s.keikaSrc(folder, name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("読めませんでした：%s", name)
	}
	e, err := parseDayDocx(data)
	if err != nil {
		return nil, fmt.Errorf("この書類は、支援経過（1日分）の決まった形ではありません。%v", err)
	}
	uname, dir, err := s.keikaUser(uid)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	pl := &keikaPlan{ID: newPlanID(), User: uid, UserLabel: uname + "（" + uid + "）", Folder: keikaDirName + "＼" + filepath.Base(dir),
		Source: name, Head: e.head(), BodyLines: len(e.body), Warnings: []string{}, Files: []string{}, BodyFirst: []string{}, Sheets: []string{}, created: time.Now(), srcPath: src,
		srcHash: hex.EncodeToString(sum[:]), dir: dir, name: uname, entry: e}
	for i, l := range e.body {
		if i >= 6 {
			break
		}
		pl.BodyFirst = append(pl.BodyFirst, l)
	}
	if e.wdFixed != "" {
		pl.Warnings = append(pl.Warnings, "1行目の曜日（"+e.wdFixed+"）が日付と合わないので、日付から出した曜日（"+wdays[e.date.Weekday()]+"）にして書き足します")
	}
	rec := filepath.Join(dir, keikaRecordName)
	lst := filepath.Join(dir, keikaListName)
	pl.recSig, pl.lstSig = fileSig(rec), fileSig(lst)
	// 記録
	var rp *ooPkg
	if b, err := os.ReadFile(rec); err == nil {
		if rp, err = readPkg(b); err != nil {
			return nil, fmt.Errorf("%s をExcelとして開けません", keikaRecordName)
		}
	} else if os.IsNotExist(err) {
		pl.RecordNew = true
		rp, _ = readPkg(templateKeikaRecord)
	} else {
		return nil, fmt.Errorf("%s を読めません（Excelで開いていたら閉じてください）", keikaRecordName)
	}
	pages := splitRecordPages(e.recordFirst(), e.body)
	if at, ok := recordHas(rp, strings.Join(pages[0], "\n")); ok {
		pl.RecordDone, pl.RecordAt = true, at
	}
	seq := 1
	if strings.Contains(folder, keikaDocKey+warekiDate(e.date)) {
		seq = keikaSeqOf(folder) // 同じ日の2件目（…R8.9.27②）なら、シート名も「（R8.9.27②）」にする（改善5）
	}
	pl.Sheets = chooseSheetNames(rp, e.date, seq, len(pages))
	if len(pages) > 1 {
		pl.Split = true
		pl.Warnings = append(pl.Warnings, fmt.Sprintf("本文が長く、記録の枠（A4〜T39）に収まらない見込みです（本文 %d 行・約 %d 字）。記録は %d 枚のシート（%s）に分けます。一覧には1件として足します", len(e.body), utf8.RuneCountInString(strings.Join(e.body, "")), len(pages), strings.Join(pl.Sheets, "・")))
	}
	// 一覧
	if b, err := os.ReadFile(lst); err == nil {
		lp, err := readPkg(b)
		if err != nil {
			return nil, fmt.Errorf("%s をWordとして開けません", keikaListName)
		}
		doc, _ := lp.get("word/document.xml")
		_, _, paras, err := bodyParas(doc)
		if err != nil {
			return nil, err
		}
		want := normCmp(toHalfDigits(e.listText()))
		for _, pa := range paras {
			if normCmp(toHalfDigits(pa.text)) == want {
				pl.ListDone = true
			}
		}
	} else if os.IsNotExist(err) {
		pl.ListNew = true
	} else {
		return nil, fmt.Errorf("%s を読めません（Wordで開いていたら閉じてください）", keikaListName)
	}
	if pl.RecordDone && pl.ListDone {
		pl.Blocked = "この1日分（" + e.head() + "）は、すでに記録（シート「" + pl.RecordAt + "」）にも一覧にも書き足してあります。2回目は書き足しません"
	}
	if pl.RecordNew {
		pl.Warnings = append(pl.Warnings, "この方の "+keikaRecordName+" がまだないので、様式（原紙①・原紙②・祝日表）から新しく作ります")
	}
	if pl.ListNew {
		pl.Warnings = append(pl.Warnings, "この方の "+keikaListName+" がまだないので、様式（案1）から新しく作ります")
	}
	if pl.RecordDone && !pl.ListDone {
		pl.Warnings = append(pl.Warnings, "記録にはすでに入っています（シート「"+pl.RecordAt+"」）。一覧にだけ書き足します")
	}
	if pl.ListDone && !pl.RecordDone {
		pl.Warnings = append(pl.Warnings, "一覧にはすでに入っています。記録にだけ書き足します")
	}
	if !pl.RecordDone {
		pl.Files = append(pl.Files, pl.Folder+"＼"+keikaRecordName)
	}
	if !pl.ListDone {
		pl.Files = append(pl.Files, pl.Folder+"＼"+keikaListName)
	}
	return pl, nil
}

// buildRecord: 記録に新しいシートを足した中身
func buildRecord(base []byte, pl *keikaPlan) ([]byte, error) {
	p, err := readPkg(base)
	if err != nil {
		return nil, err
	}
	e := pl.entry
	pages := splitRecordPages(e.recordFirst(), e.body)
	if len(pages) != len(pl.Sheets) {
		return nil, fmt.Errorf("記録のシートの数が合いません")
	}
	hol := holidays(p)
	wd := wdays[e.date.Weekday()]
	if hol[e.date.Format("2006-01-02")] {
		wd += "祝"
	}
	var adds []newSheet
	for i, pg := range pages {
		adds = append(adds, newSheet{
			name: pl.Sheets[i],
			cells: map[string][2]string{
				"D2": {"inlineStr", inlineStrXML(strings.Replace(pl.name, " ", "　", 1))},
				"L2": {"", "<v>" + strconv.Itoa(e.date.Year()-2018) + "</v>"},
				"N2": {"", "<v>" + strconv.Itoa(int(e.date.Month())) + "</v>"},
				"P2": {"", "<v>" + strconv.Itoa(e.date.Day()) + "</v>"},
				"A4": {"inlineStr", inlineStrXML(strings.Join(pg, "\n"))},
			},
			cache: map[string]string{"S2": wd},
		})
	}
	if err := addSheetsFromTemplate(p, keikaTemplate, adds); err != nil {
		return nil, err
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

// buildList: 一覧の一番上（最初の件の上）に1件足した中身
func buildList(base []byte, pl *keikaPlan, fresh bool) ([]byte, error) {
	p, err := readPkg(base)
	if err != nil {
		return nil, err
	}
	doc, ok := p.get("word/document.xml")
	if !ok {
		return nil, fmt.Errorf("Wordの本文が見つかりません")
	}
	styles, hasStyles := p.get("word/styles.xml")
	sid := keikaStyleID
	if hasStyles {
		styles, sid = ensureKeikaStyle(styles)
		p.set("word/styles.xml", styles)
	}
	np := entryParaXML(sid, pl.entry.head(), pl.entry.body)
	bodyIn, _, paras, err := bodyParas(doc)
	if err != nil {
		return nil, err
	}
	allEmpty := true
	for _, pa := range paras {
		if strings.TrimSpace(pa.text) != "" {
			allEmpty = false
		}
	}
	switch {
	case allEmpty && len(paras) > 0 && fresh:
		// 様式から作ったばかりの一覧：空の段落の代わりに入れる
		doc = doc[:paras[0].start] + np + doc[paras[0].end:]
	default:
		// 今までどおり、一番上の件（年入りの日付で始まる最初の段落）の上に足す。年入りの件がまだない一覧は本文の一番上に。
		// ただし同じ日の件がすでに一番上にあり、その時刻が足す件より新しいときは、その下に入れる（同じ日は時刻の新しいものが上。改善5）
		at := bodyIn
		nk := entryKeyOf(pl.entry.date, pl.entry.time)
		for i, pa := range paras {
			k, ok := paraEntryKey(pa.text)
			if !ok {
				continue
			}
			if k.day.Equal(nk.day) && k.newerThan(nk) {
				at = paras[len(paras)-1].end // この日のより新しい件の下（下に件がなければ一番下）
				for _, pb := range paras[i+1:] {
					if _, ok2 := paraEntryKey(pb.text); ok2 {
						at = pb.start
						break
					}
				}
				continue
			}
			at = pa.start
			break
		}
		doc = doc[:at] + np + doc[at:]
	}
	p.set("word/document.xml", doc)
	if fresh {
		if h, ok := p.get("word/header2.xml"); ok && strings.Contains(h, ">〇〇<") {
			p.set("word/header2.xml", strings.Replace(h, ">〇〇<", ">"+encodeText(strings.Replace(pl.name, " ", "　", 1))+"<", 1))
		}
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

// 一覧の件の頭（年入り）：R8.9.27（日）
var reEntryHead = regexp.MustCompile(`^R(\d{1,2})\.(\d{1,2})\.(\d{1,2})\s*[（(]`)

// 一覧の件の並び順の鍵（日付と時刻。時刻がないときは、その日の一番古いものとみなす）
type entryKey struct {
	day  time.Time
	mins int
}

func entryKeyOf(d time.Time, hm string) entryKey {
	k := entryKey{day: d, mins: -1}
	if m := reHM.FindStringSubmatch(hm); m != nil {
		k.mins = atoi(m[1])*60 + atoi(m[2])
	}
	return k
}

func (a entryKey) newerThan(b entryKey) bool {
	if !a.day.Equal(b.day) {
		return a.day.After(b.day)
	}
	return a.mins > b.mins
}

var (
	reHM         = regexp.MustCompile(`^(\d{1,2})[:：](\d{2})`)
	reEntryHeadT = regexp.MustCompile(`^R(\d{1,2})\.(\d{1,2})\.(\d{1,2})\s*[（(][^）)]*[）)]\s*(\d{1,2}[:：]\d{2})?`)
)

// paraEntryKey: 一覧の段落の頭（R8.9.27（日）16:00…）から並び順の鍵
func paraEntryKey(text string) (entryKey, bool) {
	m := reEntryHeadT.FindStringSubmatch(toHalfDigits(strings.TrimSpace(text)))
	if m == nil {
		return entryKey{}, false
	}
	d, ok := mkDate(atoi(m[1])+2018, atoi(m[2]), atoi(m[3]))
	if !ok {
		return entryKey{}, false
	}
	return entryKeyOf(d, m[4]), true
}

// ---------------- API：書き足し ----------------

func (s *panelServer) keikaPreview(r *http.Request) (any, error) {
	var q struct{ Folder, Name string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	pl, err := s.buildKeikaPlan(q.Folder, q.Name)
	if err != nil {
		return nil, err
	}
	if s.keikaPlans == nil {
		s.keikaPlans = map[string]*keikaPlan{}
	}
	for k, v := range s.keikaPlans {
		if time.Since(v.created) > planLifetime {
			delete(s.keikaPlans, k)
		}
	}
	s.keikaPlans[pl.ID] = pl
	return pl, nil
}

func (s *panelServer) keikaAppend(r *http.Request) (any, error) {
	var q struct{ ID string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	pl := s.keikaPlans[q.ID]
	if pl == nil || time.Since(pl.created) > planLifetime {
		return nil, fmt.Errorf("確認の画面が古くなりました。もう一度「書き足す」の画面を開いてください（まだ何も書いていません）")
	}
	delete(s.keikaPlans, q.ID) // 1回だけ使える
	if pl.Blocked != "" {
		return nil, fmt.Errorf("%s", pl.Blocked)
	}
	// 確認の画面のあとで、元の書類・記録・一覧が変わっていないか
	data, err := os.ReadFile(pl.srcPath)
	sum := sha256.Sum256(data)
	if err != nil || hex.EncodeToString(sum[:]) != pl.srcHash {
		return nil, fmt.Errorf("確認の画面のあとで、元の書類（%s）が変わりました。もう一度開き直してください（まだ何も書いていません）", pl.Source)
	}
	rec := filepath.Join(pl.dir, keikaRecordName)
	lst := filepath.Join(pl.dir, keikaListName)
	if fileSig(rec) != pl.recSig || fileSig(lst) != pl.lstSig {
		return nil, fmt.Errorf("確認の画面のあとで、記録または一覧が変わりました。もう一度開き直してください（まだ何も書いていません）")
	}
	if err := s.keikaGuard(rec); err != nil {
		return nil, err
	}
	if !pl.RecordDone && officeOpen(rec) {
		return nil, fmt.Errorf("%s がExcelで開かれています。閉じてから、もう一度押してください（まだ何も書いていません）", keikaRecordName)
	}
	if !pl.ListDone && officeOpen(lst) {
		return nil, fmt.Errorf("%s がWordで開かれています。閉じてから、もう一度押してください（まだ何も書いていません）", keikaListName)
	}
	// 中身を先に作って確かめる（どちらかが作れなければ、どちらも書かない）
	var recData, lstData []byte
	if !pl.RecordDone {
		base := templateKeikaRecord
		if !pl.RecordNew {
			if base, err = os.ReadFile(rec); err != nil {
				return nil, err
			}
		}
		if recData, err = buildRecord(base, pl); err != nil {
			return nil, fmt.Errorf("記録に書き足す中身を作れませんでした（まだ何も書いていません）：%v", err)
		}
	}
	if !pl.ListDone {
		base := templateKeikaList
		if !pl.ListNew {
			if base, err = os.ReadFile(lst); err != nil {
				return nil, err
			}
		}
		if lstData, err = buildList(base, pl, pl.ListNew); err != nil {
			return nil, fmt.Errorf("一覧に書き足す中身を作れませんでした（まだ何も書いていません）：%v", err)
		}
	}
	if err := os.MkdirAll(pl.dir, 0700); err != nil {
		return nil, err
	}
	// 控え
	stamp := time.Now().Format("20060102_150405")
	bdir := filepath.Join(pl.dir, keikaBackupDir)
	var backups []string
	for _, f := range []struct {
		path string
		do   bool
	}{{rec, recData != nil && !pl.RecordNew}, {lst, lstData != nil && !pl.ListNew}} {
		if !f.do {
			continue
		}
		if err := os.MkdirAll(bdir, 0700); err != nil {
			return nil, err
		}
		b, err := os.ReadFile(f.path)
		if err != nil {
			return nil, fmt.Errorf("控えを取れませんでした（まだ何も書いていません）：%v", err)
		}
		bp := uniquePath(bdir, stamp+"_"+filepath.Base(f.path))
		if err := writeNew(bp, b); err != nil {
			return nil, fmt.Errorf("控えを取れませんでした（まだ何も書いていません）：%v", err)
		}
		backups = append(backups, keikaBackupDir+"＼"+filepath.Base(bp))
	}
	var done []string
	if recData != nil {
		if pl.RecordNew {
			err = writeNew(rec, recData)
		} else {
			err = replaceFile(rec, recData)
		}
		if err != nil {
			return nil, fmt.Errorf("記録に書き足せませんでした（Excelで開いていたら閉じてください）。何も書いていません：%v", err)
		}
		done = append(done, keikaRecordName+" にシート "+strings.Join(pl.Sheets, "・")+" を足しました")
	}
	if lstData != nil {
		if pl.ListNew {
			err = writeNew(lst, lstData)
		} else {
			err = replaceFile(lst, lstData)
		}
		if err != nil {
			msg := "一覧に書き足せませんでした（Wordで開いていたら閉じて、もう一度「書き足す」を押してください。記録には入っているので、次は一覧にだけ足します）：" + err.Error()
			if len(done) > 0 {
				msg = done[0] + "。" + msg
			}
			return nil, fmt.Errorf("%s", msg)
		}
		done = append(done, keikaListName+" の一番上に「"+pl.Head+"」を足しました")
	}
	return map[string]any{"ok": strings.Join(done, "。") + "。", "backups": backups, "folder": pl.Folder, "user": pl.User}, nil
}

// ---------------- API：今日の支援経過（文字起こし → Word → 1_元データ） ----------------

func (s *panelServer) keikaToday(r *http.Request) (any, error) {
	var q struct{ User, Date, Text string }
	if err := decodeLimited(r, &q, 8<<20); err != nil {
		return nil, fmt.Errorf("貼り付けた文が長すぎるか、受け取れませんでした（目安：20万字まで）")
	}
	d, ok := parseISODate(q.Date)
	if !ok {
		return nil, fmt.Errorf("日付を選んでください")
	}
	if d.After(time.Now().AddDate(0, 0, 1)) {
		return nil, fmt.Errorf("先の日付は選べません")
	}
	uname, _, err := s.keikaUser(q.User)
	if err != nil {
		return nil, err
	}
	text := normPaste(xmlSafe(q.Text))
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("貼り付け欄が空です。文字起こしの文を貼り付けてください")
	}
	if n := utf8.RuneCountInString(text); n > maxPasteRunes {
		return nil, fmt.Errorf("長すぎます（%d字）。20万字までにしてください", n)
	}
	sum := sha256.Sum256([]byte(text))
	hash := hex.EncodeToString(sum[:])
	if where := s.pasteDup(hash); where != "" {
		return nil, fmt.Errorf("同じ内容の文字起こしを、すでに入れています（%s）。2回目は入れませんでした", where)
	}
	wd := warekiDateW(d)
	full := "書類：" + keikaDocLabel + "\n利用者：" + uname + "\n日付：" + wd + "\n\n" + text
	data, err := pasteDocx(keikaDocLabel+" "+warekiDate(d), full)
	if err != nil {
		return nil, err
	}
	name := "文字起こし_" + keikaDocKey + "_" + warekiDate(d) + "_" + time.Now().Format("20060102_1504") + ".docx"
	dst := uniquePath(s.p.src, name)
	if err := writeNew(dst, data); err != nil {
		return nil, fmt.Errorf("1_元データ に保存できませんでした: %v", err)
	}
	if lf, err := os.OpenFile(s.pasteLedger(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
		fmt.Fprintf(lf, "%s\t%s\r\n", hash, filepath.Base(dst))
		lf.Close()
	}
	return map[string]any{"ok": "「" + filepath.Base(dst) + "」（Word）として 1_元データ に入れました（" + strconv.Itoa(utf8.RuneCountInString(text)) + "字）。このあと「2 置き換える」→「3 AIに渡す」（書類は「" + keikaDocLabel + "」）へ進んでください",
		"name": filepath.Base(dst), "user": q.User, "date": d.Format("2006-01-02")}, nil
}

// ---------------- API：一覧から切り出す ----------------

type listEntry struct {
	date time.Time
	text string
}

// readListEntries: 一覧の段落を、年入りの日付で始まる件と、日付を読めない段落に分ける
func readListEntries(data []byte) ([]listEntry, []string, error) {
	texts, err := docxParaTexts(data)
	if err != nil {
		return nil, nil, err
	}
	ents, bad := []listEntry{}, []string{}
	for _, t := range texts {
		tt := strings.TrimSpace(t)
		if tt == "" {
			continue
		}
		m := reEntryHead.FindStringSubmatch(toHalfDigits(tt))
		if m == nil {
			bad = append(bad, truncRunes(strings.ReplaceAll(tt, "\n", " "), 40))
			continue
		}
		d, ok := mkDate(atoi(m[1])+2018, atoi(m[2]), atoi(m[3]))
		if !ok {
			bad = append(bad, truncRunes(strings.ReplaceAll(tt, "\n", " "), 40))
			continue
		}
		ents = append(ents, listEntry{d, strings.TrimRight(t, "\n")})
	}
	return ents, bad, nil
}

func (s *panelServer) keikaExtract(r *http.Request) (any, error) {
	var q struct{ User, Period, From, To string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	_, dir, err := s.keikaUser(q.User)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	var from, to time.Time
	switch q.Period {
	case "1", "3", "6":
		n := atoi(q.Period)
		from, to = today.AddDate(0, -n, 0), today
	case "custom":
		var ok1, ok2 bool
		from, ok1 = parseISODate(q.From)
		to, ok2 = parseISODate(q.To)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("期間の始めと終わりの日付を選んでください")
		}
		if to.Before(from) {
			return nil, fmt.Errorf("期間の終わりが始めより前になっています")
		}
	default:
		return nil, fmt.Errorf("期間を選んでください")
	}
	lst := filepath.Join(dir, keikaListName)
	data, err := os.ReadFile(lst)
	if err != nil {
		return nil, fmt.Errorf("この方の %s が見つかりません（場所：マスキング作業＼%s＼%s）", keikaListName, keikaDirName, filepath.Base(dir))
	}
	ents, bad, err := readListEntries(data)
	if err != nil {
		return nil, err
	}
	var pick []listEntry
	for _, e := range ents {
		if !e.date.Before(from) && !e.date.After(to) {
			pick = append(pick, e)
		}
	}
	sort.SliceStable(pick, func(a, b int) bool { return pick[a].date.After(pick[b].date) })
	span := warekiDate(from) + "〜" + warekiDate(to)
	res := map[string]any{"count": len(pick), "bad": bad, "span": span, "total": len(ents)}
	if len(pick) == 0 {
		res["ok"] = "期間（" + span + "）の件は 0 件でした。Wordは作っていません"
		res["name"] = ""
		return res, nil
	}
	var paras []string
	for _, e := range pick {
		paras = append(paras, e.text)
	}
	out, err := plainDocx("支援経過（"+span+"）", paras)
	if err != nil {
		return nil, err
	}
	name := "支援経過一覧_切り出し_" + warekiDate(from) + "-" + warekiDate(to) + "_" + now.Format("20060102_1504") + ".docx"
	dst := uniquePath(s.p.src, name)
	if err := writeNew(dst, out); err != nil {
		return nil, fmt.Errorf("1_元データ に保存できませんでした: %v", err)
	}
	res["name"] = filepath.Base(dst)
	res["ok"] = "期間（" + span + "）の " + strconv.Itoa(len(pick)) + " 件を、新しい順に「" + filepath.Base(dst) + "」（Word）にして 1_元データ に入れました。このあと「2 置き換える」を押してください"
	return res, nil
}

// ---------------- API：支援経過のフォルダ（写し直し用） ----------------

func (s *panelServer) keikaInfo(r *http.Request) (any, error) {
	var q struct{ User string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	users, err := s.userList()
	if err != nil {
		return nil, err
	}
	res := map[string]any{"users": users}
	if q.User == "" {
		return res, nil
	}
	_, dir, err := s.keikaUser(q.User)
	if err != nil {
		return nil, err
	}
	files := entriesIn(dir)
	docs := []string{}
	for _, f := range files {
		if strings.EqualFold(filepath.Ext(f.Name), ".docx") {
			docs = append(docs, f.Name)
		}
	}
	nb := len(entriesIn(filepath.Join(dir, keikaBackupDir)))
	res["folder"] = keikaDirName + "＼" + filepath.Base(dir)
	res["files"] = files
	res["docs"] = docs
	res["backups"] = nb
	return res, nil
}

// keikaUpload: 今までの一覧（Word）などを、その方の支援経過フォルダに入れる（同じ名前があれば別の名前にする。上書きしない）
func (s *panelServer) keikaUpload(r *http.Request) (any, error) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, fmt.Errorf("ファイルを受け取れませんでした: %v", err)
	}
	_, dir, err := s.keikaUser(r.FormValue("user"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	saved, rejected := []string{}, []string{}
	for _, fh := range r.MultipartForm.File["files"] {
		name := filepath.Base(strings.ReplaceAll(fh.Filename, "\\", "/"))
		ext := strings.ToLower(filepath.Ext(name))
		if !safeName(name) || (ext != ".docx" && ext != ".xlsx") {
			rejected = append(rejected, name)
			continue
		}
		f, err := fh.Open()
		if err != nil {
			rejected = append(rejected, name)
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, 32<<20))
		f.Close()
		if err != nil {
			rejected = append(rejected, name)
			continue
		}
		dst := uniquePath(dir, name)
		if err := writeNew(dst, data); err != nil {
			rejected = append(rejected, name)
			continue
		}
		saved = append(saved, filepath.Base(dst))
	}
	return map[string]any{"saved": saved, "rejected": rejected}, nil
}

// ---------------- API：今までの一覧を写し直す（年を補う） ----------------

type oldHead struct {
	year, month, day int
	circ             string
	heading          string
	n                int // 置き換える頭の字数
	monthOnly        bool
	hasNew           bool // すでに年入りの形
}

var (
	reOldHead  = regexp.MustCompile(`^(?:R\s*(\d{1,2})\s*)?[（(]\s*(\d{1,2})\s*[.．・/／]\s*(\d{1,2})\s*([①-⑳]*)`)
	reOldMonth = regexp.MustCompile(`^(?:R\s*(\d{1,2})\s*)?[（(]\s*(\d{1,2})\s*月`)
)

// parseOldHead: 「R8（1.13モニタ時）本文」「（4.27①　専門職相談）本文」「（10.4）本文」
func parseOldHead(text string) (oldHead, bool) {
	t := toHalfDigits(text)
	if reEntryHead.MatchString(strings.TrimSpace(t)) {
		m := reEntryHead.FindStringSubmatch(strings.TrimSpace(t))
		return oldHead{year: atoi(m[1]), month: atoi(m[2]), day: atoi(m[3]), hasNew: true}, true
	}
	lead := len(t) - len(strings.TrimLeft(t, " 　"))
	t2 := t[lead:]
	m := reOldHead.FindStringSubmatchIndex(t2)
	h := oldHead{}
	if m == nil {
		mm := reOldMonth.FindStringSubmatch(t2)
		if mm == nil {
			return h, false
		}
		h.monthOnly = true
		h.year, h.month = atoi(mm[1]), atoi(mm[2])
		return h, true
	}
	if m[2] >= 0 {
		h.year = atoi(t2[m[2]:m[3]])
	}
	h.month, h.day = atoi(t2[m[4]:m[5]]), atoi(t2[m[6]:m[7]])
	h.circ = t2[m[8]:m[9]]
	// 見出し：開いたかっこに対応する閉じかっこまで
	rest := t2[m[1]:]
	depth := 1
	end := -1
	for i, r := range rest {
		switch r {
		case '（', '(':
			depth++
		case '）', ')':
			depth--
		}
		if depth == 0 {
			end = i
			break
		}
	}
	if end < 0 {
		return h, false
	}
	h.heading = strings.TrimSpace(strings.Trim(rest[:end], " 　"))
	// toHalfDigits は1字を1字に置き換えるだけなので、字数は元の文と同じ
	h.n = utf8.RuneCountInString(t[:lead]) + utf8.RuneCountInString(t2[:m[1]]) + utf8.RuneCountInString(rest[:end]) + 1
	return h, true
}

type rewriteRow struct {
	No     int    `json:"no"`
	Before string `json:"before"`
	After  string `json:"after"`
	Note   string `json:"note,omitempty"`
}

func (s *panelServer) keikaRewrite(r *http.Request) (any, error) {
	var q struct{ User, Name string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	_, dir, err := s.keikaUser(q.User)
	if err != nil {
		return nil, err
	}
	if !safeName(q.Name) || !strings.EqualFold(filepath.Ext(q.Name), ".docx") {
		return nil, fmt.Errorf("名前が正しくありません")
	}
	src := filepath.Join(dir, q.Name)
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("見つかりません：%s", q.Name)
	}
	p, err := readPkg(data)
	if err != nil {
		return nil, fmt.Errorf("Wordとして開けません：%s", q.Name)
	}
	doc, ok := p.get("word/document.xml")
	if !ok {
		return nil, fmt.Errorf("Wordの本文が見つかりません")
	}
	_, _, paras, err := bodyParas(doc)
	if err != nil {
		return nil, err
	}
	type item struct {
		pi   int
		h    oldHead
		ok   bool
		year int
	}
	var items []item
	for i, pa := range paras {
		if strings.TrimSpace(pa.text) == "" {
			continue
		}
		h, ok := parseOldHead(pa.text)
		items = append(items, item{pi: i, h: h, ok: ok})
	}
	// 年を決める：年の目印（R8（…）がある件を起点に、上（新しい方）と下（古い方）へ、月の並びで年をたどる
	anchor := -1
	for i, it := range items {
		if it.ok && it.h.year > 0 {
			anchor = i
			break
		}
	}
	noMarker := anchor < 0
	if noMarker {
		now := time.Now()
		// 年の目印がない一覧：一番上の件を今年（今の月より後の月なら去年）とみなす
		for i, it := range items {
			if it.ok && it.h.month > 0 {
				y := now.Year() - 2018
				if it.h.month > int(now.Month()) {
					y--
				}
				items[i].year = y
				anchor = i
				break
			}
		}
	} else {
		items[anchor].year = items[anchor].h.year
	}
	var conflicts = map[int]string{}
	if anchor >= 0 {
		// 下へ（古い方）
		y, pm := items[anchor].year, items[anchor].h.month
		for i := anchor + 1; i < len(items); i++ {
			it := &items[i]
			if !it.ok || it.h.month == 0 {
				continue
			}
			if it.h.month > pm {
				y--
			}
			if it.h.year > 0 && it.h.year != y {
				conflicts[i] = fmt.Sprintf("月の並びからは R%d ですが、年の目印は R%d なので目印に合わせました", y, it.h.year)
				y = it.h.year
			}
			it.year, pm = y, it.h.month
		}
		// 上へ（新しい方）
		y, pm = items[anchor].year, items[anchor].h.month
		for i := anchor - 1; i >= 0; i-- {
			it := &items[i]
			if !it.ok || it.h.month == 0 {
				continue
			}
			if it.h.month < pm {
				y++
			}
			if it.h.year > 0 && it.h.year != y {
				conflicts[i] = fmt.Sprintf("月の並びからは R%d ですが、年の目印は R%d なので目印に合わせました", y, it.h.year)
				y = it.h.year
			}
			it.year, pm = y, it.h.month
		}
	}
	styles, hasStyles := p.get("word/styles.xml")
	sid := keikaStyleID
	if hasStyles {
		styles, sid = ensureKeikaStyle(styles)
	}
	supplied, undecided, kept := []rewriteRow{}, []rewriteRow{}, 0
	newDoc := doc
	// 後ろから置き換える（位置がずれないように）
	type rep struct {
		start, end int
		x          string
	}
	var reps []rep
	for i, it := range items {
		pa := paras[it.pi]
		no := i + 1
		before := truncRunes(strings.SplitN(pa.text, "\n", 2)[0], 30)
		switch {
		case !it.ok:
			undecided = append(undecided, rewriteRow{No: no, Before: before, Note: "頭に日付（（月.日 …）の形）がありません。そのまま写しました"})
			reps = append(reps, rep{pa.start, pa.end, setParaStyle(pa.xml, sid)})
			continue
		case it.h.hasNew:
			kept++
			reps = append(reps, rep{pa.start, pa.end, setParaStyle(pa.xml, sid)})
			continue
		case it.h.monthOnly:
			undecided = append(undecided, rewriteRow{No: no, Before: before, Note: fmt.Sprintf("日が分かりません（R%d.%d 月の件）。そのまま写しました。日付を決めて、手で頭を直してください", it.year, it.h.month)})
			reps = append(reps, rep{pa.start, pa.end, setParaStyle(pa.xml, sid)})
			continue
		case it.year <= 0:
			undecided = append(undecided, rewriteRow{No: no, Before: before, Note: "年を決められませんでした。そのまま写しました"})
			reps = append(reps, rep{pa.start, pa.end, setParaStyle(pa.xml, sid)})
			continue
		}
		d, ok := mkDate(it.year+2018, it.h.month, it.h.day)
		if !ok {
			undecided = append(undecided, rewriteRow{No: no, Before: before, Note: fmt.Sprintf("ありえない日付（R%d.%d.%d）です。そのまま写しました", it.year, it.h.month, it.h.day)})
			reps = append(reps, rep{pa.start, pa.end, setParaStyle(pa.xml, sid)})
			continue
		}
		head := warekiDateW(d)
		if it.h.heading != "" || it.h.circ != "" {
			head += "【" + it.h.circ + it.h.heading + "】"
		}
		nx, ok := replaceParaPrefix(pa.xml, it.h.n, head)
		if !ok {
			undecided = append(undecided, rewriteRow{No: no, Before: before, Note: "頭の日付の途中に改行・タブがあるため、書き換えられませんでした。そのまま写しました"})
			reps = append(reps, rep{pa.start, pa.end, setParaStyle(pa.xml, sid)})
			continue
		}
		reps = append(reps, rep{pa.start, pa.end, setParaStyle(nx, sid)})
		row := rewriteRow{No: no, Before: before, After: truncRunes(head, 40)}
		if it.h.year > 0 {
			row.Note = "年の目印（R" + strconv.Itoa(it.h.year) + "）どおり"
		} else if noMarker {
			row.Note = "年の目印がない一覧なので、一番上の件を今年として補いました（要確認）"
		} else {
			row.Note = "月の並びから年を補いました"
		}
		if c, ok := conflicts[i]; ok {
			row.Note = c
		}
		supplied = append(supplied, row)
	}
	sort.Slice(reps, func(a, b int) bool { return reps[a].start > reps[b].start })
	for _, rp := range reps {
		newDoc = newDoc[:rp.start] + rp.x + newDoc[rp.end:]
	}
	p.set("word/document.xml", newDoc)
	if hasStyles {
		p.set("word/styles.xml", styles)
	}
	out, err := p.bytes()
	if err != nil {
		return nil, err
	}
	if err := checkPkgXML(out); err != nil {
		return nil, fmt.Errorf("新しい一覧を作れませんでした：%v", err)
	}
	// 記号（【利用者001】など）が1つも消えていないか
	if a, b := docTokens(doc), docTokens(newDoc); !sameCounts(a, b) {
		return nil, fmt.Errorf("安全のため中止します：写し直すと記号（【利用者001】など）の数が変わります")
	}
	dst := uniquePath(dir, "支援経過一覧（年入り）.docx")
	if err := s.keikaGuard(dst); err != nil {
		return nil, err
	}
	if err := writeNew(dst, out); err != nil {
		return nil, fmt.Errorf("新しい一覧を保存できませんでした：%v", err)
	}
	return map[string]any{"ok": "「" + filepath.Base(dst) + "」を作りました（元の「" + q.Name + "」は書き換えていません）", "name": filepath.Base(dst),
		"supplied": supplied, "undecided": undecided, "kept": kept, "total": len(items), "folder": keikaDirName + "＼" + filepath.Base(dir)}, nil
}

// docTokens: 本文の段落の中の記号（【利用者001】【その他009】など、置き換えの記号の形のもの）の数
func docTokens(doc string) map[string]int {
	out := map[string]int{}
	_, _, paras, _ := bodyParas(doc)
	for _, pa := range paras {
		for _, t := range reTokenAny.FindAllString(pa.text, -1) {
			if reCodeNum.MatchString(t) {
				out[t]++
			}
		}
	}
	return out
}

func sameCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// isJobDoc: 仕事の書類名（決まった4種と、支援経過1日分R8.9.27 の形）
var reKeikaJobDoc = regexp.MustCompile(`^` + keikaDocKey + `R\d{1,2}\.\d{1,2}\.\d{1,2}[②-⑳]?$`)

// 同じ日の2件目から付ける字（改善5）
const keikaSeqMarks = "②③④⑤⑥⑦⑧⑨⑩⑪⑫⑬⑭⑮⑯⑰⑱⑲⑳"

func keikaSeqMark(n int) string {
	if n <= 1 {
		return ""
	}
	rs := []rune(keikaSeqMarks)
	if n-2 >= len(rs) {
		return ""
	}
	return string(rs[n-2])
}

// keikaSeqOf: 仕事の書類名・フォルダ名の「支援経過1日分R8.9.27②」から、何件目か（印がなければ1）
func keikaSeqOf(folder string) int {
	i := strings.Index(folder, keikaDocKey)
	if i < 0 {
		return 1
	}
	rest := folder[i+len(keikaDocKey):]
	for n, r := range []rune(keikaSeqMarks) {
		if strings.ContainsRune(rest, r) {
			return n + 2
		}
	}
	return 1
}

// keikaJobName: 同じ利用者・同じ日の1日分の仕事の名前。すでに使った名前（30・40・50のフォルダ、00_指示の依頼、
// 片づけ用の控えに残る仕事）は使わず、2件目は「②」、3件目は「③」…を付ける
func (s *panelServer) keikaJobName(user string, day time.Time) (string, int, error) {
	base := user + "_" + keikaDocKey + warekiDate(day)
	ledger, _ := os.ReadFile(filepath.Join(s.p.report, cleanLedgerName))
	used := func(job string) bool {
		if _, err := os.Stat(filepath.Join(s.p.aiIn, job)); err == nil {
			return true
		}
		for _, d := range []string{s.p.aiOut, filepath.Join(s.p.ai, consultDirName)} {
			ents, _ := os.ReadDir(d)
			for _, e := range ents {
				if n := e.Name(); strings.HasPrefix(n, job+"（") || strings.HasPrefix(n, job+"(") || n == job {
					return true
				}
			}
		}
		for _, e := range entriesIn(filepath.Join(s.p.ai, shijiDirName)) {
			if strings.HasPrefix(e.Name, "依頼_"+job+"_") {
				return true
			}
		}
		return strings.Contains(string(ledger), "\t"+job+"|")
	}
	for n := 1; n <= 20; n++ {
		job := base + keikaSeqMark(n)
		if !used(job) {
			return job, n, nil
		}
	}
	return "", 0, fmt.Errorf("同じ日の支援経過（1日分）が20件を超えました。片づけてから渡してください")
}

func isJobDoc(d string) bool { return jobDocs[d] || reKeikaJobDoc.MatchString(d) }

// decodeLimited: 大きめの本文（文字起こし）を受け取る
func decodeLimited(r *http.Request, v any, limit int64) error {
	return json.NewDecoder(io.LimitReader(r.Body, limit)).Decode(v)
}

// isDayDocx: 支援経過（1日分）の決まった形（1行目が「R8.9.27（日）…」）のWordか
func isDayDocx(path string) bool {
	return cachedKind(path, "daydocx", func() (bool, bool) { // 改善21：結果は覚えておく（開けたときだけ）
		if !canOpenZip(path) {
			return false, false
		}
		return isDayDocxFull(path), true
	})
}

func isDayDocxFull(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.Size() > 8<<20 {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_, err = parseDayDocx(data)
	return err == nil
}
