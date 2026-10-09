package main

// 完成した第1表（Excel原紙）に、カイポケのCSVの情報を入れる（改善19・R8.10.3）
//   - 「6 完成した書類」で、名前に戻した第1表のExcelに「カイポケの情報を第1表に入れる」を出す。
//   - 確認画面（何をどの欄に入れるか・すでに入っている欄は変えない）→「入れる」を押したときだけ書く。
//   - 書き方は XML を直接。書式・入力規則・図形・ハイパーリンク・AN列の早見表などはバイトのまま残す（直すのは第1表のシートだけ）。
//   - 書く前に、2_完成＼控え に元のファイルの写しを取り、一時ファイル(.part)に書いてから入れ替える。
//   - 初回居宅サービス計画作成日は、サーバーの利用者フォルダの前回の第1表から引き継ぐ（サーバーは読むだけ）。
//   - CSVは読むだけ。CSVの中身はAIに渡さない・AI作業のフォルダには書かない。

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// hyo1SheetName: ケアプランのブックの中の第1表のシートの名前（改善27の2。2表・3表と並ぶ）
const hyo1SheetName = "1表"

// 第1表の欄（原紙 R8.10.3案。fill_hyo1.py の CELLS・SELECT と同じ番地）
const (
	h1Created  = "AH1" // 作成年月日
	h1Name     = "C3"  // 利用者名
	h1Birth    = "L3"  // 生年月日
	h1Addr     = "U3"  // 住所
	h1PlanDate = "F6"  // 居宅サービス計画作成（変更）日
	h1First    = "AA6" // 初回居宅サービス計画作成日
	h1Cert     = "B7"  // 認定日
	h1From     = "N7"  // 認定の有効期間 始
	h1To       = "T7"  // 認定の有効期間 終
	h1Agree    = "S19" // 説明・同意日
	h1Level    = "E9"  // 要介護状態区分（選択セル 1〜5）
	h1Status   = "AH2" // 認定済／申請中（選択セル）
)

type hyo1Doc struct {
	pkg    *ooPkg
	target string // 第1表のシート（xl/worksheets/sheetN.xml）
	sheet  string
	ss     []string
	styles string
}

// parseHyo1: 第1表（原紙の形）のExcelか確かめる。シートに「居宅サービス計画書（１）」があり、欄の位置が原紙どおりのもの
func parseHyo1(data []byte) (*hyo1Doc, error) {
	p, err := readPkg(data)
	if err != nil {
		return nil, fmt.Errorf("Excelとして開けません")
	}
	sheets, err := wbSheets(p)
	if err != nil {
		return nil, err
	}
	ss := sharedStrings(p)
	for _, sh := range sheets {
		x, ok := p.get(sh.target)
		if !ok {
			continue
		}
		g1 := toHalfDigits(strings.NewReplacer("（", "(", "）", ")").Replace(kxCell(x, "G1", ss)))
		if !strings.Contains(g1, "居宅サービス計画書") || !strings.Contains(g1, "(1)") {
			continue
		}
		if kxCell(x, "A3", ss) != "利用者名" || kxCell(x, "K3", ss) != "生年月日" || kxCell(x, "T3", ss) != "住所" ||
			!strings.Contains(x, `<mergeCell ref="C3:F3"/>`) || !strings.Contains(x, `<mergeCell ref="U3:AL3"/>`) {
			return nil, fmt.Errorf("第1表の形が原紙（R8.10.3案）と違います")
		}
		for _, a := range []string{h1Created, h1Birth, h1PlanDate, h1First, h1Cert, h1From, h1To, h1Agree, h1Level, h1Status} {
			if !hasCell(x, a) {
				return nil, fmt.Errorf("第1表の欄（%s）が見つかりません", a)
			}
		}
		st, _ := p.get("xl/styles.xml")
		return &hyo1Doc{pkg: p, target: sh.target, sheet: x, ss: ss, styles: st}, nil
	}
	return nil, fmt.Errorf("第1表（居宅サービス計画書（１））のシートが見つかりません")
}

func hasCell(sheet, addr string) bool {
	return strings.Contains(sheet, `<c r="`+addr+`"`)
}

func isHyo1Xlsx(path string) bool {
	// 改善21：第1表の原紙はシート1つ（多めに見て3つまで）。シートの多いブック（ケアプランなど）は中身を開かない。結果は覚えておく
	return cachedKind(path, "hyo1", func() (bool, bool) {
		n := wbSheetCount(path)
		if n < 0 {
			return false, false
		}
		// 第1表を「1表」シートとして入れたケアプランのブック（改善27の2）も、第1表として扱う
		return n >= 1 && (n <= 3 || wbHasSheet(path, hyo1SheetName)) && isHyo1XlsxFull(path), true
	})
}

func isHyo1XlsxFull(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.Size() > 8<<20 {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_, err = parseHyo1(data)
	return err == nil
}

// h1Raw: セルの中身。数値セルなら num=true
func h1Raw(sheet, addr string, ss []string) (txt string, num bool) {
	re := regexp.MustCompile(`(?s)<c r="` + addr + `"((?:\s[^>]*?)?)(?:/>|>(.*?)</c>)`)
	m := re.FindStringSubmatch(sheet)
	if m == nil {
		return "", false
	}
	typ := attr("<c"+m[1]+">", "t")
	inner := m[2]
	switch typ {
	case "s":
		if v := regexp.MustCompile(`<v>(\d+)</v>`).FindStringSubmatch(inner); v != nil {
			if i, _ := strconv.Atoi(v[1]); i < len(ss) {
				txt = ss[i]
			}
		}
	case "inlineStr":
		var b strings.Builder
		for _, t := range regexp.MustCompile(`(?s)<t(?:\s[^>]*)?>(.*?)</t>`).FindAllStringSubmatch(inner, -1) {
			b.WriteString(xmlUnesc(t[1]))
		}
		txt = b.String()
	default:
		if v := regexp.MustCompile(`(?s)<v>(.*?)</v>`).FindStringSubmatch(inner); v != nil {
			txt = xmlUnesc(v[1])
		}
	}
	txt = strings.TrimSpace(txt)
	return txt, txt != "" && (typ == "" || typ == "n")
}

// ---------------- 和暦の表示（確認画面用） ----------------

func warekiLong(t time.Time) string {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	eras := []struct {
		name  string
		start time.Time
	}{{"令和", time.Date(2019, 5, 1, 0, 0, 0, 0, time.UTC)}, {"平成", time.Date(1989, 1, 8, 0, 0, 0, 0, time.UTC)},
		{"昭和", time.Date(1926, 12, 25, 0, 0, 0, 0, time.UTC)}, {"大正", time.Date(1912, 7, 30, 0, 0, 0, 0, time.UTC)},
		{"明治", time.Date(1868, 1, 25, 0, 0, 0, 0, time.UTC)}}
	for _, e := range eras {
		if !d.Before(e.start) {
			y := d.Year() - e.start.Year() + 1
			ys := strconv.Itoa(y)
			if y == 1 {
				ys = "元"
			}
			return fmt.Sprintf("%s%s年%d月%d日", e.name, ys, int(d.Month()), d.Day())
		}
	}
	return fmt.Sprintf("%d年%d月%d日", d.Year(), int(d.Month()), d.Day())
}

func serialDate(n int) time.Time {
	return time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n)
}

// h1Show: 画面に出す文字（日付の欄は、数値なら和暦にする）
func h1Show(txt string, num, isDate bool) string {
	if isDate && num {
		if n, err := strconv.ParseFloat(txt, 64); err == nil && n > 1 {
			return warekiLong(serialDate(int(n)))
		}
	}
	return txt
}

// ---------------- 住所の字数の上限（fill_hyo1.py と同じ計算） ----------------

func zenWidth(s string) float64 {
	w := 0.0
	for _, r := range s {
		switch {
		case r >= 0xFF61 && r <= 0xFF9F:
			w += 0.55
		case r < 0x80:
			w += 0.55
		case r >= 0x2E80 || (r >= 0x2010 && r < 0x2E80):
			w += 1.0
		case strings.ContainsRune("§¨°±´¶×÷", r):
			w += 1.0
		default:
			w += 0.55
		}
	}
	return w
}

func h1ColWidths(sheet string) map[int]float64 {
	w := map[int]float64{}
	for _, t := range regexp.MustCompile(`<col\b[^>]*>`).FindAllString(sheet, -1) {
		wd, err := strconv.ParseFloat(attr(t, "width"), 64)
		if err != nil {
			continue
		}
		lo, hi := atoi(attr(t, "min")), atoi(attr(t, "max"))
		for i := lo; i <= hi && i <= 200; i++ {
			w[i] = wd
		}
	}
	return w
}

func h1ColNum(c string) int {
	n := 0
	for _, ch := range c {
		n = n*26 + int(ch) - 64
	}
	return n
}

// h1AddrCap: 住所の欄に入る全角換算の字数（9pt。列幅1あたり6.09pt・左右6pt・字下げ1段=9pt）
func (d *hyo1Doc) h1AddrCap() float64 {
	widths := h1ColWidths(d.sheet)
	c1, c2 := "U", "AL"
	if m := regexp.MustCompile(`<mergeCell ref="([A-Z]+)3:([A-Z]+)3"/>`).FindAllStringSubmatch(d.sheet, -1); m != nil {
		for _, x := range m {
			if x[1] == "U" {
				c1, c2 = x[1], x[2]
			}
		}
	}
	span := 0.0
	for i := h1ColNum(c1); i <= h1ColNum(c2); i++ {
		w, ok := widths[i]
		if !ok {
			w = 8.43
		}
		span += w * 6.09
	}
	pad := 6.0 + 9.0*float64(d.indentOf(h1Addr))
	return math.Floor((span-pad)/9.0*2) / 2
}

func (d *hyo1Doc) indentOf(addr string) int {
	m := regexp.MustCompile(`<c r="` + addr + `"((?:\s[^>]*?)?)(?:/>|>)`).FindStringSubmatch(d.sheet)
	if m == nil {
		return 0
	}
	sid := regexp.MustCompile(`\ss="(\d+)"`).FindStringSubmatch(m[1])
	if sid == nil {
		return 0
	}
	i0, i1 := strings.Index(d.styles, "<cellXfs"), strings.Index(d.styles, "</cellXfs>")
	if i0 < 0 || i1 < i0 {
		return 0
	}
	xfs := regexp.MustCompile(`(?s)<xf\b[^>]*?(?:/>|>.*?</xf>)`).FindAllString(d.styles[i0:i1], -1)
	n := atoi(sid[1])
	if n >= len(xfs) {
		return 0
	}
	if x := regexp.MustCompile(`indent="(\d+)"`).FindStringSubmatch(xfs[n]); x != nil {
		return atoi(x[1])
	}
	return 0
}

// ---------------- 確認画面の計画 ----------------

type hyo1Row struct {
	Label string `json:"label"`
	Cell  string `json:"cell"`
	Value string `json:"value"`
	State string `json:"state"` // 入れる／そのまま／入れない
	Note  string `json:"note"`
}

type hyo1Write struct {
	addr string
	kind string // n（数値）／s（文字）
	num  int
	text string
}

type hyo1Plan struct {
	ID       string    `json:"id"`
	Source   string    `json:"source"`
	User     string    `json:"user"`
	Rows     []hyo1Row `json:"rows"`
	Files    string    `json:"files"` // 使うCSV
	Warnings []string  `json:"warnings"`
	Blocked  string    `json:"blocked,omitempty"`
	NWrite   int       `json:"nWrite"`
	created  time.Time
	srcPath  string
	srcHash  string
	srcSig   string
	writes   []hyo1Write
}

type hyo1Val struct {
	kind string
	num  int
	text string
	show string
}

func (s *panelServer) today() time.Time {
	if v := os.Getenv("MASKTOOL_TODAY"); v != "" {
		if t, ok := parseISODate(v); ok {
			return t
		}
	}
	n := time.Now()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

func (s *panelServer) hyo1Src(folder, name string) (string, error) {
	if !safeName(name) || !safeName(folder) || !strings.EqualFold(filepath.Ext(name), ".xlsx") {
		return "", fmt.Errorf("名前が正しくありません")
	}
	jobs, _ := s.doneJobs(s.codeBook())
	for _, j := range jobs {
		if j.Folder != folder {
			continue
		}
		for _, f := range j.Files {
			if f.Name == name {
				return filepath.Join(s.p.out, name), nil
			}
		}
	}
	return "", fmt.Errorf("⑥完成した書類 に、この仕事の書類として見つかりません")
}

var reSymbolName = regexp.MustCompile(`【[^】]*】`)

// h1PrevFirst: サーバーの利用者フォルダの、前回の第1表（名前に「第1表」を含む .xlsx でいちばん新しいもの）の初回居宅サービス計画作成日。読むだけ
func (s *panelServer) h1PrevFirst(name string) (val *hyo1Val, from string, msg string) {
	st := s.serverSettings()
	var found []string
	if dirExists(st.Base) {
		for _, d := range findUserDirs(st.Base, name) {
			found = append(found, filepath.Join(st.Base, d))
		}
	} else {
		return nil, "", "サーバーの利用者フォルダが見つからない（つながっていない）ので、初回居宅サービス計画作成日は空のままです"
	}
	if dirExists(st.Ended) {
		for _, d := range findUserDirs(st.Ended, name) {
			found = append(found, filepath.Join(st.Ended, d))
		}
	}
	switch len(found) {
	case 0:
		return nil, "", "サーバーに、この方の利用者フォルダが見つからないので、初回居宅サービス計画作成日は空のままです"
	case 1:
	default:
		return nil, "", "サーバーに、この方の利用者フォルダが2つ以上あるので、初回居宅サービス計画作成日は空のままです"
	}
	type cand struct {
		path string
		mod  time.Time
	}
	var cs []cand
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			n := e.Name()
			if strings.HasPrefix(n, "~$") || strings.HasPrefix(n, ".") {
				continue
			}
			full := filepath.Join(dir, n)
			if e.IsDir() {
				if depth < 3 && n != keikaBackupDir {
					walk(full, depth+1)
				}
				continue
			}
			if !strings.EqualFold(filepath.Ext(n), ".xlsx") || (!strings.Contains(toHalfDigits(n), "第1表") && !wbHasSheet(full, hyo1SheetName)) {
				continue // 名前に「第1表」を含むもの、または「1表」のシートのあるケアプランのブック（改善27の2）
			}
			fi, err := os.Stat(full)
			if err != nil || !fi.Mode().IsRegular() || fi.Size() > 8<<20 {
				continue
			}
			cs = append(cs, cand{full, fi.ModTime()})
		}
	}
	walk(found[0], 0)
	if len(cs) == 0 {
		return nil, "", "サーバーの利用者フォルダに、前回の第1表（名前に「第1表」を含む .xlsx、または「1表」のシートのあるケアプランのブック）が見つからないので、初回居宅サービス計画作成日は空のままです"
	}
	sort.SliceStable(cs, func(a, b int) bool { return cs[a].mod.After(cs[b].mod) })
	for _, c := range cs {
		b, err := os.ReadFile(c.path)
		if err != nil {
			continue
		}
		doc, err := parseHyo1(b)
		if err != nil {
			continue
		}
		txt, num := h1Raw(doc.sheet, h1First, doc.ss)
		if txt == "" {
			continue
		}
		if num {
			if f, err := strconv.ParseFloat(txt, 64); err == nil && f > 1 {
				return &hyo1Val{kind: "n", num: int(f), show: warekiLong(serialDate(int(f)))}, filepath.Base(c.path), ""
			}
			continue
		}
		return &hyo1Val{kind: "s", text: txt, show: txt}, filepath.Base(c.path), ""
	}
	return nil, "", "サーバーの前回の第1表（" + strconv.Itoa(len(cs)) + "件）に初回居宅サービス計画作成日が入っていないので、空のままです"
}

func (s *panelServer) buildHyo1Plan(folder, name string) (*hyo1Plan, error) {
	src, err := s.hyo1Src(folder, name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("読めませんでした：%s", name)
	}
	doc, err := parseHyo1(data)
	if err != nil {
		return nil, fmt.Errorf("この書類は、第1表（Excel原紙）の形ではありません。%v", err)
	}
	sum := sha256.Sum256(data)
	pl := &hyo1Plan{ID: newPlanID(), Source: name, Rows: []hyo1Row{}, Warnings: []string{}, created: time.Now(),
		srcPath: src, srcHash: hex.EncodeToString(sum[:]), srcSig: fileSig(src)}
	if officeOpen(src) {
		pl.Blocked = name + " がExcelで開かれています。閉じてから、もう一度開いてください。何も書いていません"
		return pl, nil
	}
	user, _ := h1Raw(doc.sheet, h1Name, doc.ss)
	user = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(user, "様"), "殿"))
	pl.User = user
	if user == "" {
		pl.Blocked = "利用者名の欄（" + h1Name + "）が空です。先に「元に戻す」を押して、名前に戻してください。何も書いていません"
		return pl, nil
	}
	if reSymbolName.MatchString(user) {
		pl.Blocked = "利用者名の欄が、まだ記号のままです。先に「元に戻す」を押して、名前に戻してください。何も書いていません"
		return pl, nil
	}
	today := s.today()
	d := loadKaipoke(filepath.Join(s.p.work, kaipokeCSVDir))
	pl.Files = d.usedFiles()
	f := d.find(user, today)
	pl.Warnings = append(pl.Warnings, d.Notes...)
	if f.Person == nil && f.Cert == nil {
		var ms []string
		if f.PersonMsg != "" {
			ms = append(ms, f.PersonMsg)
		}
		if f.CertMsg != "" {
			ms = append(ms, f.CertMsg)
		}
		pl.Blocked = strings.Join(ms, "。") + "。カイポケの情報は書いていません"
		return pl, nil
	}
	if f.PersonMsg != "" {
		pl.Warnings = append(pl.Warnings, f.PersonMsg)
	}
	if f.CertMsg != "" {
		pl.Warnings = append(pl.Warnings, f.CertMsg)
	}
	pl.Warnings = append(pl.Warnings, f.Notes...)

	var writes []hyo1Write
	add := func(label, addr string, isDate bool, v *hyo1Val, whyNot string) {
		txt, num := h1Raw(doc.sheet, addr, doc.ss)
		row := hyo1Row{Label: label, Cell: addr}
		switch {
		case txt != "":
			row.State, row.Value, row.Note = "そのまま", h1Show(txt, num, isDate), "入っているので変えません"
		case v != nil:
			row.State, row.Value = "入れる", v.show
			writes = append(writes, hyo1Write{addr: addr, kind: v.kind, num: v.num, text: v.text})
		default:
			row.State, row.Note = "入れない", whyNot
		}
		pl.Rows = append(pl.Rows, row)
	}
	dateVal := func(t time.Time) *hyo1Val {
		return &hyo1Val{kind: "n", num: kpSerial(t), show: warekiLong(t)}
	}
	// 利用者名（すでに入っている）
	pl.Rows = append(pl.Rows, hyo1Row{Label: "利用者名", Cell: h1Name, Value: user, State: "そのまま", Note: "入っているので変えません"})
	// 利用者情報
	if f.Person != nil && f.Person.BirthOK {
		add("生年月日", h1Birth, true, dateVal(f.Person.Birth), "")
	} else {
		add("生年月日", h1Birth, true, nil, firstNonEmpty(f.PersonMsg, "生年月日を読めませんでした"))
	}
	switch {
	case f.Person != nil && f.AddrMsg == "":
		capN := doc.h1AddrCap()
		if w := zenWidth(f.Address); w > capN {
			add("住所", h1Addr, false, nil, fmt.Sprintf("長すぎるので書きません（全角換算 %g字 > 上限 %g字）。第1表に手で入れてください：%s", w, capN, f.Address))
		} else {
			add("住所", h1Addr, false, &hyo1Val{kind: "s", text: f.Address, show: f.Address}, "")
		}
	case f.Person != nil:
		add("住所", h1Addr, false, nil, f.AddrMsg)
	default:
		add("住所", h1Addr, false, nil, f.PersonMsg)
	}
	// 認定情報
	if f.Cert != nil {
		if f.Level > 0 {
			add("要介護度", h1Level, false, &hyo1Val{kind: "n", num: f.Level, show: "要介護" + strconv.Itoa(f.Level)}, "")
		} else {
			add("要介護度", h1Level, false, nil, f.LevelMsg)
		}
		for _, x := range []struct {
			label, addr string
			t           time.Time
			ok          bool
		}{{"認定日", h1Cert, f.Cert.Cert, f.Cert.CertOK}, {"認定の有効期間（始）", h1From, f.Cert.Start, f.Cert.StartOK}, {"認定の有効期間（終）", h1To, f.Cert.End, f.Cert.EndOK}} {
			if x.ok {
				add(x.label, x.addr, true, dateVal(x.t), "")
			} else {
				add(x.label, x.addr, true, nil, "CSVの日付を読めませんでした")
			}
		}
		if f.Valid {
			add("認定済・申請中", h1Status, false, &hyo1Val{kind: "s", text: "認定済", show: "認定済"}, "")
		} else {
			add("認定済・申請中", h1Status, false, nil, f.ValidMsg)
		}
		if !f.Valid {
			pl.Warnings = append(pl.Warnings, f.ValidMsg+"（認定情報のCSVは、有効期間が新しい行を使っています）")
		}
		if f.LevelMsg != "" {
			pl.Warnings = append(pl.Warnings, f.LevelMsg)
		}
	} else {
		for _, x := range [][2]string{{"要介護度", h1Level}, {"認定日", h1Cert}, {"認定の有効期間（始）", h1From}, {"認定の有効期間（終）", h1To}, {"認定済・申請中", h1Status}} {
			add(x[0], x[1], x[0] != "要介護度" && x[0] != "認定済・申請中", nil, f.CertMsg)
		}
	}
	// 日付（空なら今日）
	td := dateVal(today)
	add("作成年月日", h1Created, true, td, "")
	add("計画作成（変更）日", h1PlanDate, true, td, "")
	// 初回居宅サービス計画作成日
	if txt, _ := h1Raw(doc.sheet, h1First, doc.ss); txt != "" {
		add("初回居宅サービス計画作成日", h1First, true, nil, "")
	} else {
		pv, from, why := s.h1PrevFirst(user)
		if pv != nil {
			pv.show += "（サーバーの前回の第1表「" + from + "」から）"
			add("初回居宅サービス計画作成日", h1First, true, pv, "")
		} else {
			add("初回居宅サービス計画作成日", h1First, true, nil, why)
			pl.Warnings = append(pl.Warnings, why)
		}
	}
	add("説明・同意日", h1Agree, true, td, "")

	pl.writes = writes
	pl.NWrite = len(writes)
	return pl, nil
}

func firstNonEmpty(a ...string) string {
	for _, x := range a {
		if x != "" {
			return x
		}
	}
	return ""
}

// ---------------- 書く・確かめる ----------------

var reH1Formula = regexp.MustCompile(`(?s)<c r="([A-Z]+\d+)"([^>]*)><f>(.*?)</f>(?:<v>.*?</v>|<v/>)?</c>`)
var reH1Check = regexp.MustCompile(`^IF\(\$([A-Z]+)\$(\d+)&""="([^"]*)","☑","☐"\)&"(.*)"$`)

// h1ApplyWrites: 値を書き、選択セルに合わせて ☑☐の式のキャッシュを直す。触ったセルの一覧も返す
func h1ApplyWrites(sheet string, ss []string, writes []hyo1Write) (string, []string, error) {
	touched := []string{}
	var err error
	for _, w := range writes {
		if w.kind == "n" {
			sheet, err = setCellXML(sheet, w.addr, "", "<v>"+strconv.Itoa(w.num)+"</v>")
		} else {
			sheet, err = setCellXML(sheet, w.addr, "inlineStr", inlineStrXML(w.text))
		}
		if err != nil {
			return "", nil, fmt.Errorf("%s に書けませんでした：%v", w.addr, err)
		}
		touched = append(touched, w.addr)
	}
	sel := map[string]bool{}
	for _, w := range writes {
		if w.addr == h1Level || w.addr == h1Status {
			sel[w.addr] = true
		}
	}
	if len(sel) > 0 {
		sheet = reH1Formula.ReplaceAllStringFunc(sheet, func(c string) string {
			m := reH1Formula.FindStringSubmatch(c)
			fm := reH1Check.FindStringSubmatch(html.UnescapeString(m[3]))
			if fm == nil || !sel[fm[1]+fm[2]] {
				return c
			}
			cur, _ := h1Raw(sheet, fm[1]+fm[2], ss)
			mark := "☐"
			if cur == fm[3] {
				mark = "☑"
			}
			attrs := regexp.MustCompile(`\s+t="[^"]*"`).ReplaceAllString(m[2], "")
			touched = append(touched, m[1])
			return `<c r="` + m[1] + `"` + attrs + ` t="str"><f>` + m[3] + `</f><v>` + encodeText(mark+fm[4]) + `</v></c>`
		})
	}
	return sheet, touched, nil
}

// h1CellElem: セル（<c r="X" …/> または <c r="X" …>…</c>）の位置
func h1CellElem(sheet, addr string) (int, int) {
	re := regexp.MustCompile(`(?s)<c r="` + addr + `"(?:\s[^>]*?)?(?:/>|>.*?</c>)`)
	loc := re.FindStringIndex(sheet)
	if loc == nil {
		return -1, -1
	}
	return loc[0], loc[1]
}

// verifyHyo1: 書いた結果を確かめる（だめなら何も書かない）
//   - 第1表のシート以外の部品が、バイトのまま同じ
//   - 触ったセルだけを元に戻すと、シートが元とまったく同じ
//   - 書いた値が読み返せる
//   - すべてのXMLが読める
func verifyHyo1(oldData, newData []byte, target string, writes []hyo1Write, touched []string) error {
	po, err := readPkg(oldData)
	if err != nil {
		return err
	}
	pn, err := readPkg(newData)
	if err != nil {
		return fmt.Errorf("書いたファイルをExcelとして開けません")
	}
	if len(po.entries) != len(pn.entries) {
		return fmt.Errorf("部品の数が変わりました")
	}
	for i, e := range po.entries {
		n := pn.entries[i]
		if e.name != n.name {
			return fmt.Errorf("部品の並びが変わりました（%s）", e.name)
		}
		if e.name == target {
			continue
		}
		a, _ := po.get(e.name)
		b, _ := pn.get(n.name)
		if a != b {
			return fmt.Errorf("書くはずのない部品（%s）が変わりました", e.name)
		}
	}
	xo, _ := po.get(target)
	xn, _ := pn.get(target)
	rest := xn
	for _, a := range touched {
		s0, e0 := h1CellElem(xo, a)
		s1, e1 := h1CellElem(rest, a)
		if s0 < 0 || s1 < 0 {
			return fmt.Errorf("セル %s を確かめられません", a)
		}
		rest = rest[:s1] + xo[s0:e0] + rest[e1:]
	}
	if rest != xo {
		return fmt.Errorf("書くはずのないセルが変わりました")
	}
	ss := sharedStrings(pn)
	for _, w := range writes {
		txt, _ := h1Raw(xn, w.addr, ss)
		want := w.text
		if w.kind == "n" {
			want = strconv.Itoa(w.num)
		}
		if txt != want {
			return fmt.Errorf("書いた値（%s）を読み返せません", w.addr)
		}
	}
	return checkPkgXML(newData)
}

// buildHyo1Book: 第1表のシートだけ差し替えた新しいファイルを作る
func buildHyo1Book(orig []byte, writes []hyo1Write) ([]byte, error) {
	doc, err := parseHyo1(orig)
	if err != nil {
		return nil, err
	}
	sheet, touched, err := h1ApplyWrites(doc.sheet, doc.ss, writes)
	if err != nil {
		return nil, err
	}
	doc.pkg.set(doc.target, sheet)
	out, err := doc.pkg.bytes()
	if err != nil {
		return nil, err
	}
	if err := verifyHyo1(orig, out, doc.target, writes, touched); err != nil {
		return nil, err
	}
	// 元の部品を圧縮したまま写していること（変えない部品のバイトが同じ）
	zo, _ := zip.NewReader(bytes.NewReader(orig), int64(len(orig)))
	zn, _ := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	for i, f := range zo.File {
		if f.Name != doc.target && f.CRC32 != zn.File[i].CRC32 {
			return nil, fmt.Errorf("変えない部品（%s）が変わりました", f.Name)
		}
	}
	return out, nil
}

// ---------------- API ----------------

func (s *panelServer) hyo1Preview(r *http.Request) (any, error) {
	var q struct{ Folder, Name string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	pl, err := s.buildHyo1Plan(q.Folder, q.Name)
	if err != nil {
		return nil, err
	}
	if s.hyo1Plans == nil {
		s.hyo1Plans = map[string]*hyo1Plan{}
	}
	for k, v := range s.hyo1Plans {
		if time.Since(v.created) > planLifetime {
			delete(s.hyo1Plans, k)
		}
	}
	s.hyo1Plans[pl.ID] = pl
	return pl, nil
}

func (s *panelServer) hyo1Apply(r *http.Request) (any, error) {
	var q struct{ ID string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	pl := s.hyo1Plans[q.ID]
	if pl == nil || time.Since(pl.created) > planLifetime {
		return nil, fmt.Errorf("確認の画面が古くなりました。もう一度「カイポケの情報を第1表に入れる」の画面を開いてください（まだ何も書いていません）")
	}
	delete(s.hyo1Plans, q.ID) // 1回だけ使える
	if pl.Blocked != "" {
		return nil, fmt.Errorf("%s", pl.Blocked)
	}
	if len(pl.writes) == 0 {
		return nil, fmt.Errorf("入れる欄がありません（まだ何も書いていません）")
	}
	if filepath.Dir(pl.srcPath) != s.p.out || !strings.EqualFold(filepath.Ext(pl.srcPath), ".xlsx") {
		return nil, fmt.Errorf("書く先が正しくありません（まだ何も書いていません）")
	}
	if officeOpen(pl.srcPath) {
		return nil, fmt.Errorf("%s がExcelで開かれています。閉じてから、もう一度押してください（まだ何も書いていません）", pl.Source)
	}
	if fileSig(pl.srcPath) != pl.srcSig {
		return nil, fmt.Errorf("確認の画面のあとで、%s が変わりました。もう一度開き直してください（まだ何も書いていません）", pl.Source)
	}
	orig, err := os.ReadFile(pl.srcPath)
	sum := sha256.Sum256(orig)
	if err != nil || hex.EncodeToString(sum[:]) != pl.srcHash {
		return nil, fmt.Errorf("確認の画面のあとで、%s が変わりました。もう一度開き直してください（まだ何も書いていません）", pl.Source)
	}
	// 中身を先に作って確かめる（だめなら何も書かない）
	newData, err := buildHyo1Book(orig, pl.writes)
	if err != nil {
		return nil, fmt.Errorf("入れる中身を作れませんでした（まだ何も書いていません）：%v", err)
	}
	// 控え（2_完成＼控え。消さない・上書きしない）
	bdir := filepath.Join(s.p.out, keikaBackupDir)
	if err := os.MkdirAll(bdir, 0700); err != nil {
		return nil, fmt.Errorf("「控え」のフォルダを作れませんでした（まだ何も書いていません）：%v", err)
	}
	stamp := time.Now().Format("20060102_150405")
	bp, err := writeNoDelete(bdir, stamp+"_"+pl.Source, orig)
	if err != nil || !sameFile(bp, orig) {
		return nil, fmt.Errorf("控えを正しく取れませんでした。%s は書き換えていません", pl.Source)
	}
	if officeOpen(pl.srcPath) || fileSig(pl.srcPath) != pl.srcSig {
		return nil, fmt.Errorf("書く直前に %s が開かれたか変わったため、書きませんでした（控えは取ってあります）", pl.Source)
	}
	if err := replaceFile(pl.srcPath, newData); err != nil {
		return nil, fmt.Errorf("%s に入れ替えられませんでした（Excelで開いていたら閉じてください）。元のファイルはそのままです：%v", pl.Source, err)
	}
	nOK := 0
	for _, rw := range pl.Rows {
		if rw.State == "入れる" {
			nOK++
		}
	}
	return map[string]any{
		"ok":     "第1表（" + pl.Source + "）に、" + strconv.Itoa(nOK) + "つの欄を入れました。",
		"backup": keikaBackupDir + "＼" + filepath.Base(bp),
		"n":      nOK,
	}, nil
}
