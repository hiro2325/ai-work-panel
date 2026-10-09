package main

// 支援経過（Excel）をサーバーの利用者別 .xlsm に書き足す（改善18・R8.10.3 決定）
//
//   - AIから届いた1日分は Excel（新しい原紙の形・シート1つ）。
//   - 「書き足す」：事務所サーバーの利用者フォルダ（サーバーへ保存と同じ場所）の「支援経過記録…xlsm」の「一覧」のすぐ右に、1日分のシートを足す。
//   - 書き足す前に、同じフォルダの「控え」へ日付・時刻付きで元の .xlsm の写しを取る。今あるシートは消さない・変えない。
//   - .xlsm の vbaProject.bin などの部品はバイトのまま残す（XMLだけ直す）。
//   - サーバーでは何も消さない（この .xlsm への書き足しだけは、控えを取ったあとで入れ替える）。
//   - .xlsm が無い・2つ以上ある・Excelで開いている・利用者フォルダが見つからない／2つ以上のときは書かない。
//   - 一覧への書き写しはマクロ（開いて保存したとき）。パネルは一覧を書かない。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	keikaXPrefix   = "支援経過記録"
	keikaXTemplate = "原紙"
	keikaXList     = "一覧"
	keikaXMaxLines = 37
	keikaXPerLine  = 47.0
)

var keikaXCirc = []rune("②③④⑤⑥⑦⑧⑨⑩")

// ---------------- 届いた1日分（Excel）を読む ----------------

type kxDay struct {
	sheet                                     string // 10.3 / 10.3②
	y, m, d                                   int    // 令和の年・月・日（L2・N2・P2）
	date                                      time.Time
	weekday                                   string // S2 の計算結果（日・月…・祝つき）
	code, kubun, jikan, basho, aite, mokuteki string
	body                                      string
}

var reDaySheet = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})([②-⑩]?)$`)

// kxCell: セルの文字（共有文字列・インライン文字列・数値）。空のセル（<c … />）は ""
func kxCell(sheet, ref string, ss []string) string {
	re := regexp.MustCompile(`(?s)<c r="` + ref + `"([^>]*?)(?:/>|>(.*?)</c>)`)
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

// parseDayXlsx: 新しい原紙の形の1日分（シート1つ）か確かめて、中身を読む
func parseDayXlsx(data []byte) (*kxDay, error) {
	p, err := readPkg(data)
	if err != nil {
		return nil, fmt.Errorf("Excelとして開けません")
	}
	sheets, err := wbSheets(p)
	if err != nil || len(sheets) != 1 {
		return nil, fmt.Errorf("シートが1つだけの支援経過（1日分）ではありません")
	}
	x, ok := p.get(sheets[0].target)
	if !ok {
		return nil, fmt.Errorf("シートの中身が見つかりません")
	}
	return parseDaySheet(x, sharedStrings(p), sheets[0].name)
}

// parseDaySheet: 1日分のシート（XML）の中身を読む（改善29：まとめるときは、今ある何枚ものシートを1枚ずつ読む）
func parseDaySheet(x string, ss []string, sheetName string) (*kxDay, error) {
	if kxCell(x, "A4", ss) != "区分" || kxCell(x, "A6", ss) != "目的" || !strings.Contains(x, `<mergeCell ref="A7:T41"/>`) {
		return nil, fmt.Errorf("新しい原紙の形（区分・目的・本文A7）ではありません")
	}
	m := reDaySheet.FindStringSubmatch(toHalfDigits(sheetName))
	if m == nil {
		return nil, fmt.Errorf("シート名が「月.日」の形ではありません（%s）", sheetName)
	}
	e := &kxDay{sheet: toHalfDigits(sheetName)}
	num := func(ref string) int {
		n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(kxCell(x, ref, ss), ".0")))
		return n
	}
	e.y, e.m, e.d = num("L2"), num("N2"), num("P2")
	if e.y == 0 || e.m == 0 || e.d == 0 {
		return nil, fmt.Errorf("日付（年L2・月N2・日P2）が入っていません")
	}
	dt, ok := mkDate(e.y+2018, e.m, e.d)
	if !ok {
		return nil, fmt.Errorf("日付（令和%d年%d月%d日）が正しくありません", e.y, e.m, e.d)
	}
	e.date = dt
	if sm := atoi(m[1]); sm != e.m || atoi(m[2]) != e.d {
		return nil, fmt.Errorf("シート名（%s）と日付欄（%d月%d日）が合いません", e.sheet, e.m, e.d)
	}
	e.weekday = kxCell(x, "S2", ss)
	e.code = kxCell(x, "D2", ss)
	e.kubun = kxCell(x, "C4", ss)
	e.jikan = kxCell(x, "M4", ss)
	e.basho = kxCell(x, "C5", ss)
	e.aite = kxCell(x, "M5", ss)
	e.mokuteki = kxCell(x, "C6", ss)
	e.body = strings.Trim(strings.ReplaceAll(kxCell(x, "A7", ss), "\r\n", "\n"), "\n")
	if strings.TrimSpace(e.body) == "" {
		return nil, fmt.Errorf("本文（A7）が空です")
	}
	return e, nil
}

func (e *kxDay) head() string {
	wd := e.weekday
	if wd == "" || wd == "？" {
		wd = wdays[e.date.Weekday()]
	}
	h := warekiDate(e.date) + "（" + wd + "）"
	if e.jikan != "" {
		h += " " + e.jikan
	}
	return h
}

func (e *kxDay) bodyLines() int {
	n := 0
	for _, l := range strings.Split(e.body, "\n") {
		w := runeWidth(l)
		n += max(1, int((w+keikaXPerLine-0.001)/keikaXPerLine))
	}
	return n
}

// isDayXlsx: 支援経過（1日分）の新しい形（Excel）か（2_完成 の一覧で、書き足せる印を付けるため）
func isDayXlsx(path string) bool {
	// 改善21：1日分は必ずシート1つ。シートの数が違えば中身を開かない。結果は覚えておく
	return cachedKind(path, "day", func() (bool, bool) {
		n := wbSheetCount(path)
		if n < 0 {
			return false, false
		}
		return n == 1 && isDayXlsxFull(path), true
	})
}

func isDayXlsxFull(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.Size() > 8<<20 {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_, err = parseDayXlsx(data)
	return err == nil
}

// ---------------- 場所：サーバーの利用者フォルダ・.xlsm ----------------

// keikaServerDir: サーバーの利用者フォルダ（サーバーへ保存と同じ探し方）。見つからない・2つ以上ならメッセージを返す
func (s *panelServer) keikaServerDir(uid string) (name, dir, problem string) {
	rows, err := s.nameRowsWithCodes()
	if err != nil {
		return "", "", err.Error()
	}
	var row *nameRow
	for i := range rows {
		if rows[i].Code == uid && rows[i].Kind == "利用者" {
			if row != nil {
				return "", "", "氏名一覧に【" + uid + "】の利用者が2人以上います。氏名一覧を直してから、もう一度開いてください"
			}
			row = &rows[i]
		}
	}
	if row == nil {
		return "", "", "氏名一覧に【" + uid + "】の利用者が見つかりません"
	}
	st := s.serverSettings()
	var found []string
	if dirExists(st.Base) {
		for _, d := range findUserDirs(st.Base, row.Name) {
			found = append(found, filepath.Join(st.Base, d))
		}
	} else {
		return row.Name, "", "サーバーの利用者フォルダ（" + st.Base + "）が見つかりません。サーバーにつながっているかを確かめてください。何も書いていません"
	}
	if dirExists(st.Ended) {
		for _, d := range findUserDirs(st.Ended, row.Name) {
			found = append(found, filepath.Join(st.Ended, d))
		}
	}
	switch len(found) {
	case 0:
		return row.Name, "", "サーバーに、この方の利用者フォルダが見つかりません。先に「サーバーへ保存」で利用者フォルダを作ってから、40フォルダの元の .xlsm をコピーして置いてください。何も書いていません"
	case 1:
		if !strictlyWithin(st.Base, found[0]) && !strictlyWithin(st.Ended, found[0]) {
			return row.Name, "", "保存先がサーバーの利用者フォルダの外になるため、書きません"
		}
		return row.Name, found[0], ""
	}
	return row.Name, "", "この方の利用者フォルダが2つ以上見つかりました。どちらに書くか決められないので書きません。フォルダを1つにしてから、もう一度開いてください"
}

// keikaXlsmFiles: フォルダの中の「支援経過記録」で始まる .xlsm（ロックファイル ~$ は除く）
func keikaXlsmFiles(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, "~$") || strings.HasPrefix(n, ".") {
			continue
		}
		if strings.HasPrefix(normKX(n), keikaXPrefix) && strings.EqualFold(filepath.Ext(n), ".xlsm") {
			if st, err := os.Lstat(filepath.Join(dir, n)); err == nil && st.Mode().IsRegular() {
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

func normKX(s string) string { return toHalfDigits(s) }

// xlsmLocked: Excelで開いているか（~$ のロックファイル。名前の先頭2字が ~$ に変わる形も見る）
func xlsmLocked(dir, base string) bool {
	if officeOpen(filepath.Join(dir, base)) {
		return true
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "~$") && len(n) > 2 && strings.HasSuffix(base, n[2:]) {
			return true
		}
	}
	return false
}

// ---------------- 書き足しの計画 ----------------

type keikaXPlan struct {
	ID        string      `json:"id"`
	User      string      `json:"user"`
	UserLabel string      `json:"userLabel"`
	Source    string      `json:"source"`
	Sheet     string      `json:"sheet"`     // 足すシートの名前
	After     string      `json:"after"`     // この右に入れる（一覧）
	Head      string      `json:"head"`      // 1行の見出し（R8.10.3（金）14:00〜14:40）
	Fields    [][2]string `json:"fields"`    // 区分・時間・場所・相手・目的
	BodyFirst []string    `json:"bodyFirst"` // 本文の最初の数行
	BodyLines int         `json:"bodyLines"`
	Dir       string      `json:"dir"`  // 表示用：利用者フォルダの名前（サーバー）
	Xlsm      string      `json:"xlsm"` // 表示用：.xlsm の名前
	Sheets    []string    `json:"sheets"`
	Warnings  []string    `json:"warnings"`
	Blocked   string      `json:"blocked,omitempty"`
	DayFile   string      `json:"dayFile,omitempty"` // 利用者フォルダに「支援経過1日分_…xlsx」があるとき、その名前（改善28：.xlsm が無い方は、こちらの形で足す）
	BookFile  string      `json:"bookFile,omitempty"` // 利用者フォルダに「支援経過_氏名.xlsx」があるとき、その名前（改善29 No.57：「サーバーへ保存」で足す）
	CanCreate bool        `json:"canCreate,omitempty"` // .xlsm も1日分Excelも「支援経過_…」もない方：「サーバーへ保存」で「支援経過_氏名.xlsx」を新しく作れる（改善29 No.57）
	created   time.Time
	srcPath   string
	srcHash   string
	dirPath   string
	xlsmPath  string
	xlsmSig   string
	day       *kxDay
}

func (s *panelServer) keikaSrcExt(folder, name, ext string) (string, string, error) {
	if !safeName(name) || !safeName(folder) || !strings.EqualFold(filepath.Ext(name), ext) {
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

// kxSheetNames: .xlsm のシート名（並び順）
func kxSheetNames(p *ooPkg) ([]string, error) {
	sh, err := wbSheets(p)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, x := range sh {
		out = append(out, x.name)
	}
	return out, nil
}

// kxChooseName: 足すシートの名前。同じ名前があれば ②③… を付ける。同じ日に同じ本文がもう入っていれば dup にそのシート名を返す
func kxChooseName(p *ooPkg, day *kxDay) (name, dup string, err error) {
	sheets, _ := wbSheets(p)
	used := map[string]bool{}
	for _, sh := range sheets {
		used[strings.ToLower(sh.name)] = true
	}
	ss := sharedStrings(p)
	m := reDaySheet.FindStringSubmatch(day.sheet)
	base := m[1] + "." + m[2]
	start := 0
	if m[3] != "" {
		for i, c := range keikaXCirc {
			if string(c) == m[3] {
				start = i + 1
			}
		}
	}
	cands := []string{}
	for i := start; i <= len(keikaXCirc); i++ {
		if i == 0 {
			cands = append(cands, base)
		} else {
			cands = append(cands, base+string(keikaXCirc[i-1]))
		}
	}
	// 同じ日・同じ本文のシートがもうあるか
	for _, sh := range sheets {
		if mm := reDaySheet.FindStringSubmatch(toHalfDigits(sh.name)); mm != nil && mm[1]+"."+mm[2] == base {
			if x, ok := p.get(sh.target); ok {
				if strings.Trim(strings.ReplaceAll(kxCell(x, "A7", ss), "\r\n", "\n"), "\n") == day.body {
					return "", sh.name, nil
				}
			}
		}
	}
	for _, c := range cands {
		if !used[strings.ToLower(c)] {
			return c, "", nil
		}
	}
	return "", "", fmt.Errorf("同じ日（%s）のシートが多すぎます。シートを整理してから、もう一度開いてください", base)
}

func (s *panelServer) buildKeikaXPlan(folder, name string) (*keikaXPlan, error) {
	src, uid, err := s.keikaSrcExt(folder, name, ".xlsx")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("読めませんでした：%s", name)
	}
	day, err := parseDayXlsx(data)
	if err != nil {
		return nil, fmt.Errorf("この書類は、支援経過（1日分）の新しい形ではありません。%v", err)
	}
	sum := sha256.Sum256(data)
	pl := &keikaXPlan{ID: newPlanID(), User: uid, Source: name, After: keikaXList, Head: day.head(), BodyLines: len(strings.Split(day.body, "\n")),
		Warnings: []string{}, BodyFirst: []string{}, Sheets: []string{}, Fields: [][2]string{}, created: time.Now(), srcPath: src, srcHash: hex.EncodeToString(sum[:]), day: day}
	for i, l := range strings.Split(day.body, "\n") {
		if i >= 6 {
			break
		}
		pl.BodyFirst = append(pl.BodyFirst, l)
	}
	for _, f := range [][2]string{{"区分", day.kubun}, {"時間", day.jikan}, {"場所", day.basho}, {"相手", day.aite}, {"目的", day.mokuteki}} {
		pl.Fields = append(pl.Fields, [2]string{f[0], f[1]})
	}
	uname, dir, problem := s.keikaServerDir(uid)
	pl.UserLabel = uname + "（" + uid + "）"
	if problem != "" {
		pl.Blocked = problem
		return pl, nil
	}
	pl.dirPath = dir
	pl.Dir = filepath.Base(filepath.Dir(dir)) + "＼" + filepath.Base(dir)
	files := keikaXlsmFiles(dir)
	switch {
	case len(files) == 0:
		if bk := bookXlsxFiles(dir); len(bk) > 0 {
			pl.BookFile = strings.Join(bk, "・")
		} else if dx := dayXlsxFiles(dir); len(dx) > 0 {
			pl.DayFile = strings.Join(dx, "・")
		} else {
			pl.CanCreate = true
		}
		pl.Blocked = "この方の利用者フォルダに、支援経過の .xlsm（名前が「" + keikaXPrefix + "」で始まるもの）がありません。40フォルダの元の .xlsm をコピーして、利用者のフォルダに置いてください（パネルは .xlsm を新しく作りません）。何も書いていません"
		return pl, nil
	case len(files) > 1:
		pl.Blocked = "この方の利用者フォルダに、支援経過の .xlsm が2つ以上あります（" + strings.Join(files, "・") + "）。どれに書くか決められないので書きません。1つにしてから、もう一度開いてください"
		return pl, nil
	}
	pl.Xlsm = files[0]
	pl.xlsmPath = filepath.Join(dir, files[0])
	if xlsmLocked(dir, files[0]) {
		pl.Blocked = files[0] + " がExcelで開かれています。閉じてから、もう一度開いてください。何も書いていません"
		return pl, nil
	}
	pl.xlsmSig = fileSig(pl.xlsmPath)
	b, err := os.ReadFile(pl.xlsmPath)
	if err != nil {
		pl.Blocked = files[0] + " を読めません（Excelで開いていたら閉じてください）。何も書いていません"
		return pl, nil
	}
	p, err := readPkg(b)
	if err != nil {
		pl.Blocked = files[0] + " をExcelとして開けません。何も書いていません"
		return pl, nil
	}
	names, _ := kxSheetNames(p)
	has := func(n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}
	if !has(keikaXTemplate) || !has(keikaXList) {
		pl.Blocked = files[0] + " に「" + keikaXTemplate + "」「" + keikaXList + "」のシートがありません。40フォルダの元の .xlsm（新しい原紙の形）をコピーして置いてください。何も書いていません"
		return pl, nil
	}
	if !p.has("xl/vbaProject.bin") {
		pl.Warnings = append(pl.Warnings, "この .xlsm にはマクロ（vbaProject.bin）が入っていません。シートは足せますが、開いて保存しても一覧には写りません。40フォルダの元の .xlsm か確かめてください")
	}
	{
		sheets, _ := wbSheets(p)
		ss := sharedStrings(p)
		for _, sh := range sheets {
			if sh.name == keikaXTemplate {
				x, _ := p.get(sh.target)
				if kxCell(x, "A4", ss) != "区分" || kxCell(x, "A6", ss) != "目的" || !strings.Contains(x, `<mergeCell ref="A7:T41"/>`) {
					pl.Blocked = files[0] + " の「原紙」が新しい形（区分・目的・本文A7）ではありません。40フォルダの元の .xlsm をコピーして置いてください。何も書いていません"
					return pl, nil
				}
			}
		}
	}
	nm, dup, err := kxChooseName(p, day)
	if err != nil {
		pl.Blocked = err.Error()
		return pl, nil
	}
	if dup != "" {
		pl.Blocked = "この1日分（" + day.head() + "）は、すでにシート「" + dup + "」に入っています。2回目は書き足しません"
		return pl, nil
	}
	pl.Sheet = nm
	pl.Sheets = names
	if nm != day.sheet {
		pl.Warnings = append(pl.Warnings, "シート名「"+day.sheet+"」がすでにあるので、「"+nm+"」にして足します")
	}
	if n := day.bodyLines(); n > keikaXMaxLines {
		pl.Warnings = append(pl.Warnings, fmt.Sprintf("本文が約 %d 行あり、枠（%d行・1行47字）に入りきらない見込みです", n, keikaXMaxLines))
	}
	if day.code == "" {
		pl.Warnings = append(pl.Warnings, "利用者の名前（D2）が空です。足したあとで入れてください")
	}
	return pl, nil
}

// ---------------- 書き足した中身を作る・確かめる ----------------

func xlsmNumCell(n int) [2]string { return [2]string{"", "<v>" + strconv.Itoa(n) + "</v>"} }

// buildKeikaXBook: .xlsm の「原紙」を複製して、1日分の値を入れたシートを「一覧」のすぐ右に足す。ほかの部品はそのまま
func buildKeikaXBook(base []byte, day *kxDay, sheetName string) ([]byte, error) {
	p, err := readPkg(base)
	if err != nil {
		return nil, err
	}
	cells := map[string][2]string{
		"L2": xlsmNumCell(day.y), "N2": xlsmNumCell(day.m), "P2": xlsmNumCell(day.d),
		"A7": {"inlineStr", inlineStrXML(day.body)},
	}
	if day.code != "" {
		cells["D2"] = [2]string{"inlineStr", inlineStrXML(day.code)}
	}
	for ref, v := range map[string]string{"C4": day.kubun, "M4": day.jikan, "C5": day.basho, "M5": day.aite, "C6": day.mokuteki} {
		if v != "" {
			cells[ref] = [2]string{"inlineStr", inlineStrXML(strings.ReplaceAll(v, "\n", "　"))}
		}
	}
	wd := day.weekday
	if wd == "" {
		wd = wdays[day.date.Weekday()]
	}
	ns := newSheet{name: sheetName, cells: cells, cache: map[string]string{"S2": wd}}
	if err := addSheetsFromTemplateAfter(p, keikaXTemplate, keikaXList, []newSheet{ns}); err != nil {
		return nil, err
	}
	out, err := p.bytes()
	if err != nil {
		return nil, err
	}
	if err := verifyKeikaX(base, out, sheetName, day); err != nil {
		return nil, err
	}
	return out, nil
}

type kxDef struct{ name, sheet, text string }

func kxDefs(p *ooPkg) ([]kxDef, []string, error) {
	names, err := kxSheetNames(p)
	if err != nil {
		return nil, nil, err
	}
	wb, _ := p.get("xl/workbook.xml")
	var out []kxDef
	for _, d := range reDefName.FindAllString(wb, -1) {
		sh := ""
		if v := attr(d, "localSheetId"); v != "" {
			i, e := strconv.Atoi(v)
			if e != nil || i < 0 || i >= len(names) {
				return nil, nil, fmt.Errorf("名前（%s）の localSheetId=%s がシートの数を超えています", attr(d, "name"), v)
			}
			sh = names[i]
		}
		txt := d[strings.Index(d, ">")+1 : strings.LastIndex(d, "<")]
		out = append(out, kxDef{attr(d, "name"), sh, txt})
	}
	return out, names, nil
}

// verifyKeikaX: 書き足した結果を、書く前に必ず確かめる（だめなら何も書かない）
func verifyKeikaX(oldData, newData []byte, sheetName string, day *kxDay) error {
	return verifyKeikaXAt(oldData, newData, sheetName, day, func(on []string) int {
		li := -1
		for i, n := range on {
			if n == keikaXList {
				li = i
			}
		}
		return li + 1
	})
}

// verifyKeikaXAt: insertAt は、もとのシートの並びを見て「新しいシートを入れる番号」を返す（改善28：支援経過1日分のExcelでは一覧の右か、いちばん左）
func verifyKeikaXAt(oldData, newData []byte, sheetName string, day *kxDay, insertAt func(on []string) int) error {
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
	changed := map[string]bool{"xl/workbook.xml": true, "xl/_rels/workbook.xml.rels": true, "[Content_Types].xml": true, "docProps/app.xml": true}
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
	if op.has("xl/vbaProject.bin") {
		a, _ := op.get("xl/vbaProject.bin")
		b, ok := np.get("xl/vbaProject.bin")
		if !ok || a != b {
			return fmt.Errorf("マクロ（vbaProject.bin）が変わってしまうため、書きません")
		}
	}
	od, on, err := kxDefs(op)
	if err != nil {
		return err
	}
	nd, nn, err := kxDefs(np)
	if err != nil {
		return err
	}
	// シートの並び：もとの並びのまま、「一覧」のすぐ右に1つ入っている
	if len(nn) != len(on)+1 {
		return fmt.Errorf("シートの数が合いません")
	}
	at := insertAt(on)
	want := append(append(append([]string{}, on[:at]...), sheetName), on[at:]...)
	for i := range want {
		if nn[i] != want[i] {
			return fmt.Errorf("シートの並びが思ったとおりになりません（%v）", nn)
		}
	}
	// 名前（印刷範囲など）：もとのものが、同じシートを指したまま全部残り、足したのは新しいシートの分だけ
	key := func(d kxDef) string { return d.name + "|" + d.sheet + "|" + d.text }
	have := map[string]int{}
	for _, d := range nd {
		have[key(d)]++
	}
	for _, d := range od {
		if have[key(d)] == 0 {
			return fmt.Errorf("印刷範囲などの名前（%s）がずれてしまうため、書きません", d.name)
		}
		have[key(d)]--
	}
	for k, c := range have {
		if c > 0 && !strings.Contains(k, "|"+sheetName+"|") {
			return fmt.Errorf("関係ないシートの名前が増えています（%s）", k)
		}
	}
	// workbook.xml：sheetId・r:id が重ならず、部品がある。content type もある
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
	// 新しいシート：値が入っている・codeName が重ならない
	sheets, _ := wbSheets(np)
	cn := map[string]bool{}
	for _, sh := range sheets {
		x, _ := np.get(sh.target)
		if m := regexp.MustCompile(`<sheetPr\b[^>]*?\bcodeName="([^"]*)"`).FindStringSubmatch(x); m != nil {
			if cn[m[1]] {
				return fmt.Errorf("シートの codeName（%s）が重なっています", m[1])
			}
			cn[m[1]] = true
		}
		if sh.name == sheetName {
			if !strings.Contains(x, esc4(day.body)) && strings.TrimSpace(day.body) != "" {
				return fmt.Errorf("本文が新しいシートに入っていません")
			}
			if !regexp.MustCompile(`<c r="P2"[^>]*><v>` + strconv.Itoa(day.d) + `</v>`).MatchString(x) {
				return fmt.Errorf("日付が新しいシートに入っていません")
			}
			if strings.Contains(x, "tabSelected") {
				return fmt.Errorf("新しいシートが選ばれた状態になっています")
			}
		}
	}
	app, _ := np.get("docProps/app.xml")
	if oa, ok := op.get("docProps/app.xml"); ok && oa != "" {
		// 題名の並びにシート名が、シートの並びどおりに入っている
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

func esc4(s string) string { return encodeText(xmlSafe(s)) }

// ---------------- API ----------------

func (s *panelServer) keikaXPreview(r *http.Request) (any, error) {
	var q struct{ Folder, Name string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	pl, err := s.buildKeikaXPlan(q.Folder, q.Name)
	if err != nil {
		return nil, err
	}
	if s.keikaXPlans == nil {
		s.keikaXPlans = map[string]*keikaXPlan{}
	}
	for k, v := range s.keikaXPlans {
		if time.Since(v.created) > planLifetime {
			delete(s.keikaXPlans, k)
		}
	}
	s.keikaXPlans[pl.ID] = pl
	return pl, nil
}

// writeNoDelete: 新しいファイルとして書く。途中で失敗しても、サーバーのファイルは消さない
func writeNoDelete(dir, name string, data []byte) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i < 100; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s(%d)%s", base, i, ext)
		}
		p := filepath.Join(dir, n)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return p, err
		}
		return p, f.Close()
	}
	return "", fmt.Errorf("同じ名前のファイルが多すぎます")
}

func (s *panelServer) keikaXAppend(r *http.Request) (any, error) {
	var q struct{ ID string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	pl := s.keikaXPlans[q.ID]
	if pl == nil || time.Since(pl.created) > planLifetime {
		return nil, fmt.Errorf("確認の画面が古くなりました。もう一度「書き足す」の画面を開いてください（まだ何も書いていません）")
	}
	delete(s.keikaXPlans, q.ID) // 1回だけ使える
	if pl.Blocked != "" {
		return nil, fmt.Errorf("%s", pl.Blocked)
	}
	data, err := os.ReadFile(pl.srcPath)
	sum := sha256.Sum256(data)
	if err != nil || hex.EncodeToString(sum[:]) != pl.srcHash {
		return nil, fmt.Errorf("確認の画面のあとで、元の書類（%s）が変わりました。もう一度開き直してください（まだ何も書いていません）", pl.Source)
	}
	st := s.serverSettings()
	if !strictlyWithin(st.Base, pl.dirPath) && !strictlyWithin(st.Ended, pl.dirPath) {
		return nil, fmt.Errorf("保存先がサーバーの利用者フォルダの外になるため、書きません（まだ何も書いていません）")
	}
	if filepath.Dir(pl.xlsmPath) != pl.dirPath || !strings.EqualFold(filepath.Ext(pl.xlsmPath), ".xlsm") {
		return nil, fmt.Errorf("書く先が正しくありません（まだ何も書いていません）")
	}
	// 書く直前にもう一度：.xlsm が1つだけ・開かれていない・確認のあとで変わっていない
	if fs := keikaXlsmFiles(pl.dirPath); len(fs) != 1 || fs[0] != pl.Xlsm {
		return nil, fmt.Errorf("確認の画面のあとで、利用者フォルダの .xlsm が変わりました。もう一度開き直してください（まだ何も書いていません）")
	}
	if xlsmLocked(pl.dirPath, pl.Xlsm) {
		return nil, fmt.Errorf("%s がExcelで開かれています。閉じてから、もう一度押してください（まだ何も書いていません）", pl.Xlsm)
	}
	if fileSig(pl.xlsmPath) != pl.xlsmSig {
		return nil, fmt.Errorf("確認の画面のあとで、%s が変わりました。もう一度開き直してください（まだ何も書いていません）", pl.Xlsm)
	}
	orig, err := os.ReadFile(pl.xlsmPath)
	if err != nil {
		return nil, fmt.Errorf("%s を読めませんでした（Excelで開いていたら閉じてください）。まだ何も書いていません", pl.Xlsm)
	}
	if fileSig(pl.xlsmPath) != pl.xlsmSig {
		return nil, fmt.Errorf("読んでいる間に %s が変わりました。もう一度開き直してください（まだ何も書いていません）", pl.Xlsm)
	}
	// 中身を先に作って確かめる（だめなら何も書かない）
	p0, err := readPkg(orig)
	if err != nil {
		return nil, fmt.Errorf("%s をExcelとして開けません（まだ何も書いていません）", pl.Xlsm)
	}
	nm, dup, err := kxChooseName(p0, pl.day)
	if err != nil {
		return nil, fmt.Errorf("%v（まだ何も書いていません）", err)
	}
	if dup != "" {
		return nil, fmt.Errorf("この1日分は、すでにシート「%s」に入っています。2回目は書き足しません", dup)
	}
	newData, err := buildKeikaXBook(orig, pl.day, nm)
	if err != nil {
		return nil, fmt.Errorf("書き足す中身を作れませんでした（まだ何も書いていません）：%v", err)
	}
	// 控え（同じフォルダの「控え」。消さない・上書きしない）
	bdir := filepath.Join(pl.dirPath, keikaBackupDir)
	if err := os.MkdirAll(bdir, 0755); err != nil {
		return nil, fmt.Errorf("「控え」のフォルダを作れませんでした（まだ何も書いていません）：%v", err)
	}
	stamp := time.Now().Format("20060102_150405")
	bp, err := writeNoDelete(bdir, stamp+"_"+pl.Xlsm, orig)
	if err != nil || !sameFile(bp, orig) {
		return nil, fmt.Errorf("控えを正しく取れませんでした。%s は書き換えていません。控えの途中のファイルが残っていたら、あとで手で消してください（パネルはサーバーで消しません）", pl.Xlsm)
	}
	// 書く：同じフォルダに一時の名前（.part）で書いて、確かめてから、元の名前へ入れ替える（元の .xlsm への書き足しだけ）
	part, err := writeNoDelete(pl.dirPath, pl.Xlsm+".part", newData)
	if err != nil || !sameFile(part, newData) {
		return nil, fmt.Errorf("書き足したファイルを正しく作れませんでした。%s はそのままです（控え：%s）。途中のファイル（%s）が残っていたら、あとで手で消してください", pl.Xlsm, filepath.Base(bp), filepath.Base(part))
	}
	if xlsmLocked(pl.dirPath, pl.Xlsm) || fileSig(pl.xlsmPath) != pl.xlsmSig {
		return nil, fmt.Errorf("書く直前に %s が開かれたか変わったため、入れ替えませんでした。%s はそのままです。途中のファイル（%s）はそのまま残しています", pl.Xlsm, pl.Xlsm, filepath.Base(part))
	}
	if err := os.Rename(part, pl.xlsmPath); err != nil {
		return nil, fmt.Errorf("%s に入れ替えられませんでした（Excelで開いていたら閉じてください）。%s はそのままです。途中のファイル（%s）はそのまま残しています：%v", pl.Xlsm, pl.Xlsm, filepath.Base(part), err)
	}
	s.writeKeikaXHistory(pl, nm)
	return map[string]any{
		"ok":     "「" + pl.Xlsm + "」の「一覧」のすぐ右に、シート「" + nm + "」（" + pl.Head + "）を足しました。",
		"note":   "Excelで開いて保存すると、「一覧」に写ります。",
		"backup": keikaBackupDir + "＼" + filepath.Base(bp),
		"dir":    pl.Dir, "sheet": nm, "user": pl.User,
	}, nil
}

func (s *panelServer) writeKeikaXHistory(pl *keikaXPlan, sheet string) {
	line := time.Now().Format("2006/01/02 15:04") + "\t【" + pl.User + "】\t支援経過（1日分）\tシート「" + sheet + "」を .xlsm に書き足し"
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
