package main

// 片づける：済んだ仕事のファイルを、一覧で確かめてから Windows のごみ箱へ移す（完全削除はしない）。
//
//   - 「片づける」を押す → /api/clean/preview：一覧を作るだけ。何も動かさない
//   - 確認画面で「ごみ箱へ移す」を押す → /api/clean/run：一覧のときと同じもの（場所・名前・中身の大きさと日時）だけを移す
//   - 氏名一覧・記号対応表・登録セル・対応表フォルダ・1_元データ直下・00_指示のマニュアル類・様式原本・引き継ぎメモは、
//     どんな入力でも対象にしない（protectedReason）
//   - 紐付けが確かでないもの（どの仕事のものか分からない元データなど）は入れずに、確認画面で知らせる
//
// 紐付けのための控え：確認リスト\片づけ用の控え.tsv（名前は書かない。ファイルの中身のハッシュと、記号の仕事名だけ）
//   M 日時 元データのハッシュ 結果(ok/held/err) 3_確認待ちのファイルのハッシュ 確認リストの名前   … 置き換えたとき
//   S 日時 渡したファイルのハッシュ 仕事（利用者001_モニタリング|R8.9）                       … AIに渡したとき
//   R 日時 2_完成のファイルのハッシュ 40のフォルダ名 確認リストの名前                           … 元に戻したとき

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	cleanLedgerName  = "片づけ用の控え.tsv"
	cleanHistoryName = "片づけ履歴.txt"
	consultDirName   = "50_要相談"
	shijiDirName     = "00_指示"
	planLifetime     = 30 * time.Minute
)

var (
	// 40のフォルダ名：「利用者014_モニタリング（R8.10実施）」（前からの形）と、改善29 No.62 の書類ごとの形
	// 「利用者014_ケアプラン（R8.10.7～適用・R8.10.7作成）」「利用者014_アセスメント（R8.10.7作成）」「利用者020_支援経過1日分R8.10.6（R8.10.6）」。
	// どちらの形も読む。かっこの中は、表示ではそのまま出す（Paren）。月（Month）は、かっこの中の日付から決める
	reJobFolder  = regexp.MustCompile(`^(利用者\d{3,})_([^_（(]+)[（(]([^（）()]+)[）)]$`)
	reReqName    = regexp.MustCompile(`^依頼_(利用者\d{3,})_([^_]+)_(R\d{1,2}\.\d{1,2})_\d{4}_\d{4}(?:\(\d+\))?\.md$`)
	reReportName = regexp.MustCompile(`^確認リスト_(置き換え|元に戻す)_\d{8}_\d{6}(?:\(\d+\))?\.html$`)
	jobDocs      = map[string]bool{"モニタリング": true, "支援経過": true, "ケアプラン": true, "アセスメント": true}
	// どこにあっても対象にしないファイル名
	protectedNames = map[string]bool{"氏名一覧.xlsx": true, "記号対応表.tsv": true, "登録セル.txt": true, "引き継ぎメモ.md": true,
		cleanHistoryName: true, cleanLedgerName: true, "last.json": true, "文字起こし_控え.tsv": true, intakeFileName: true}
	// 30・40・50 の中で、仕事ではない（ツール・置き場の）フォルダ
	protectedFolderPrefix = []string{"マスキングツール", "AI作業パネル", "_削除してよい"}
)

type jobID struct {
	User, Doc, Month string
	Paren            string // 40のフォルダ名のかっこの中（表示用。例：R8.10.7～適用・R8.10.7作成／R8.10実施）
}

func (j jobID) key() string   { return j.User + "_" + j.Doc + "|" + j.Month }
func (j jobID) dir30() string { return j.User + "_" + j.Doc }

var (
	reParenLegacy = regexp.MustCompile(`^(R\d{1,2}\.\d{1,2})実施$`)
	reParenMade   = regexp.MustCompile(`R(\d{1,2})\.(\d{1,2})\.\d{1,2}作成$`)
	reParenDay    = regexp.MustCompile(`^R(\d{1,2})\.(\d{1,2})\.\d{1,2}$`)
)

// jobMonthOfParen: 40のフォルダ名のかっこの中から、仕事の月（R8.10）を決める。どの形でもない（読めない）ときは false
func jobMonthOfParen(paren string) (string, bool) {
	paren = toHalfDigits(strings.TrimSpace(paren))
	if m := reParenLegacy.FindStringSubmatch(paren); m != nil {
		return m[1], true
	}
	if m := reParenMade.FindStringSubmatch(paren); m != nil { // ケアプラン・アセスメント：作成日の月（依頼の月も作成日の月）
		return "R" + m[1] + "." + m[2], true
	}
	if m := reParenDay.FindStringSubmatch(paren); m != nil { // 支援経過1日分：その日の月
		return "R" + m[1] + "." + m[2], true
	}
	return "", false
}

func parseJobFolder(name string) (jobID, bool) {
	m := reJobFolder.FindStringSubmatch(name)
	if m == nil || !isJobDoc(m[2]) {
		return jobID{}, false
	}
	month, ok := jobMonthOfParen(m[3])
	if !ok {
		return jobID{}, false
	}
	return jobID{User: m[1], Doc: m[2], Month: month, Paren: m[3]}, true
}

// parenOrJob: 仕事の表示に使う「（…）」の中身。40のフォルダ名のかっこの中があればそのまま、なければ月＋実施
func (j jobID) parenText() string {
	if j.Paren != "" {
		return j.Paren
	}
	return j.Month + "実施"
}

// ---------------- 控え（ハッシュ） ----------------

func (s *panelServer) hashFile(path string) string {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	ck := fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())
	if h, ok := s.hcache[ck]; ok {
		return h
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	hs := sha256.New()
	if _, err := io.Copy(hs, f); err != nil {
		return ""
	}
	h := hex.EncodeToString(hs.Sum(nil))
	if s.hcache == nil || len(s.hcache) > 5000 {
		s.hcache = map[string]string{}
	}
	s.hcache[ck] = h
	return h
}

func (s *panelServer) ledgerAppend(lines [][]string) {
	if len(lines) == 0 {
		return
	}
	f, err := os.OpenFile(filepath.Join(s.p.report, cleanLedgerName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	now := time.Now().Format("2006/01/02 15:04:05")
	for _, l := range lines {
		for i := range l {
			l[i] = strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(l[i])
		}
		fmt.Fprintf(f, "%s\t%s\t%s\r\n", l[0], now, strings.Join(l[1:], "\t"))
	}
}

type cleanLedger struct {
	srcStage      map[string]map[string]bool // 元データのハッシュ → 3_確認待ちのファイルのハッシュ
	stageKey      map[string]map[string]bool // 渡したファイルのハッシュ → 仕事
	reportSrc     map[string][]string        // 置き換えの確認リスト → 元データのハッシュ
	outFolder     map[string]map[string]bool // 2_完成のファイルのハッシュ → 40のフォルダ
	restoreReport map[string]map[string]bool // 元に戻すの確認リスト → 40のフォルダ
}

func addSet(m map[string]map[string]bool, k, v string) {
	if k == "" || v == "" {
		return
	}
	if m[k] == nil {
		m[k] = map[string]bool{}
	}
	m[k][v] = true
}

func (s *panelServer) readLedger() *cleanLedger {
	l := &cleanLedger{map[string]map[string]bool{}, map[string]map[string]bool{}, map[string][]string{}, map[string]map[string]bool{}, map[string]map[string]bool{}}
	b, err := os.ReadFile(filepath.Join(s.p.report, cleanLedgerName))
	if err != nil {
		return l
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		switch {
		case len(f) >= 6 && f[0] == "M":
			if f[2] == "" {
				continue
			}
			if f[3] == "ok" {
				addSet(l.srcStage, f[2], f[4])
			}
			if f[5] != "" {
				l.reportSrc[f[5]] = append(l.reportSrc[f[5]], f[2])
			}
		case len(f) >= 4 && f[0] == "S":
			addSet(l.stageKey, f[2], f[3])
		case len(f) >= 5 && f[0] == "R":
			addSet(l.outFolder, f[2], f[3])
			addSet(l.restoreReport, f[4], f[3])
		}
	}
	return l
}

// srcKeys: 元データ（ハッシュ）が渡された仕事
func (l *cleanLedger) srcKeys(h string) map[string]bool {
	out := map[string]bool{}
	for st := range l.srcStage[h] {
		for k := range l.stageKey[st] {
			out[k] = true
		}
	}
	return out
}

func onlyKey(m map[string]bool, k string) bool { return len(m) == 1 && m[k] }

// ---------------- 守り ----------------

func (s *panelServer) cleanRoot(where string) (string, bool) {
	p := s.p
	switch where {
	case "30":
		return p.aiIn, true
	case "40":
		return p.aiOut, true
	case "50":
		return filepath.Join(p.ai, consultDirName), true
	case "00":
		return filepath.Join(p.ai, shijiDirName), true
	case "done":
		return p.done, true
	case "out":
		return p.out, true
	case "report":
		return p.report, true
	}
	return "", false
}

func within(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// cleanTarget: 場所と名前から、移してよいものの場所を返す。守るものは理由を返す
func (s *panelServer) cleanTarget(where, name string) (string, error) {
	root, ok := s.cleanRoot(where)
	if !ok {
		return "", fmt.Errorf("この場所のものは片づけられません")
	}
	if !safeName(name) || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("名前が正しくありません（「..」・「/」「\\」・ドライブ名の入った名前は使えません）：%s", name)
	}
	abs := filepath.Join(root, name)
	if filepath.Dir(abs) != filepath.Clean(root) {
		return "", fmt.Errorf("名前が正しくありません：%s", name)
	}
	if r := s.protectedReason(where, name, abs); r != "" {
		return "", fmt.Errorf("%s：%s", r, name)
	}
	return abs, nil
}

func (s *panelServer) protectedReason(where, name, abs string) string {
	p := s.p
	if protectedNames[name] {
		return "守るファイル（氏名一覧・記号対応表・履歴など）なので移せません"
	}
	for _, f := range []string{p.nameList, p.codeBookF, p.cellCfg, filepath.Join(p.ai, "引き継ぎメモ.md")} {
		if filepath.Clean(abs) == filepath.Clean(f) {
			return "守るファイルなので移せません"
		}
	}
	for _, d := range []string{p.table, filepath.Join(p.ai, "ケアプラン"), filepath.Join(p.ai, "アセスメント")} {
		if within(d, abs) {
			return "対応表・様式の原本は守るので移せません"
		}
	}
	for _, d := range []string{p.work, p.src, p.done, p.out, p.report, p.stage, p.table, p.ai, p.aiIn, p.aiOut, filepath.Join(p.ai, consultDirName), filepath.Join(p.ai, shijiDirName)} {
		if filepath.Clean(abs) == filepath.Clean(d) {
			return "決まったフォルダそのものは移せません"
		}
	}
	if filepath.Dir(abs) == filepath.Clean(p.src) || within(p.stage, abs) {
		return "1_元データ（まだ処理していないもの）・3_確認待ち は移せません"
	}
	switch where {
	case "00":
		if !reReqName.MatchString(name) {
			return "00_指示 では、依頼の書き置き（依頼_〜.md）のほかは移せません（マニュアルなどを守るため）"
		}
	case "report":
		if !reReportName.MatchString(name) {
			return "確認リストでは、置き換え・元に戻すのときの確認リストのほかは移せません"
		}
	case "30", "40", "50":
		for _, pre := range protectedFolderPrefix {
			// 40 の古いパネルの版・「_削除してよい」は、④から片づけてよい（R8.9.29 注文）。保管の印があるものは下の checkCleanFolder で守る
			if where == "40" && (pre == "AI作業パネル" || pre == "_削除してよい") {
				continue
			}
			if strings.HasPrefix(name, pre) {
				return "ツールや置き場のフォルダは移せません"
			}
		}
	}
	return ""
}

// snapshot: いまの中身（大きさ・更新日時）。フォルダは中の全部。リンクや守るファイルが中にあれば入れない
func snapshot(abs string) (sig string, isDir bool, inner []string, err error) {
	st, err := os.Lstat(abs)
	if err != nil {
		return "", false, nil, fmt.Errorf("見つかりません")
	}
	switch {
	case st.Mode().IsRegular():
		return fmt.Sprintf("f|%d|%d", st.Size(), st.ModTime().UnixNano()), false, nil, nil
	case st.IsDir():
	default:
		return "", false, nil, fmt.Errorf("ショートカット（リンク）などの特別なものなので入れていません")
	}
	var lines []string
	werr := filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == abs {
			return nil
		}
		rel, _ := filepath.Rel(abs, path)
		t := d.Type()
		if t&fs.ModeSymlink != 0 || (t&^fs.ModeDir) != 0 {
			return fmt.Errorf("中にショートカット（リンク）などの特別なものがあるので入れていません")
		}
		if protectedNames[d.Name()] {
			return fmt.Errorf("中に守るファイル（%s）が入っているので入れていません", d.Name())
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			lines = append(lines, "d|"+filepath.ToSlash(rel))
			return nil
		}
		inner = append(inner, filepath.ToSlash(rel))
		lines = append(lines, fmt.Sprintf("f|%s|%d|%d", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	if werr != nil {
		if strings.Contains(werr.Error(), "入れていません") {
			return "", true, nil, werr
		}
		return "", true, nil, fmt.Errorf("中を確かめられませんでした")
	}
	sort.Strings(lines)
	sort.Strings(inner)
	h := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return "d|" + hex.EncodeToString(h[:]), true, inner, nil
}

// ---------------- 一覧（確認画面） ----------------

type planItem struct {
	Where string   `json:"where"`
	Name  string   `json:"name"`
	IsDir bool     `json:"isDir"`
	Inner []string `json:"inner,omitempty"`
	sig   string
}

type planGroup struct {
	Where string     `json:"where"`
	Title string     `json:"title"`
	Items []planItem `json:"items"`
}

type skipNote struct {
	Title  string   `json:"title"`
	Reason string   `json:"reason"`
	Names  []string `json:"names"`
}

type cleanPlan struct {
	ID      string      `json:"id"`
	Kind    string      `json:"kind"`
	Folder  string      `json:"folder"`
	Label   string      `json:"label"`
	Groups  []planGroup `json:"groups"`
	Skipped []skipNote  `json:"skipped"`
	Total   int         `json:"total"`
	Place   string      `json:"place"`
	job     jobID
	jobOK   bool
	created time.Time
	subs    []*cleanPlan // まとめて片づけるときの、仕事ごとの一覧（履歴を仕事ごとに書く）
}

var groupTitles = map[string]string{
	"30":     "30_記入係作業中（AIの作業フォルダ）",
	"40":     "40_合格・確認待ち（合格品のフォルダ）",
	"50":     "50_要相談（相談のフォルダ）",
	"00":     "00_指示（依頼の書き置き）",
	"done":   "1_元データ＼処理済み（置き換えに使った元の資料）",
	"out":    "2_完成（元の名前に戻した書類）",
	"report": "確認リスト（置き換え・元に戻すのときの記録）",
}

var groupOrder = []string{"30", "40", "50", "00", "done", "out", "report"}

func (s *panelServer) checkCleanFolder(kind, folder string) error {
	if kind != "40" && kind != "50" {
		return fmt.Errorf("片づける場所が正しくありません")
	}
	abs, err := s.cleanTarget(kind, folder)
	if err != nil {
		return err
	}
	st, err := os.Lstat(abs)
	if kind == "50" {
		if err != nil || !st.IsDir() {
			return fmt.Errorf("フォルダが見つかりません")
		}
		return nil
	}
	if err == nil && st.IsDir() {
		if _, err := os.Stat(filepath.Join(abs, "保管.txt")); err == nil {
			return fmt.Errorf("AIが使う保管品（保管の印があるもの）なので片づけられません")
		}
		if _, isJob := parseJobFolder(folder); !isJob {
			return nil // 利用者の書類ではないもの（古いパネルの版・提案・_削除してよい）は、元に戻さなくても片づけられる
		}
		if _, err := os.Stat(filepath.Join(abs, "元に戻し済み.txt")); err != nil {
			return fmt.Errorf("まだ元に戻していない合格品は片づけられません（先に「4 届いた合格品」で元に戻してください）")
		}
		return nil
	}
	// 40 のフォルダがもうない：元に戻したときの控えがあれば、2_完成 などだけ片づけられる
	for _, fset := range s.readLedger().outFolder {
		if fset[folder] {
			return nil
		}
	}
	return fmt.Errorf("フォルダが見つかりません")
}

func (s *panelServer) buildPlan(kind, folder string) (*cleanPlan, error) {
	if err := s.checkCleanFolder(kind, folder); err != nil {
		return nil, err
	}
	p := s.p
	cb := s.codeBook()
	job, jobOK := parseJobFolder(folder)
	pl := &cleanPlan{Kind: kind, Folder: folder, Label: labelFor(folder, cb), Groups: []planGroup{}, Skipped: []skipNote{}, Place: trashPlaceForScreen, job: job, jobOK: jobOK}
	items := map[string][]planItem{}
	unknown := []string{}
	skip := func(title, reason string, names ...string) {
		if len(names) == 0 {
			return
		}
		pl.Skipped = append(pl.Skipped, skipNote{title, reason, names})
	}
	add := func(where, name string) {
		abs, err := s.cleanTarget(where, name)
		if err != nil {
			skip(groupTitles[where], err.Error(), name)
			return
		}
		sig, isDir, inner, err := snapshot(abs)
		if err != nil {
			skip(groupTitles[where], err.Error(), name)
			return
		}
		items[where] = append(items[where], planItem{Where: where, Name: name, IsDir: isDir, Inner: inner, sig: sig})
	}
	// 同じ仕事（同じ利用者・書類・月）や、同じ30のフォルダを使う、ほかの 40・50 のフォルダ
	sameKey, same30 := []string{}, []string{}
	for _, w := range []string{"40", "50"} {
		root, _ := s.cleanRoot(w)
		for _, d := range listDirs(root, ".") {
			if w == kind && d == folder {
				continue
			}
			if j, ok := parseJobFolder(d); ok && jobOK {
				if j.key() == job.key() {
					sameKey = append(sameKey, w+"＼"+d)
				}
				if j.dir30() == job.dir30() {
					same30 = append(same30, w+"＼"+d)
				}
			}
		}
	}
	// 1) 40・50 のフォルダ
	if st, err := os.Lstat(filepath.Join(mustRoot(s, kind), folder)); err == nil && st.IsDir() {
		add(kind, folder)
	}
	ld := s.readLedger()
	if !jobOK && strings.HasPrefix(folder, "利用者") { // 利用者の仕事らしいのに読み取れないときだけ知らせる（古いパネルの版・提案などは、もともと関係なし）
		skip("30・00_指示・処理済みの元データ・置き換えの確認リスト", "フォルダの名前から、どの利用者・どの書類・何月の仕事か読み取れないので入れていません", folder)
	} else {
		// 2) 00_指示 の依頼（名前が 依頼_利用者001_モニタリング_R8.9_… のもの）
		var reqs []string
		otherMonth := []string{}
		for _, f := range listFiles(filepath.Join(p.ai, shijiDirName)) {
			m := reReqName.FindStringSubmatch(f)
			if m == nil || m[1] != job.User || m[2] != job.Doc {
				continue
			}
			if m[3] == job.Month {
				reqs = append(reqs, f)
			} else {
				otherMonth = append(otherMonth, f)
			}
		}
		if len(sameKey) > 0 {
			skip(groupTitles["00"], "同じ仕事の別のフォルダ（"+strings.Join(sameKey, "、")+"）が残っているので入れていません。そちらを片づけるときに入ります", reqs...)
		} else {
			for _, f := range reqs {
				add("00", f)
			}
		}
		// 3) 30 の作業フォルダ（利用者001_モニタリング）。ほかの月の仕事でも使っていれば入れない
		if st, err := os.Lstat(filepath.Join(p.aiIn, job.dir30())); err == nil && st.IsDir() {
			if len(same30) > 0 || len(otherMonth) > 0 {
				why := append(append([]string{}, same30...), otherMonth...)
				skip(groupTitles["30"], "同じ利用者・同じ書類の、ほかの仕事でも使うフォルダなので入れていません（"+strings.Join(why, "、")+"）", job.dir30())
			} else {
				add("30", job.dir30())
			}
		}
		// 4) 処理済みの元データ（控えのハッシュで、この仕事に渡したものだけ）
		shared := []string{}
		for _, f := range listFiles(p.done) {
			keys := ld.srcKeys(s.hashFile(filepath.Join(p.done, f)))
			switch {
			case len(keys) == 0:
				unknown = append(unknown, "1_元データ＼処理済み＼"+f)
			case onlyKey(keys, job.key()):
				if len(sameKey) > 0 {
					shared = append(shared, f)
				} else {
					add("done", f)
				}
			case keys[job.key()]:
				skip(groupTitles["done"], "ほかの仕事にも渡した資料なので入れていません", f)
			}
		}
		skip(groupTitles["done"], "同じ仕事の別のフォルダ（"+strings.Join(sameKey, "、")+"）が残っているので入れていません", shared...)
	}
	// 5) 2_完成（控えのハッシュで、この 40 のフォルダから元に戻したものだけ）
	if kind == "40" {
		for _, f := range listFiles(p.out) {
			fset := ld.outFolder[s.hashFile(filepath.Join(p.out, f))]
			switch {
			case len(fset) == 0:
				unknown = append(unknown, "2_完成＼"+f)
			case onlyKey(fset, folder):
				add("out", f)
			case fset[folder]:
				skip(groupTitles["out"], "ほかの仕事の書類と同じ中身なので入れていません", f)
			}
		}
	}
	// 6) 確認リスト
	for _, f := range listFiles(p.report) {
		m := reReportName.FindStringSubmatch(f)
		if m == nil {
			continue
		}
		if m[1] == "元に戻す" {
			fset := ld.restoreReport[f]
			switch {
			case len(fset) == 0:
				unknown = append(unknown, "確認リスト＼"+f)
			case kind == "40" && onlyKey(fset, folder):
				add("report", f)
			}
			continue
		}
		srcs := ld.reportSrc[f]
		if len(srcs) == 0 {
			unknown = append(unknown, "確認リスト＼"+f)
			continue
		}
		if !jobOK {
			continue
		}
		mine, all := 0, true
		for _, h := range srcs {
			keys := ld.srcKeys(h)
			if keys[job.key()] {
				mine++
			}
			if !onlyKey(keys, job.key()) {
				all = false
			}
		}
		switch {
		case mine == 0:
		case all && len(sameKey) == 0:
			add("report", f)
		case all:
			skip(groupTitles["report"], "同じ仕事の別のフォルダ（"+strings.Join(sameKey, "、")+"）が残っているので入れていません", f)
		default:
			skip(groupTitles["report"], "ほかの仕事の資料（または、まだAIに渡していない資料）のことも書かれているので入れていません", f)
		}
	}
	sort.Strings(unknown)
	skip("どの仕事のものか分からないもの", "どの仕事のものか分からないので入れていません（この版より前に処理したもの・置き換え.exe や 元に戻す.exe で処理したもの・手で直したものなど）。要らなければ、ご自身で確かめて片づけてください", unknown...)
	for _, w := range groupOrder {
		if len(items[w]) > 0 {
			pl.Groups = append(pl.Groups, planGroup{Where: w, Title: groupTitles[w], Items: items[w]})
			pl.Total += len(items[w])
		}
	}
	return pl, nil
}

func mustRoot(s *panelServer, where string) string {
	r, _ := s.cleanRoot(where)
	return r
}

func newPlanID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *panelServer) cleanPreview(r *http.Request) (any, error) {
	var q struct{ Kind, Folder string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	pl, err := s.buildPlan(q.Kind, q.Folder)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if s.plans == nil {
		s.plans = map[string]*cleanPlan{}
	}
	for id, x := range s.plans {
		if now.Sub(x.created) > planLifetime {
			delete(s.plans, id)
		}
	}
	pl.ID = newPlanID()
	pl.created = now
	s.plans[pl.ID] = pl
	return pl, nil
}

// まとめて片づける（R8.9.29）：選んだ仕事ごとに一覧を作り、1つの確認画面にまとめる
func (s *panelServer) cleanPreviewMulti(r *http.Request) (any, error) {
	var q struct {
		Kind    string
		Folders []string
	}
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	if q.Kind != "40" && q.Kind != "50" {
		return nil, fmt.Errorf("片づける場所が正しくありません")
	}
	if len(q.Folders) == 0 || len(q.Folders) > 100 {
		return nil, fmt.Errorf("選んだものがありません")
	}
	all := &cleanPlan{Kind: q.Kind, Groups: []planGroup{}, Skipped: []skipNote{}, Place: trashPlaceForScreen}
	seen := map[string]bool{}
	idx := map[string]int{}
	var bad []string
	for _, f := range q.Folders {
		if seen["f\x00"+f] {
			continue
		}
		seen["f\x00"+f] = true
		pl, err := s.buildPlan(q.Kind, f)
		if err != nil {
			bad = append(bad, f+"（"+err.Error()+"）")
			continue
		}
		all.subs = append(all.subs, pl)
		for _, g := range pl.Groups {
			for _, it := range g.Items {
				k := it.Where + "\x00" + it.Name
				if seen[k] {
					continue
				}
				seen[k] = true
				gi, ok := idx[g.Where]
				if !ok {
					all.Groups = append(all.Groups, planGroup{Where: g.Where, Title: g.Title, Items: []planItem{}})
					gi = len(all.Groups) - 1
					idx[g.Where] = gi
				}
				all.Groups[gi].Items = append(all.Groups[gi].Items, it)
				all.Total++
			}
		}
		all.Skipped = append(all.Skipped, pl.Skipped...)
	}
	if len(bad) > 0 {
		all.Skipped = append([]skipNote{{"片づけられないもの", "選んだ中に、片づけられないものがありました（入れていません）", bad}}, all.Skipped...)
	}
	all.Label = fmt.Sprintf("まとめて片づける（選んだ %d 件）", len(all.subs))
	now := time.Now()
	if s.plans == nil {
		s.plans = map[string]*cleanPlan{}
	}
	for id, x := range s.plans {
		if now.Sub(x.created) > planLifetime {
			delete(s.plans, id)
		}
	}
	all.ID = newPlanID()
	all.created = now
	s.plans[all.ID] = all
	return all, nil
}

// ---------------- ごみ箱へ移す ----------------

type movedJSON struct {
	Where  string `json:"where"`
	Name   string `json:"name"`
	Reason string `json:"reason,omitempty"`
}

func (s *panelServer) cleanRun(r *http.Request) (any, error) {
	var q struct {
		ID    string
		Items []struct{ Where, Name string }
	}
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	// 1) 送られてきた一つひとつを、守りの規則で確かめる（一覧にあるかどうかより先に）
	if len(q.Items) == 0 {
		return nil, fmt.Errorf("移すものがありません")
	}
	want := map[string]bool{}
	for _, it := range q.Items {
		if _, err := s.cleanTarget(it.Where, it.Name); err != nil {
			return nil, fmt.Errorf("移せません（何も移していません）。%v", err)
		}
		want[it.Where+"\x00"+it.Name] = true
	}
	// 2) 確認画面の一覧と同じものか
	pl, ok := s.plans[q.ID]
	if !ok || time.Since(pl.created) > planLifetime {
		return nil, fmt.Errorf("確認画面の一覧が見つかりません（時間がたったか、パネルを起動し直しました）。もう一度「片づける」を押して、一覧を確かめてください（何も移していません）")
	}
	var planned []planItem
	for _, g := range pl.Groups {
		planned = append(planned, g.Items...)
	}
	if len(planned) != len(want) {
		return nil, fmt.Errorf("確認画面の一覧と違うものが送られてきました（何も移していません）")
	}
	for _, it := range planned {
		if !want[it.Where+"\x00"+it.Name] {
			return nil, fmt.Errorf("確認画面の一覧と違うものが送られてきました（何も移していません）")
		}
	}
	// 3) 一覧を作ったあとで中身が変わっていないか（増えた・書き換わったものを黙って移さない）
	for _, it := range planned {
		abs, err := s.cleanTarget(it.Where, it.Name)
		if err != nil {
			return nil, fmt.Errorf("移せません（何も移していません）。%v", err)
		}
		sig, _, _, err := snapshot(abs)
		if err != nil || sig != it.sig {
			delete(s.plans, q.ID)
			return nil, fmt.Errorf("確かめたあとで「%s」の中身が変わりました。もう一度「片づける」を押して、一覧を確かめてください（何も移していません）", it.Name)
		}
	}
	delete(s.plans, q.ID) // 1回だけ使える
	// 4) 移す（40・50 のフォルダは最後。途中で止まっても、⑥・⑤に残って、もう一度片づけられる）
	order := map[string]int{"out": 0, "done": 1, "report": 2, "00": 3, "30": 4, "40": 5, "50": 5}
	sort.SliceStable(planned, func(a, b int) bool { return order[planned[a].Where] < order[planned[b].Where] })
	moved, failed := []movedJSON{}, []movedJSON{}
	for _, it := range planned {
		abs, err := s.cleanTarget(it.Where, it.Name)
		if err == nil {
			err = moveToTrash(abs, s.p.work)
		}
		if err != nil {
			failed = append(failed, movedJSON{it.Where, it.Name, err.Error()})
			continue
		}
		moved = append(moved, movedJSON{Where: it.Where, Name: it.Name})
	}
	s.hcache = nil
	// 5) 履歴：名前を書かない1行（日付・記号・書類・件数）
	if len(pl.subs) > 0 {
		mv, fl := map[string]bool{}, map[string]bool{}
		for _, m := range moved {
			mv[m.Where+"\x00"+m.Name] = true
		}
		for _, m := range failed {
			fl[m.Where+"\x00"+m.Name] = true
		}
		for _, sp := range pl.subs {
			nm, nf := 0, 0
			for _, g := range sp.Groups {
				for _, it := range g.Items {
					if mv[it.Where+"\x00"+it.Name] {
						nm++
					} else if fl[it.Where+"\x00"+it.Name] {
						nf++
					}
				}
			}
			s.writeCleanHistory(sp, nm, nf)
		}
	} else {
		s.writeCleanHistory(pl, len(moved), len(failed))
	}
	return map[string]any{"moved": moved, "failed": failed, "place": trashPlaceForScreen}, nil
}

func (s *panelServer) writeCleanHistory(pl *cleanPlan, nMoved, nFailed int) {
	code, doc := "（記号なし）", "（書類名なし）"
	if pl.jobOK {
		code = "【" + pl.job.User + "】"
		doc = pl.job.Doc + "（" + pl.job.parenText() + "）"
	}
	if pl.Kind == "40" {
		doc += "・合格品"
	} else {
		doc += "・要相談"
	}
	line := time.Now().Format("2006/01/02 15:04") + "\t" + code + "\t" + doc + "\tごみ箱へ移した " + fmt.Sprint(nMoved) + "件"
	if nFailed > 0 {
		line += "（移せなかった " + fmt.Sprint(nFailed) + "件）"
	}
	path := filepath.Join(s.p.report, cleanHistoryName)
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

// ---------------- ⑥ 完成した書類を仕事ごとに ----------------

type doneJob struct {
	Folder     string     `json:"folder"`
	Label      string     `json:"label"`
	RestoredAt string     `json:"restoredAt,omitempty"`
	Files      []doneJSON `json:"files"`
	Gone       bool       `json:"gone,omitempty"` // 40 のフォルダはもうない
	t          time.Time
}

func (s *panelServer) doneJobs(cb *codeBook) ([]doneJob, []doneJSON) {
	p := s.p
	ld := s.readLedger()
	jobs := map[string]*doneJob{}
	for _, d := range listDirs(p.aiOut, ".") {
		if s.protectedReason("40", d, filepath.Join(p.aiOut, d)) != "" {
			continue
		}
		st, err := os.Stat(filepath.Join(p.aiOut, d, "元に戻し済み.txt"))
		if err != nil {
			continue
		}
		jobs[d] = &doneJob{Folder: d, Label: labelFor(d, cb), RestoredAt: wareki(st.ModTime()), Files: []doneJSON{}, t: st.ModTime()}
	}
	other := []doneJSON{}
	var byName map[string]map[string]bool
	for _, d := range doneInfo(p.out) {
		fset := ld.outFolder[s.hashFile(filepath.Join(p.out, d.Name))]
		if len(fset) != 1 {
			// 元に戻したあとExcelで書き足して保存すると中身（ハッシュ）が変わる。
			// そのときは、元に戻したときの確認リストに書いたファイル名で仕事を探す（R8.9.29）
			if byName == nil {
				byName = s.restoredNames(ld)
			}
			fset = byName[d.Name]
		}
		if len(fset) == 1 {
			var f string
			for k := range fset {
				f = k
			}
			j := jobs[f]
			if j == nil && safeName(f) && s.protectedReason("40", f, filepath.Join(p.aiOut, f)) == "" {
				if _, err := os.Stat(filepath.Join(p.aiOut, f)); os.IsNotExist(err) {
					j = &doneJob{Folder: f, Label: labelFor(f, cb), Files: []doneJSON{}, Gone: true, t: d.t}
					jobs[f] = j
				}
			}
			if j != nil {
				if strings.EqualFold(filepath.Ext(d.Name), ".docx") && isDayDocx(filepath.Join(p.out, d.Name)) {
					d.Keika = true
				}
				if strings.EqualFold(filepath.Ext(d.Name), ".xlsx") && isDayXlsx(filepath.Join(p.out, d.Name)) {
					d.KeikaX = true
				}
				if strings.EqualFold(filepath.Ext(d.Name), ".xlsx") && !d.KeikaX && isHyo1Xlsx(filepath.Join(p.out, d.Name)) {
					// 1表の入ったブックは、ケアプランの仕事のときだけボタンを出す（モニタリング・アセスメントの仕事のブックにも白紙の1表があるため。改善27の2・点検役 4）
					if jid, ok := jobOfFolder(f); !ok || jid.Doc == "ケアプラン" || wbSheetCount(filepath.Join(p.out, d.Name)) <= 3 {
						d.Hyo1 = true
					}
				}
				j.Files = append(j.Files, d)
				continue
			}
		}
		other = append(other, d)
	}
	out := []doneJob{}
	for _, j := range jobs {
		out = append(out, *j)
	}
	sort.SliceStable(out, func(a, b int) bool {
		if !out[a].t.Equal(out[b].t) {
			return out[a].t.After(out[b].t)
		}
		return out[a].Folder < out[b].Folder
	})
	return out, other
}

var reRestoredName = regexp.MustCompile(`2_完成 に「([^」<]+)」として置きました`)

// restoredNames: 元に戻したときの確認リスト（確認リスト_元に戻す_*.html）から、2_完成 に置いたファイル名 → 40 のフォルダ
func (s *panelServer) restoredNames(ld *cleanLedger) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for rep, folders := range ld.restoreReport {
		if rep == "" || rep != filepath.Base(rep) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.p.report, rep))
		if err != nil {
			continue
		}
		for _, m := range reRestoredName.FindAllStringSubmatch(string(b), -1) {
			n := html.UnescapeString(m[1])
			for f := range folders {
				addSet(out, n, f)
			}
		}
	}
	return out
}
