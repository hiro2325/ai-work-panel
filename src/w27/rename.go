package main

// サーバーの名前をそろえる（改善29 No.58・R8.10.7 承認）
//
//   - サーバーの利用者フォルダ（50音の行フォルダの中・利用者（終了）も）を見て、今の決まりと違う名前の
//     ケアプランのブック・「元にした資料_…」フォルダ・支援経過1日分のExcelを一覧にする（見るだけ。何も書かない）。
//   - 名前を変えるのは、画面でチェックを付けた行だけ。移さない・消さない・上書きしない
//     （同じ名前があれば変えずに知らせる。Excelで開いているものは変えない）。
//   - 変える前に、利用者フォルダの「控え」に前後の名前を日時つきで書き残す。
//   - ケアプランのブックの新しい名前：「作成日（適用開始日～理由）.xlsx」（例：R8.9.30（R8.10.1～新規）.xlsx）。
//     作成日は2表の作成年月日、適用開始日は3表の適用期間の始め（なければ2表の短期目標の期間の始め）をExcelの中から読む。
//     理由は今の名前から（新規・認定更新・区分変更・サービス変更）。分からなければ空けて「要確認」にする。

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	rnBackupRecord = "名前をそろえた記録"
	rnPlanRecord   = "名前をそろえる前の記録"
	rnMaxRows      = 3000
	rnMaxReads     = 3000
)

type rnRow struct {
	ID       int    `json:"id"`
	Kind     string `json:"kind"` // book（ケアプランのブック）／src（元にした資料_…）／keika（支援経過1日分。知らせるだけ）
	Root     string `json:"root"` // base／ended
	Rel      string `json:"rel"`  // 置き場所からの利用者フォルダ（/ 区切り）
	Sub      string `json:"sub"`  // 利用者フォルダの中のフォルダ（なければ空）
	From     string `json:"from"`
	To       string `json:"to"`
	Note     string `json:"note,omitempty"`
	Checked  bool   `json:"checked"`
	Editable bool   `json:"editable"`
	Blocked  bool   `json:"blocked"` // 変えられない（同じ名前がある・案が作れない）
	Where    string `json:"where"`   // 画面に出す場所
	Pre      string `json:"pre,omitempty"` // 理由が分からないとき：理由の前までの名前（画面で理由を選ぶと「Pre＋理由＋）.xlsx」になる）
}

var reBookNameOK = regexp.MustCompile(`^R\d{1,2}\.\d{1,2}\.\d{1,2}（R\d{1,2}\.\d{1,2}\.\d{1,2}～[^（）]+）\.xlsx$`)

var rnReasons = []string{"サービス変更", "区分変更", "認定更新", "新規"}

func rnReasonOf(name string) string {
	for _, r := range rnReasons {
		if strings.Contains(name, r) {
			return r
		}
	}
	return ""
}

// ---- Excelの中から日付を読む（読むだけ） ----

var reFlexWareki = regexp.MustCompile(`^R(\d{1,2})\.(\d{1,2})\.(\d{1,2})$`)
var reFlexKanji = regexp.MustCompile(`^令和(元|\d{1,2})年(\d{1,2})月(\d{1,2})日?$`)
var reFlexSlash = regexp.MustCompile(`^(\d{4})[/\-.](\d{1,2})[/\-.](\d{1,2})$`)
var reFlexSerial = regexp.MustCompile(`^(\d{5})(\.\d+)?$`)

func parseFlexDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(toHalfDigits(s))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "　", "")
	if m := reFlexWareki.FindStringSubmatch(s); m != nil {
		return mkDate(atoi(m[1])+2018, atoi(m[2]), atoi(m[3]))
	}
	if m := reFlexKanji.FindStringSubmatch(s); m != nil {
		y := 1
		if m[1] != "元" {
			y = atoi(m[1])
		}
		return mkDate(y+2018, atoi(m[2]), atoi(m[3]))
	}
	if m := reFlexSlash.FindStringSubmatch(s); m != nil {
		return mkDate(atoi(m[1]), atoi(m[2]), atoi(m[3]))
	}
	if m := reFlexSerial.FindStringSubmatch(s); m != nil {
		n := atoi(m[1])
		if n >= 40000 && n <= 80000 {
			return serialDate(n), true
		}
	}
	return time.Time{}, false
}

func rnColName(n int) string {
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}

func rnColNum(s string) int {
	n := 0
	for _, c := range s {
		n = n*26 + int(c-'A'+1)
	}
	return n
}

var rnLabelWords = map[string]bool{"令和": true, "年": true, "月": true, "日": true, "作成年月日": true, "適用期間": true, "～": true, "〜": true, "~": true}

// rnRowDate: 行の中（列 fromCol〜toCol）に書かれた日付。「令和 8 年 9 月 30 日」と数字が別々のセルでも、1つのセルでも読む
func rnRowDate(sheet string, row int, fromCol, toCol string, ss []string) (time.Time, bool) {
	var toks []string
	for c := rnColNum(fromCol); c <= rnColNum(toCol); c++ {
		t := strings.TrimSpace(kxCell(sheet, fmt.Sprintf("%s%d", rnColName(c), row), ss))
		if t == "" || rnLabelWords[t] {
			continue
		}
		toks = append(toks, t)
	}
	if len(toks) == 1 {
		return parseFlexDate(toks[0])
	}
	if len(toks) == 3 {
		ok := true
		var n [3]int
		for i, t := range toks {
			t = toHalfDigits(t)
			if i == 0 && t == "元" {
				n[0] = 1
				continue
			}
			if f := strings.TrimSuffix(t, ".0"); regexp.MustCompile(`^\d{1,2}$`).MatchString(f) {
				n[i] = atoi(f)
			} else {
				ok = false
			}
		}
		if ok {
			return mkDate(n[0]+2018, n[1], n[2])
		}
	}
	return time.Time{}, false
}

type bookDates struct {
	isBook         bool
	created, start time.Time
	okC, okS       bool
}

func rnZipRead(zr *zip.ReadCloser, name string, limit int64) (string, bool) {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", false
		}
		b, err := io.ReadAll(io.LimitReader(rc, limit))
		rc.Close()
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return "", false
}

func rnSharedStrings(x string) []string {
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

// readBookDates: ケアプランのブック（シート「2表」「3表」がある .xlsx）か確かめ、作成日・適用開始日を読む。書かない
func readBookDates(path string) bookDates {
	var bd bookDates
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 200<<20 {
		return bd
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return bd
	}
	defer zr.Close()
	wb, ok := rnZipRead(zr, "xl/workbook.xml", 4<<20)
	if !ok {
		return bd
	}
	rels, _ := rnZipRead(zr, "xl/_rels/workbook.xml.rels", 4<<20)
	rm := relsMap(rels)
	target := map[string]string{}
	for _, t := range reSheetTag.FindAllString(wb, -1) {
		target[attr(t, "name")] = resolveTarget("xl", rm[attr(t, "r:id")][1])
	}
	t2, ok2 := target["2表"]
	t3, ok3 := target["3表"]
	if !ok2 || !ok3 {
		return bd
	}
	bd.isBook = true
	ssx, _ := rnZipRead(zr, "xl/sharedStrings.xml", 64<<20)
	ss := rnSharedStrings(ssx)
	s2, _ := rnZipRead(zr, t2, 32<<20)
	s3, _ := rnZipRead(zr, t3, 32<<20)
	bd.created, bd.okC = rnRowDate(s2, 1, "AF", "AU", ss)
	bd.start, bd.okS = rnRowDate(s3, 2, "AJ", "AT", ss)
	if !bd.okS {
		// 2表の短期目標の期間（S列）の始め。いちばん早いもの
		for r := 8; r <= 79 && s2 != ""; r++ {
			if t, ok := parseFlexDate(kxCell(s2, fmt.Sprintf("S%d", r), ss)); ok {
				if !bd.okS || t.Before(bd.start) {
					bd.start, bd.okS = t, true
				}
			}
		}
	}
	return bd
}

// ---- 見つける ----

func rnUserDirs(root string) []string {
	var out []string
	ents, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	skip := func(n string) bool {
		return strings.HasPrefix(n, ".") || strings.HasPrefix(n, "$") || strings.HasPrefix(n, "~")
	}
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() || skip(n) {
			continue
		}
		if utf8.RuneCountInString(n) == 1 {
			sub, err := os.ReadDir(filepath.Join(root, n))
			if err != nil {
				continue
			}
			for _, e2 := range sub {
				if e2.IsDir() && !skip(e2.Name()) {
					out = append(out, n+"/"+e2.Name())
				}
			}
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func rnSkipDir(n string) bool {
	return n == keikaBackupDir || n == srcRootName || strings.HasPrefix(n, srcOldPrefix) || strings.HasPrefix(n, ".")
}

func rnPlainFile(e os.DirEntry, dir string) bool {
	n := e.Name()
	if e.IsDir() || strings.HasPrefix(n, "~$") || strings.HasPrefix(n, ".") || strings.HasSuffix(n, ".part") {
		return false
	}
	st, err := os.Lstat(filepath.Join(dir, n))
	return err == nil && st.Mode().IsRegular()
}

func (s *panelServer) renameScan(r *http.Request) (any, error) {
	st := s.serverSettings()
	type rootT struct{ key, path, label string }
	roots := []rootT{{"base", st.Base, "利用者"}, {"ended", st.Ended, "利用者（終了）"}}
	rows := []*rnRow{}
	notes := []string{}
	reads := 0
	found := false
	for _, rt := range roots {
		if !dirExists(rt.path) {
			notes = append(notes, "「"+rt.label+"」のフォルダが見つかりません（つながっていない・置き場所が違うかもしれません）："+rt.path)
			continue
		}
		found = true
		for _, rel := range rnUserDirs(rt.path) {
			ud := filepath.Join(rt.path, filepath.FromSlash(rel))
			where := rt.label + "＼" + strings.ReplaceAll(rel, "/", "＼")
			add := func(sub string, row *rnRow) {
				row.Root, row.Rel, row.Sub, row.Where = rt.key, rel, sub, where
				if sub != "" {
					row.Where += "＼" + sub
				}
				rows = append(rows, row)
			}
			// 元にした資料_…（利用者フォルダのいちばん上）
			mv, prob := oldSrcMoves(ud)
			for _, m := range mv {
				row := &rnRow{Kind: "src", From: m.From, To: srcRootName + "＼" + m.To, Checked: !m.Conflict, Editable: false, Blocked: m.Conflict}
				if m.Conflict {
					row.Note = "「元にした資料」の中に同じ名前があるので、移しません"
					if prob != "" {
						row.Note = prob
					}
				} else {
					row.Note = "「元にした資料」フォルダの中へ移します（名前の変更だけ。中は触りません）"
				}
				add("", row)
			}
			// 支援経過1日分（まとめの対象を知らせるだけ）
			for _, n := range dayXlsxFiles(ud) {
				add("", &rnRow{Kind: "keika", From: n, To: bookNameFor(ud, ""), Note: "「支援経過_氏名.xlsx」にまとめる対象です。まとめるのは、サーバーへ保存のとき（ここでは名前を変えません）"})
			}
			// ケアプランのブック（利用者フォルダのいちばん上と、その中のフォルダ1段）
			scanDir := func(dir, sub string) {
				ents, _ := os.ReadDir(dir)
				var local []*rnRow
				for _, e := range ents {
					if !rnPlainFile(e, dir) || !strings.EqualFold(filepath.Ext(e.Name()), ".xlsx") {
						continue
					}
					n := e.Name()
					if reBookNameOK.MatchString(n) || strings.HasPrefix(n, keikaBookPrefix) || strings.HasPrefix(n, dayFilePrefix) {
						continue
					}
					if reads >= rnMaxReads || len(rows) >= rnMaxRows {
						continue
					}
					reads++
					bd := readBookDates(filepath.Join(dir, n))
					if !bd.isBook {
						continue
					}
					row := &rnRow{Kind: "book", From: n, Editable: true}
					reason := rnReasonOf(n)
					var why []string
					switch {
					case !bd.okC:
						why = append(why, "作成日（2表の作成年月日）を読めません")
					default:
						if !bd.okS {
							why = append(why, "適用開始日（3表の適用期間・2表の短期目標の期間）を読めません")
						}
						if reason == "" {
							why = append(why, "理由（新規・認定更新・区分変更・サービス変更）が今の名前から分かりません")
						}
						sd := ""
						if bd.okS {
							sd = warekiDate(bd.start)
						}
						row.To = warekiDate(bd.created) + "（" + sd + "～" + reason + "）.xlsx"
						if reason == "" {
							row.Pre = warekiDate(bd.created) + "（" + sd + "～"
						}
					}
					if len(why) > 0 {
						row.Note = "要確認：" + strings.Join(why, "／") + "。新しい名前を入れてから、チェックを付けてください"
					} else {
						row.Checked = true
					}
					local = append(local, row)
				}
				// 同じ新しい名前が2つ・すでに同じ名前がある
				cnt := map[string]int{}
				for _, rw := range local {
					cnt[rw.To]++
				}
				for _, rw := range local {
					if rw.To == "" {
						rw.Blocked = true
					} else if _, err := os.Lstat(filepath.Join(dir, rw.To)); err == nil {
						rw.Blocked, rw.Checked = true, false
						rw.Note = "同じ名前のファイルがあります。案の名前を少し変えて（例：末尾に②）から、そろえてください"
					} else if cnt[rw.To] > 1 {
						rw.Checked = false
						rw.Note = "要確認：同じ新しい名前になるファイルが2つあります。同じ名前のファイルがあります。案の名前を少し変えて（例：末尾に②）から、そろえてください"
					}
					add(sub, rw)
				}
			}
			scanDir(ud, "")
			if ents, err := os.ReadDir(ud); err == nil {
				for _, e := range ents {
					if e.IsDir() && !rnSkipDir(e.Name()) {
						scanDir(filepath.Join(ud, e.Name()), e.Name())
					}
				}
			}
		}
	}
	for i, rw := range rows {
		rw.ID = i + 1
	}
	if !found {
		return nil, fmt.Errorf("サーバーの利用者フォルダが見つかりません（つながっていない・置き場所が違うかもしれません）。何も変えていません")
	}
	return map[string]any{"rows": rows, "notes": notes, "truncated": reads >= rnMaxReads || len(rows) >= rnMaxRows}, nil
}

// ---- 名前を変える ----

type rnReq struct {
	Rows []struct {
		Kind string `json:"kind"`
		Root string `json:"root"`
		Rel  string `json:"rel"`
		Sub  string `json:"sub"`
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"rows"`
}

type rnResult struct {
	From string `json:"from"`
	To   string `json:"to"`
	Dir  string `json:"dir"`
	Why  string `json:"why,omitempty"`
}

func rnBadName(n string) string {
	n = strings.TrimSpace(n)
	switch {
	case n == "":
		return "新しい名前が空です"
	case strings.ContainsAny(n, "\\/:*?\"<>|\r\n\t") || strings.Contains(n, ".."):
		return "名前に使えない文字（\\ / : * ? \" < > |）が入っています"
	case strings.HasPrefix(n, ".") || strings.HasPrefix(n, "~$"):
		return "名前の最初に使えない文字があります"
	case strings.Contains(n, "～）") || strings.Contains(n, "（～"):
		return "理由（または適用開始日）が空の名前（…～）.xlsx）には変えません。理由を選ぶか書いてください"
	case !strings.EqualFold(filepath.Ext(n), ".xlsx"):
		return "名前の終わりは .xlsx にしてください"
	case utf8.RuneCountInString(n) > 120:
		return "名前が長すぎます"
	case strings.HasSuffix(strings.TrimSuffix(n, filepath.Ext(n)), " ") || strings.HasSuffix(strings.TrimSuffix(n, filepath.Ext(n)), "."):
		return "名前の終わり（.xlsx の前）に空白・ドットは使えません"
	}
	return ""
}

func (s *panelServer) renameDo(r *http.Request) (any, error) {
	var q rnReq
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	if len(q.Rows) == 0 {
		return nil, fmt.Errorf("名前を変える行にチェックを付けてください（何も変えていません）")
	}
	st := s.serverSettings()
	now := time.Now()
	done, skipped := []rnResult{}, []rnResult{}
	type job struct{ from, to, kind, sub string }
	byDir := map[string][]job{} // 利用者フォルダ → 行
	userDirs := map[string]string{}
	var order []string
	for _, rw := range q.Rows {
		base := ""
		switch rw.Root {
		case "base":
			base = st.Base
		case "ended":
			base = st.Ended
		}
		fail := func(why string) {
			skipped = append(skipped, rnResult{From: rw.From, To: rw.To, Dir: rw.Rel, Why: why})
		}
		if base == "" || !dirExists(base) {
			fail("サーバーのフォルダが見つかりません")
			continue
		}
		rel := filepath.FromSlash(rw.Rel)
		ud := filepath.Join(base, rel)
		if rw.Rel == "" || strings.Contains(rw.Rel, "..") || !strictlyWithin(base, ud) || strings.Count(rw.Rel, "/") > 1 || !dirExists(ud) {
			fail("利用者フォルダが見つかりません")
			continue
		}
		if rw.From == "" || rw.From != filepath.Base(rw.From) || strings.Contains(rw.Sub, "/") || strings.Contains(rw.Sub, `\`) || strings.Contains(rw.Sub, "..") || rnSkipDir(rw.Sub) {
			fail("名前の指定が正しくありません")
			continue
		}
		key := rw.Root + "|" + rw.Rel
		if _, ok := userDirs[key]; !ok {
			userDirs[key] = ud
			order = append(order, key)
		}
		byDir[key] = append(byDir[key], job{rw.From, strings.TrimSpace(rw.To), rw.Kind, rw.Sub})
	}
	for _, key := range order {
		ud := userDirs[key]
		jobs := byDir[key]
		where := strings.SplitN(key, "|", 2)[1]
		// 元にした資料
		var srcMoves []srvMove
		type okJob struct{ dir, from, to string }
		var books []okJob
		for _, j := range jobs {
			switch j.kind {
			case "src":
				srcMoves = append(srcMoves, srvMove{From: j.from, To: strings.TrimPrefix(j.from, srcOldPrefix)})
			case "book":
				dir := ud
				if j.sub != "" {
					dir = filepath.Join(ud, j.sub)
				}
				fail := func(why string) {
					skipped = append(skipped, rnResult{From: j.from, To: j.to, Dir: where, Why: why})
				}
				if !strictlyWithin(ud, dir) && dir != ud {
					fail("フォルダの場所が正しくありません")
					continue
				}
				if why := rnBadName(j.to); why != "" {
					fail(why)
					continue
				}
				if j.to == j.from {
					fail("名前が今のままです")
					continue
				}
				src := filepath.Join(dir, j.from)
				if fi, err := os.Lstat(src); err != nil || !fi.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(j.from), ".xlsx") {
					fail("元のファイルが見つかりません（もう変わっているかもしれません）")
					continue
				}
				if !readBookDates(src).isBook {
					fail("ケアプランのブックではないので、変えません")
					continue
				}
				if xlsmLocked(dir, j.from) {
					fail("Excelで開いているので、変えません（閉じてからもう一度）")
					continue
				}
				if _, err := os.Lstat(filepath.Join(dir, j.to)); err == nil {
					fail("同じ名前のファイルがもうあるので、変えません")
					continue
				}
				books = append(books, okJob{dir, j.from, j.to})
			default:
				skipped = append(skipped, rnResult{From: j.from, To: j.to, Dir: where, Why: "ここでは名前を変えない種類です"})
			}
		}
		if len(srcMoves) > 0 {
			mv, sk := moveOldSrcDirs(ud, srcMoves, now)
			for _, m := range mv {
				done = append(done, rnResult{From: m.From, To: srcRootName + "＼" + m.To, Dir: where})
			}
			for _, m := range sk {
				skipped = append(skipped, rnResult{From: m.From, To: srcRootName + "＼" + m.To, Dir: where, Why: "「元にした資料」の中に同じ名前があるなどで、移しませんでした"})
			}
		}
		if len(books) == 0 {
			continue
		}
		bdir := filepath.Join(ud, keikaBackupDir)
		stamp := now.Format("20060102_150405")
		var plan strings.Builder
		plan.WriteString("サーバーの名前をそろえます（" + now.Format("2006-01-02 15:04:05") + "）。名前の変更だけで、中身は触りません。\r\n\r\n")
		for _, b := range books {
			plan.WriteString("変える前：" + relFrom(ud, b.dir, b.from) + "\r\n変えたあと（予定）：" + relFrom(ud, b.dir, b.to) + "\r\n\r\n")
		}
		if err := os.MkdirAll(bdir, 0755); err != nil {
			for _, b := range books {
				skipped = append(skipped, rnResult{From: b.from, To: b.to, Dir: where, Why: "「控え」フォルダを作れないので、変えません"})
			}
			continue
		}
		if _, err := writeNoDelete(bdir, stamp+"_"+rnPlanRecord+".txt", []byte(bom+plan.String())); err != nil {
			for _, b := range books {
				skipped = append(skipped, rnResult{From: b.from, To: b.to, Dir: where, Why: "「控え」に記録を書けないので、変えません"})
			}
			continue
		}
		var res strings.Builder
		res.WriteString("名前をそろえました（" + now.Format("2006-01-02 15:04:05") + "）。元に戻すときは、「変えたあと」の名前を「変える前」の名前に直してください。\r\n\r\n")
		nres := 0
		for _, b := range books {
			if _, err := os.Lstat(filepath.Join(b.dir, b.to)); err == nil {
				skipped = append(skipped, rnResult{From: b.from, To: b.to, Dir: where, Why: "同じ名前のファイルがもうあるので、変えません"})
				continue
			}
			if err := os.Rename(filepath.Join(b.dir, b.from), filepath.Join(b.dir, b.to)); err != nil {
				skipped = append(skipped, rnResult{From: b.from, To: b.to, Dir: where, Why: "名前を変えられませんでした（Excelで開いているかもしれません）"})
				continue
			}
			done = append(done, rnResult{From: b.from, To: b.to, Dir: where})
			res.WriteString("変える前：" + relFrom(ud, b.dir, b.from) + "\r\n変えたあと：" + relFrom(ud, b.dir, b.to) + "\r\n\r\n")
			nres++
		}
		if nres > 0 {
			if _, err := writeNoDelete(bdir, stamp+"_"+rnBackupRecord+".txt", []byte(bom+res.String())); err != nil {
				skipped = append(skipped, rnResult{From: "（記録）", Dir: where, Why: "名前は変えましたが、結果の記録を書けませんでした。変える前の記録は「控え」にあります"})
			}
		}
	}
	msg := fmt.Sprintf("%d件の名前をそろえました", len(done))
	if len(skipped) > 0 {
		msg += fmt.Sprintf("（変えなかったもの %d件）", len(skipped))
	}
	return map[string]any{"ok": msg, "done": done, "skipped": skipped}, nil
}

func relFrom(ud, dir, name string) string {
	if dir == ud {
		return name
	}
	return filepath.Base(dir) + "＼" + name
}
