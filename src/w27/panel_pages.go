package main

// AI作業パネル：下の「そのほか」のページ（氏名一覧・00_指示・原本・引き継ぎメモ・使い方・作業フォルダ）と、
// 文字起こしの貼り付け。どれも読むのは決まったフォルダの中だけで、AI作業の側には何も書かない。

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxViewBytes = 2 << 20 // 画面で読む1ファイルの上限

type fileEntry struct {
	Name  string `json:"name"`
	MTime string `json:"mtime"`
	Size  int64  `json:"size"`
}

func fmtTime(t time.Time) string { return t.Format("2006/01/02 15:04") }

// safeName: フォルダの外を指さない「ファイル名1つ」だけを通す
func safeName(name string) bool {
	return name != "" && name == filepath.Base(name) && name != "." && name != ".." &&
		!strings.HasPrefix(name, ".") && !strings.ContainsAny(name, `/\:`) && !strings.Contains(name, "..")
}

// safeRel: 「ファイル名」または「フォルダ名/ファイル名」（2段まで）だけを通し、root の中のパスを返す
func safeRel(root, rel string) (string, bool) {
	parts := strings.Split(rel, "/")
	if len(parts) == 0 || len(parts) > 2 {
		return "", false
	}
	for _, p := range parts {
		if !safeName(p) {
			return "", false
		}
	}
	full := filepath.Join(append([]string{root}, parts...)...)
	r, err := filepath.Rel(root, full)
	if err != nil || strings.HasPrefix(r, "..") || filepath.IsAbs(r) {
		return "", false
	}
	return full, true
}

func readSmallText(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return "", fmt.Errorf("見つかりません：%s", filepath.Base(path))
	}
	if st.Size() > maxViewBytes {
		return "", fmt.Errorf("大きすぎて画面に出せません：%s", filepath.Base(path))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("読めませんでした：%s", filepath.Base(path))
	}
	s := strings.TrimPrefix(string(b), bom)
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "？")
	}
	return strings.ReplaceAll(s, "\r\n", "\n"), nil
}

func entriesIn(dir string) []fileEntry {
	out := []fileEntry{}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "~$") || strings.HasSuffix(n, ".part") || strings.HasSuffix(n, ".tmp") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, fileEntry{Name: n, MTime: fmtTime(info.ModTime()), Size: info.Size()})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// ================= 氏名一覧 =================

var nameKinds = map[string]bool{"利用者": true, "家族": true, "その他": true, "除外": true, addrKind: true}

func (s *panelServer) nameRowsWithCodes() ([]nameRow, error) {
	rows, err := readNameRows(s.p.nameList)
	if err != nil {
		return nil, err
	}
	// まだ記号が決まっていない人は、ここで決める（置き換えのときと同じ順・同じ決め方）
	if _, err := s.userList(); err != nil {
		return nil, err
	}
	cb := s.codeBook()
	for i, r := range rows {
		if r.Kind == "除外" {
			continue
		}
		if r.Kind == addrKind {
			rows[i].Code = addrCodesFor(cb, r.Name)
			continue
		}
		rows[i].Code = strings.Trim(cb.keyToCode["名|"+rowPrefix(r.Kind, r.Name)+"|"+r.Name], "【】")
		if a, _, _ := splitAddrAliases(r.Alias); len(a) > 0 {
			rows[i].AddrAlias = a
		}
	}
	// 同じ区分・同じ氏名の行（二重登録）に印を付ける。消すのは人が「消す」を押したときだけ（改善5）
	cnt := map[string]int{}
	for _, r := range rows {
		cnt[r.Kind+"|"+normName(r.Name)]++
	}
	for i, r := range rows {
		if n := cnt[r.Kind+"|"+normName(r.Name)]; n > 1 {
			rows[i].Dup = n
		}
	}
	return rows, nil
}

func (s *panelServer) namesList(r *http.Request) (any, error) {
	rows, err := s.nameRowsWithCodes()
	if err != nil {
		return nil, err
	}
	gs := dupGroups(rows) // 重複をまとめる候補（改善22）
	if gs == nil {
		gs = []dupGroup{}
	}
	return map[string]any{"rows": rows, "groups": gs, "excelOpen": nameListOpenInExcel(s.p.nameList)}, nil
}

func cleanName(n string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(n, "　", " ")), " ")
}

func cleanAlias(a string) string {
	var parts []string
	for _, x := range strings.FieldsFunc(a, func(r rune) bool { return r == '/' || r == '／' || r == '、' || r == ',' || r == '，' }) {
		if x = strings.TrimSpace(x); x != "" {
			parts = append(parts, x)
		}
	}
	return strings.Join(parts, "／")
}

func aliasList(a string) []string {
	var out []string
	for _, x := range strings.FieldsFunc(a, func(r rune) bool { return r == '/' || r == '／' || r == '、' || r == ',' || r == '，' }) {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// checkNameInput: 区分・氏名・呼び名の書き方を確かめる。old は直す前（新しく足すときは nil）
func checkNameInput(kind, name, alias string, old *nameRow, rows []nameRow) error {
	if !nameKinds[kind] && (old == nil || kind != old.Kind) {
		return fmt.Errorf("区分は 利用者・家族・その他・除外 から選んでください")
	}
	if name == "" {
		return fmt.Errorf("氏名を書いてください")
	}
	if len([]rune(name)) > 40 || len([]rune(alias)) > 120 || strings.ContainsAny(name+alias, "\t\r\n【】") {
		return fmt.Errorf("書き方が正しくありません（長すぎる、または【】・改行が入っています）")
	}
	nameChanged := old == nil || name != old.Name || kind != old.Kind
	if nameChanged && (kind == "利用者" || kind == "家族") && !strings.Contains(name, " ") {
		return fmt.Errorf("利用者・家族は、姓と名の間にスペースを入れてください（例：大和 一郎）")
	}
	if kind == "除外" && alias != "" {
		return fmt.Errorf("除外の行には呼び名を書けません")
	}
	if kind == addrKind {
		if alias != "" {
			return fmt.Errorf("住所の行には呼び名を書けません（住所は人に結び付けません）")
		}
		if err := checkAddrWord(name); err != nil {
			return err
		}
		for _, r := range rows {
			if old != nil && r.Row == old.Row {
				continue
			}
			if r.Kind == addrKind && addrCanon(r.Name) == addrCanon(name) {
				return fmt.Errorf("住所「%s」は、すでに氏名一覧の %d 行目に登録されています", r.Name, r.Row)
			}
		}
	}
	for _, r := range rows {
		if old != nil && r.Row == old.Row {
			continue
		}
		if normName(r.Name) == normName(name) {
			return fmt.Errorf("「%s」は、すでに氏名一覧の %d 行目に登録されています（区分：%s）", r.Name, r.Row, r.Kind)
		}
	}
	return nil
}

type nameReq struct {
	Row   int
	Old   nameRow
	Kind  string
	Name  string
	Alias string
	Word  string
	// 重複行を1行消すとき、その行にしかない呼び名を移す先（同じ区分・同じ氏名のほかの行。改善25）
	Target nameRow
}

func (s *panelServer) namesAdd(r *http.Request) (any, error) {
	var q nameReq
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	q.Kind, q.Name, q.Alias = strings.TrimSpace(q.Kind), cleanName(q.Name), cleanAlias(q.Alias)
	if q.Kind == "除外" {
		q.Name = strings.TrimSpace(q.Name)
	}
	if q.Kind == addrKind {
		q.Name = cleanAddr(q.Name)
	}
	if nameListOpenInExcel(s.p.nameList) {
		return nil, fmt.Errorf("氏名一覧.xlsx がExcelで開かれています。Excelを閉じてから、もう一度押してください")
	}
	rows, err := readNameRows(s.p.nameList)
	if err != nil {
		return nil, err
	}
	if err := checkNameInput(q.Kind, q.Name, q.Alias, nil, rows); err != nil {
		return nil, err
	}
	if err := appendNameRow(s.p.nameList, q.Kind, q.Name, q.Alias); err != nil {
		return nil, err
	}
	code := s.codeOf(q.Kind, q.Name)
	msg := q.Kind + "「" + q.Name + "」を氏名一覧に追加しました"
	if code != "" {
		msg += "（記号：" + code + "）"
	}
	return map[string]string{"ok": msg}, nil
}

// addrCodesFor: 登録した住所に、これまでに付いた記号（書類ごとの書き方で番号が分かれることがある）
func addrCodesFor(cb *codeBook, name string) string {
	c := addrCanon(name)
	core := ""
	if loc := addrCoreLoc(name); loc != nil {
		core = addrCanon(name[loc[0]:loc[1]])
	}
	var out []string
	for _, k := range cb.order {
		if !strings.HasPrefix(k, "住|") {
			continue
		}
		if kc := addrCanon(cb.keyOrig[k]); kc == c || (kc == core && core != "") || addrCanon(strings.TrimPrefix(k, "住|")) == c {
			out = append(out, strings.Trim(cb.keyToCode[k], "【】"))
		}
	}
	if len(out) > 3 {
		out = append(out[:3], "…")
	}
	return strings.Join(out, "・")
}

func (s *panelServer) codeOf(kind, name string) string {
	if kind == "除外" || kind == addrKind {
		return ""
	}
	if _, err := s.userList(); err != nil {
		return ""
	}
	return strings.Trim(s.codeBook().keyToCode["名|"+rowPrefix(kind, name)+"|"+name], "【】")
}

// aliasCollision: 新しく足す呼び名が、前に別の呼び名で使った記号番号と重なるか
func aliasCollision(cb *codeBook, kind, name, alias string) bool {
	prefix := sanitizePrefix(kind)
	base := cb.keyToCode["名|"+prefix+"|"+name]
	if base == "" {
		return false
	}
	for i, a := range aliasList(alias) {
		if _, ok := cb.keyToCode["別|"+prefix+"|"+name+"|"+a]; ok {
			continue
		}
		code := strings.TrimSuffix(base, "】") + fmt.Sprintf("の呼び名%d】", i+1)
		if _, used := cb.codeToOrig[code]; used {
			return true
		}
	}
	return false
}

func (s *panelServer) namesEdit(r *http.Request) (any, error) {
	var q nameReq
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	q.Kind, q.Name, q.Alias = strings.TrimSpace(q.Kind), cleanName(q.Name), cleanAlias(q.Alias)
	if q.Old.Row < 2 {
		return nil, fmt.Errorf("直す行が正しくありません")
	}
	rows, err := readNameRows(s.p.nameList)
	if err != nil {
		return nil, err
	}
	if err := checkNameInput(q.Kind, q.Name, q.Alias, &q.Old, rows); err != nil {
		return nil, err
	}
	if q.Kind == q.Old.Kind && q.Name == q.Old.Name && q.Alias == q.Old.Alias {
		return map[string]string{"ok": "変更はありませんでした"}, nil
	}
	oldCode := s.codeOf(q.Old.Kind, q.Old.Name)
	collide := aliasCollision(s.codeBook(), q.Kind, q.Name, q.Alias)
	if err := modifyNameRow(s.p.nameList, q.Old, func(x string) (string, error) {
		return editNameRowXML(x, q.Old.Row, q.Kind, q.Name, q.Alias)
	}); err != nil {
		return nil, err
	}
	newCode := s.codeOf(q.Kind, q.Name)
	msg := "氏名一覧の " + fmt.Sprint(q.Old.Row) + " 行目を直しました（" + q.Kind + "「" + q.Name + "」"
	if q.Alias != "" {
		msg += "　呼び名：" + q.Alias
	}
	msg += "）。次の置き換えから、この内容が使われます"
	if oldCode != "" && newCode != "" && oldCode != newCode {
		msg += "。※氏名か区分を直したので、記号は " + newCode + " になります（前の記号 " + oldCode + " は記号対応表に残っているので、前に渡した書類も元に戻せます）"
	}
	if collide {
		msg += "。※呼び名を入れ替えたため、前に渡した書類を元に戻すと、呼び名の部分が新しい書き方で戻ることがあります（呼び名は、消さずに後ろへ書き足すのがおすすめです）"
	}
	s.bumpPend(1) // 置き換え直すまで③で渡さない（改善23）
	return map[string]string{"ok": msg}, nil
}

func (s *panelServer) namesDelete(r *http.Request) (any, error) {
	var q nameReq
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	if q.Old.Row < 2 {
		return nil, fmt.Errorf("消す行が正しくありません")
	}
	if q.Target.Row >= 2 {
		return s.namesDeleteMove(q.Old, q.Target)
	}
	code := s.codeOf(q.Old.Kind, q.Old.Name)
	shifted := false
	if err := modifyNameRow(s.p.nameList, q.Old, func(x string) (string, error) {
		nx, sh, err := deleteNameRowXML(x, q.Old.Row)
		shifted = sh
		return nx, err
	}); err != nil {
		return nil, err
	}
	msg := q.Old.Kind + "「" + q.Old.Name + "」を氏名一覧から消しました"
	if !shifted {
		msg += "（この氏名一覧には結合セルなどがあるため、行の中身だけを消し、空いた行はそのまま残しました）"
	}
	if code != "" {
		msg += "。記号対応表の記録（" + code + "）は残してあります。同じ区分・同じ氏名でもう一度登録すると、同じ記号に戻ります"
	}
	s.bumpPend(1) // 置き換え直すまで③で渡さない（改善23）
	return map[string]string{"ok": msg}, nil
}

// namesAlias: 住所などの語を、利用者・家族の「呼び名」に書き足す
func (s *panelServer) namesAlias(r *http.Request) (any, error) {
	var q nameReq
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	w := strings.TrimSpace(q.Word)
	if w != "" && strings.ContainsAny(w, "/／") && birthFull(w) == w {
		// 「/」は呼び名の区切りの字なので、「39 /10/ 17」のような日付は呼び名に入れられない。
		// 氏名一覧の区分「その他」の行にして、次の置き換えから記号にする（改善27）
		return s.otherDateAdd(w)
	}
	if w == "" || len([]rune(w)) > 60 || strings.ContainsAny(w, "\t\r\n【】/／、,，") {
		return nil, fmt.Errorf("書き足す語が正しくありません")
	}
	if q.Old.Row < 2 || (q.Old.Kind != "利用者" && q.Old.Kind != "家族") {
		return nil, fmt.Errorf("書き足す先（利用者・家族）を選んでください")
	}
	for _, a := range aliasList(q.Old.Alias) {
		if a == w {
			return map[string]string{"ok": "「" + w + "」は、すでに " + q.Old.Name + " の呼び名に書いてあります"}, nil
		}
	}
	na := cleanAlias(q.Old.Alias + "／" + w)
	if len([]rune(na)) > 120 {
		return nil, fmt.Errorf("呼び名の欄が長くなりすぎます。氏名一覧のページで整理してください")
	}
	if err := modifyNameRow(s.p.nameList, q.Old, func(x string) (string, error) {
		return editNameRowXML(x, q.Old.Row, q.Old.Kind, q.Old.Name, na)
	}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": "「" + w + "」を " + q.Old.Kind + "「" + q.Old.Name + "」の呼び名に書き足しました", "pend": s.bumpPend(1)}, nil
}

// ================= 読むだけのページ =================

type viewReq struct {
	What   string
	Name   string
	Box    string
	Folder string
}

type reqItem struct {
	File  string `json:"file"`
	User  string `json:"user"`
	Doc   string `json:"doc"`
	Month string `json:"month"`
	State string `json:"state"`
	Date  string `json:"date"`
	MTime string `json:"mtime"`
}

var (
	reReqTitle = regexp.MustCompile(`(?m)^#\s*依頼[：:]\s*(\S+)\s+(.+?)[（(]\s*(R\d{1,2}\.\d{1,2})\s*実施分\s*[）)]`)
	reReqState = regexp.MustCompile(`(?m)^\s*[-*]?\s*(?:\*\*)?状態(?:\*\*)?\s*[：:]\s*(.+?)\s*$`)
	reReqDate  = regexp.MustCompile(`(?m)^\s*[-*]?\s*依頼日時\s*[：:]\s*(\S+\s*\S*)`)
	reReqPlace = regexp.MustCompile("(?m)資料の場所\\s*[：:]\\s*`?([^`\\n]+)")
	reReqFile  = regexp.MustCompile(`^依頼_(利用者\d{3,})_([^_]+)_(R\d{1,2}\.\d{1,2})_`)
)

func parseRequest(name, text string, cb *codeBook) reqItem {
	it := reqItem{File: name}
	if m := reReqFile.FindStringSubmatch(name); m != nil {
		it.User, it.Doc, it.Month = m[1], m[2], m[3]
	}
	if m := reReqTitle.FindStringSubmatch(text); m != nil {
		it.User, it.Doc, it.Month = m[1], strings.TrimSpace(m[2]), m[3]
	} else if it.User == "" {
		if m := reReqPlace.FindStringSubmatch(text); m != nil {
			if u := reStageUser.FindStringSubmatch("【" + strings.SplitN(filepath.Base(strings.TrimRight(strings.ReplaceAll(m[1], `\`, "/"), "/")), "_", 2)[0] + "】"); u != nil {
				it.User = u[1]
			}
		}
	}
	if ms := reReqState.FindAllStringSubmatch(text, -1); len(ms) > 0 {
		it.State = strings.Trim(ms[len(ms)-1][1], "*` ")
	}
	if m := reReqDate.FindStringSubmatch(text); m != nil {
		it.Date = strings.TrimSpace(m[1])
	}
	if it.User != "" {
		if n, ok := cb.codeToOrig["【"+it.User+"】"]; ok {
			it.User = n + "（" + it.User + "）"
		}
	}
	it.State = restoreText(it.State, cb)
	return it
}

func (s *panelServer) view(r *http.Request) (any, error) {
	var q viewReq
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	p := s.p
	cb := s.codeBook()
	switch q.What {
	case "org":
		return s.orgView()
	case "shiji":
		dir := filepath.Join(p.ai, "00_指示")
		reqs, docs := []reqItem{}, []fileEntry{}
		for _, e := range entriesIn(dir) {
			if !strings.EqualFold(filepath.Ext(e.Name), ".md") {
				continue
			}
			if strings.HasPrefix(e.Name, "依頼_") {
				t, _ := readSmallText(filepath.Join(dir, e.Name))
				it := parseRequest(e.Name, t, cb)
				it.MTime = e.MTime
				reqs = append(reqs, it)
			} else {
				docs = append(docs, e)
			}
		}
		sort.SliceStable(reqs, func(a, b int) bool { return reqs[a].MTime > reqs[b].MTime })
		return map[string]any{"requests": reqs, "docs": docs}, nil
	case "shijiDoc":
		if !safeName(q.Name) || !strings.EqualFold(filepath.Ext(q.Name), ".md") {
			return nil, fmt.Errorf("名前が正しくありません")
		}
		t, err := readSmallText(filepath.Join(p.ai, "00_指示", q.Name))
		if err != nil {
			return nil, err
		}
		return map[string]string{"name": restoreText(q.Name, cb), "text": restoreText(t, cb)}, nil
	case "folder", "folderDoc":
		var root string
		switch q.Box {
		case "careplan":
			root = filepath.Join(p.ai, "ケアプラン")
		case "assess":
			root = filepath.Join(p.ai, "アセスメント")
		default:
			return nil, fmt.Errorf("フォルダが正しくありません")
		}
		if q.What == "folderDoc" {
			full, ok := safeRel(root, q.Name)
			ext := strings.ToLower(filepath.Ext(q.Name))
			if !ok || (ext != ".md" && ext != ".txt") {
				return nil, fmt.Errorf("名前が正しくありません")
			}
			t, err := readSmallText(full)
			if err != nil {
				return nil, err
			}
			return map[string]string{"name": q.Name, "text": restoreText(t, cb)}, nil
		}
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			return map[string]any{"missing": true, "files": []fileEntry{}}, nil
		}
		files := entriesIn(root)
		for _, d := range listDirs(root, ".") {
			for _, e := range entriesIn(filepath.Join(root, d)) {
				e.Name = d + "/" + e.Name
				files = append(files, e)
			}
		}
		return map[string]any{"files": files}, nil
	case "memo":
		t, err := readSmallText(filepath.Join(p.ai, "引き継ぎメモ.md"))
		if err != nil {
			return nil, fmt.Errorf("引き継ぎメモ.md が見つかりません")
		}
		return map[string]string{"name": "引き継ぎメモ.md", "text": restoreText(t, cb)}, nil
	case "howto":
		t, err := readSmallText(filepath.Join(s.extDir, "使い方.html"))
		if err != nil {
			return nil, fmt.Errorf("使い方.html が見つかりません（AI作業パネル.exe と同じフォルダに置いてください）")
		}
		return map[string]string{"html": t}, nil
	case "work":
		type dirInfo struct {
			Name  string      `json:"name"`
			Count int         `json:"count"`
			Files []fileEntry `json:"files"`
		}
		dirs := []dirInfo{}
		for _, d := range listDirs(p.work, ".") {
			fs := entriesIn(filepath.Join(p.work, d))
			dirs = append(dirs, dirInfo{d, len(fs), fs})
			for _, sd := range listDirs(filepath.Join(p.work, d), ".") {
				fs := entriesIn(filepath.Join(p.work, d, sd))
				dirs = append(dirs, dirInfo{d + "＼" + sd, len(fs), fs})
			}
		}
		return map[string]any{"dirs": dirs, "work": p.work}, nil
	case "aidir":
		var base string
		switch q.Box {
		case "40":
			base = p.aiOut
		case "50":
			base = filepath.Join(p.ai, "50_要相談")
		default:
			return nil, fmt.Errorf("フォルダが正しくありません")
		}
		if !safeName(q.Folder) || strings.HasPrefix(q.Folder, "マスキングツール") {
			return nil, fmt.Errorf("フォルダが正しくありません")
		}
		dir := filepath.Join(base, q.Folder)
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("フォルダが見つかりません")
		}
		type doc struct {
			Name string `json:"name"`
			Text string `json:"text"`
		}
		files := []fileEntry{}
		docs := []doc{}
		for _, e := range entriesIn(dir) {
			e2 := e
			e2.Name = restoreText(e.Name, cb)
			files = append(files, e2)
			if strings.EqualFold(filepath.Ext(e.Name), ".md") {
				if t, err := readSmallText(filepath.Join(dir, e.Name)); err == nil {
					docs = append(docs, doc{e2.Name, restoreText(t, cb)})
				}
			}
		}
		// 判定記録・作業メモを先に
		rank := func(n string) int {
			switch {
			case strings.HasPrefix(n, "判定記録"):
				return 0
			case strings.HasPrefix(n, "作業メモ"):
				return 1
			case strings.HasPrefix(n, "相談メモ"):
				return 0
			}
			return 2
		}
		sort.SliceStable(docs, func(a, b int) bool { return rank(docs[a].Name) < rank(docs[b].Name) })
		return map[string]any{"label": labelFor(q.Folder, cb), "files": files, "docs": docs}, nil
	}
	return nil, fmt.Errorf("表示できません")
}

// ================= 文字起こしの貼り付け =================

const maxPasteRunes = 200000

var reBadFileChar = regexp.MustCompile(`[\\/:*?"<>|【】\x00-\x1f\x7f]`)

func normPaste(t string) string {
	t = strings.ReplaceAll(strings.ReplaceAll(t, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(t, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRightFunc(l, unicode.IsSpace)
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// xmlSafe: XMLに入れられない制御文字を除く
func xmlSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r >= 0x20 && r != 0xFFFE && r != 0xFFFF && !(r >= 0xD800 && r <= 0xDFFF) {
			return r
		}
		return -1
	}, s)
}

func pasteDocx(title, text string) ([]byte, error) {
	var body strings.Builder
	rpr := `<w:rPr><w:rFonts w:ascii="游ゴシック" w:eastAsia="游ゴシック" w:hAnsi="游ゴシック"/><w:sz w:val="21"/><w:szCs w:val="21"/></w:rPr>`
	para := func(t string, bold bool) {
		body.WriteString(`<w:p><w:pPr><w:spacing w:after="0"/></w:pPr>`)
		if t != "" {
			r := rpr
			if bold {
				r = strings.Replace(rpr, `<w:sz `, `<w:b/><w:sz `, 1)
			}
			for i, part := range strings.Split(t, "\t") {
				if i > 0 {
					body.WriteString(`<w:r>` + r + `<w:tab/></w:r>`)
				}
				if part != "" {
					body.WriteString(`<w:r>` + r + `<w:t xml:space="preserve">` + encodeText(part) + `</w:t></w:r>`)
				}
			}
		}
		body.WriteString(`</w:p>`)
	}
	if title != "" {
		para("【文字起こし】"+title, true)
		para("", false)
	}
	for _, l := range strings.Split(text, "\n") {
		para(l, false)
	}
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body.String() +
		`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134" w:header="567" w:footer="567" w:gutter="0"/></w:sectPr></w:body></w:document>`
	files := []struct{ name, data string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/document.xml", doc},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.data)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// 貼り付けた本文のハッシュと、できたファイル名の控え（作業フォルダの 確認リスト の中。本文そのものは残さない）
func (s *panelServer) pasteLedger() string {
	return filepath.Join(s.p.report, "文字起こし_控え.tsv")
}

func (s *panelServer) pasteDup(hash string) string {
	b, err := os.ReadFile(s.pasteLedger())
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 2 || f[0] != hash || !safeName(f[1]) {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.p.src, f[1])); err == nil {
			return "1_元データ に「" + f[1] + "」として入っています"
		}
		if _, err := os.Stat(filepath.Join(s.p.done, f[1])); err == nil {
			return "すでに置き換え済みです（1_元データ＼処理済み の「" + f[1] + "」）"
		}
	}
	return ""
}

func (s *panelServer) paste(r *http.Request) (any, error) {
	var q struct{ Title, Text string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&q); err != nil {
		return nil, fmt.Errorf("貼り付けた文が長すぎるか、受け取れませんでした（目安：20万字まで）")
	}
	text := normPaste(xmlSafe(q.Text))
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("貼り付け欄が空です。文字起こしの文を貼り付けてください")
	}
	if n := utf8.RuneCountInString(text); n > maxPasteRunes {
		return nil, fmt.Errorf("長すぎます（%d字）。20万字までにしてください（分けて入れてください）", n)
	}
	title := strings.Join(strings.Fields(xmlSafe(q.Title)), " ")
	if utf8.RuneCountInString(title) > 60 {
		return nil, fmt.Errorf("題名は60字までにしてください")
	}
	sum := sha256.Sum256([]byte(text))
	hash := hex.EncodeToString(sum[:])
	if where := s.pasteDup(hash); where != "" {
		return nil, fmt.Errorf("同じ内容の文字起こしを、すでに入れています（%s）。2回目は入れませんでした", where)
	}
	data, err := pasteDocx(title, text)
	if err != nil {
		return nil, err
	}
	ft := strings.TrimSpace(reBadFileChar.ReplaceAllString(title, ""))
	ft = strings.Trim(strings.Join(strings.Fields(ft), "_"), ". ")
	if r := []rune(ft); len(r) > 40 {
		ft = string(r[:40])
	}
	name := "文字起こし_"
	if ft != "" {
		name += ft + "_"
	}
	name += time.Now().Format("20060102_1504") + ".docx"
	dst := uniquePath(s.p.src, name)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		_, err = out.Write(data)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		os.Remove(dst)
		return nil, fmt.Errorf("1_元データ に保存できませんでした: %v", err)
	}
	lf, err := os.OpenFile(s.pasteLedger(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err == nil {
		fmt.Fprintf(lf, "%s\t%s\r\n", hash, filepath.Base(dst))
		lf.Close()
	}
	return map[string]any{"ok": "「" + filepath.Base(dst) + "」（Word）として 1_元データ に入れました（" + fmt.Sprint(utf8.RuneCountInString(text)) + "字・" + fmt.Sprint(strings.Count(text, "\n")+1) + "行）。このあと「置き換える」を押してください", "name": filepath.Base(dst)}, nil
}

// ================= 声で入力（Windows の音声入力を立ち上げる） =================

func (s *panelServer) voice(r *http.Request) (any, error) {
	ok, hint := sendWinH()
	if ok {
		return map[string]any{"sent": true, "ok": "音声入力を立ち上げました。題名を話してください（止めるときは Windowsキー＋H をもう一度）"}, nil
	}
	return map[string]any{"sent": false, "hint": hint}, nil
}

// otherDateAdd: 「/」入りの日付を、氏名一覧の区分「その他」の行に足す（同じものがあれば足さない）
func (s *panelServer) otherDateAdd(w string) (any, error) {
	if nameListOpenInExcel(s.p.nameList) {
		return nil, fmt.Errorf("氏名一覧.xlsx がExcelで開かれています。Excelを閉じてから、もう一度押してください")
	}
	rows, err := readNameRows(s.p.nameList)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Kind == "その他" && normName(r.Name) == normName(w) {
			return map[string]string{"ok": "「" + w + "」は、すでに氏名一覧の「その他」に入っています"}, nil
		}
	}
	if err := appendNameRow(s.p.nameList, "その他", w); err != nil {
		return nil, err
	}
	return map[string]any{"ok": "「" + w + "」を記号にするように登録しました（「/」の入った日付なので、呼び名ではなく氏名一覧の区分「その他」に入れました）", "pend": s.bumpPend(1)}, nil
}
