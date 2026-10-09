package main

// サーバーへ保存（改善8・R8.9.29 承認）
// ⑥ 完成した書類の仕事ごとに「サーバーへ保存」→ 確認画面 →「保存する」を押したときだけ、
// 事務所サーバーの利用者フォルダ（既定 \\server\share\利用者\ふりがな_氏名）へコピーする。
// 守ること：サーバーでは何も消さない・上書きしない（同じ名前があれば日付付きの別名）。
//           書くのは設定した「利用者」「利用者（終了）」の中だけ。利用者フォルダが2つ以上見つかったら保存しない。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	serverSettingName  = "設定_サーバー保存.txt"
	serverHistoryName  = "サーバー保存履歴.txt"
	defaultServerBase  = `\\server\share\利用者`
	defaultServerEnded = `\\server\share\利用者（終了）`
)

var reReading = regexp.MustCompile(`^[ぁ-ゖ][ぁ-ゖー]{0,19}$`)

type serverSetting struct {
	Base  string `json:"base"`
	Ended string `json:"ended"`
}

func (s *panelServer) serverSettings() serverSetting {
	st := serverSetting{Base: defaultServerBase, Ended: defaultServerEnded}
	if v := os.Getenv("MASKTOOL_SERVER_BASE"); v != "" {
		st.Base = v
	}
	if v := os.Getenv("MASKTOOL_SERVER_ENDED"); v != "" {
		st.Ended = v
	}
	if b, err := os.ReadFile(filepath.Join(s.p.work, serverSettingName)); err == nil {
		for _, line := range strings.Split(strings.TrimPrefix(string(b), bom), "\n") {
			line = strings.TrimSpace(line)
			if k, v, ok := strings.Cut(line, "="); ok {
				v = strings.TrimSpace(v)
				switch strings.TrimSpace(k) {
				case "利用者フォルダ":
					if v != "" {
						st.Base = v
					}
				case "終了した方のフォルダ":
					if v != "" {
						st.Ended = v
					}
				}
			}
		}
	}
	return st
}

type srvFile struct {
	Name    string     `json:"name"`
	At      string     `json:"at,omitempty"`
	Checked bool       `json:"checked"`
	Append  *srvAppend `json:"append,omitempty"` // 支援経過（1日分）：足す先のExcelがあるとき（改善28 No.50）／新しく作る「支援経過_氏名.xlsx」（改善29 No.57）
	Merge   *srvMerge  `json:"merge,omitempty"`  // 支援経過（1日分）：今ある1日分のExcelを「支援経過_氏名.xlsx」にまとめるとき（改善29 No.57）
}

// srvMove: 利用者フォルダの一番上にある「元にした資料_…」を「元にした資料」の中へ移す予定（改善28 No.52）
type srvMove struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Conflict bool   `json:"conflict"` // 同じ名前がもうあるので移さない
}

type srvFound struct {
	Where string `json:"where"` // base / ended
	Dir   string `json:"dir"`
	Name  string `json:"name"`
}

type srvPlan struct {
	ID      string        `json:"id"`
	Folder  string        `json:"folder"`
	Label   string        `json:"label"`
	Name    string        `json:"name"`
	Files   []srvFile     `json:"files"`
	Setting serverSetting `json:"setting"`
	BaseOK  bool          `json:"baseOK"`
	EndedOK bool          `json:"endedOK"`
	Found   []srvFound    `json:"found"`
	Reading string        `json:"reading"` // 呼び名から見つけたひらがな（あれば）
	NewName string        `json:"newName"` // 見つからないときに作るフォルダ名（ふりがなを入れると決まる）
	Problem string        `json:"problem,omitempty"`
	Srcs    []srvFile     `json:"srcs"`              // 元にした資料（置き換える前の元のファイル。改善27）
	SrcRoot string        `json:"srcRoot"`           // 元にした資料をまとめるフォルダの名前（改善28 No.52：利用者フォルダの中に1つだけ）
	SrcDir  string        `json:"srcDir"`            // その中に作るフォルダの名前（日付_書類名。例：R8.10.6_支援経過1日分R8.10.6）
	SrcMiss int           `json:"srcMiss,omitempty"` // この仕事で渡したのに元のファイルが見つからない数（改善27 試験役 R1）
	Moves   []srvMove     `json:"moves"`             // すでにある「元にした資料_…」を、次の保存で「元にした資料」の中へ移す予定（改善28 No.52）
	SrcProb string        `json:"srcProblem,omitempty"`
	job     jobID
	created time.Time
	appends map[string]*dayAppendPlan // 支援経過（1日分）：足す先（改善28 No.50）
}

// 50音の行フォルダ（R8.9.29 決定：行ごと。ら行のフォルダは作らない → ら行の方は「わ」へ）
var gyouFolders = []string{"あ", "か", "さ", "た", "な", "は", "ま", "や", "わ"}

var gyouRows = []struct{ head, chars string }{
	{"あ", "ぁあぃいぅうぇえぉおゔ"},
	{"か", "かがきぎくぐけげこごゕゖ"},
	{"さ", "さざしじすずせぜそぞ"},
	{"た", "ただちぢっつづてでとど"},
	{"な", "なにぬねの"},
	{"は", "はばぱひびぴふぶぷへべぺほぼぽ"},
	{"ま", "まみむめも"},
	{"や", "ゃやゅゆょよ"},
	{"わ", "らりるれろゎわゐゑをん"},
}

// ふりがなの1文字目から行フォルダを決める（決められなければ ""）
func gyouOf(reading string) string {
	for _, c := range reading {
		for _, g := range gyouRows {
			if strings.ContainsRune(g.chars, c) {
				return g.head
			}
		}
		return ""
	}
	return ""
}

// 利用者フォルダ名の「_」のあと（空白を除いて）が氏名と同じもの。
// 置き場所の直下（前の形）と、1文字の行フォルダ（あ・か・さ…。ほかの1文字フォルダも見る）の中を探す。
// 返す名前は置き場所からの相対（例：は\はやし_林 一郎）
func findUserDirs(root, name string) []string {
	var out []string
	ents, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	want := normName(name)
	match := func(n string) bool {
		if i := strings.Index(n, "_"); i >= 0 {
			n = n[i+1:]
		}
		return normName(n) == want
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		n := e.Name()
		if match(n) {
			out = append(out, n)
			continue
		}
		if utf8.RuneCountInString(n) == 1 {
			sub, err := os.ReadDir(filepath.Join(root, n))
			if err != nil {
				continue
			}
			for _, e2 := range sub {
				if e2.IsDir() && match(e2.Name()) {
					out = append(out, filepath.Join(n, e2.Name()))
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func (s *panelServer) serverPreview(r *http.Request) (any, error) {
	var q struct{ Folder string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	if !safeName(q.Folder) {
		return nil, fmt.Errorf("名前が正しくありません")
	}
	job, ok := parseJobFolder(reFixSuffix.ReplaceAllString(q.Folder, "")) // 「・修正1」などの直した版も同じ仕事として扱う（改善10の4）
	if !ok {
		return nil, fmt.Errorf("利用者の書類ではないので、サーバーへは保存しません")
	}
	cb := s.codeBook()
	jobs, _ := s.doneJobs(cb)
	var dj *doneJob
	for i := range jobs {
		if jobs[i].Folder == q.Folder {
			dj = &jobs[i]
		}
	}
	if dj == nil || len(dj.Files) == 0 {
		return nil, fmt.Errorf("2_完成 に、この仕事の書類が見つかりません（先に「元に戻す」を押してください）")
	}
	rows, err := s.nameRowsWithCodes()
	if err != nil {
		return nil, err
	}
	var row *nameRow
	for i := range rows {
		if rows[i].Code == job.User && rows[i].Kind == "利用者" {
			if row != nil {
				return nil, fmt.Errorf("氏名一覧に【%s】の利用者が2人以上います。氏名一覧を直してから、もう一度押してください", job.User)
			}
			row = &rows[i]
		}
	}
	if row == nil {
		return nil, fmt.Errorf("氏名一覧に【%s】の利用者が見つかりません", job.User)
	}
	st := s.serverSettings()
	pl := &srvPlan{Folder: q.Folder, Label: dj.Label, Name: row.Name, Setting: st, Files: []srvFile{}, Found: []srvFound{}, job: job}
	for _, f := range dj.Files {
		ext := strings.ToLower(filepath.Ext(f.Name))
		pl.Files = append(pl.Files, srvFile{Name: f.Name, At: f.At, Checked: ext == ".xlsx" || ext == ".xlsm"})
	}
	pl.Srcs = []srvFile{}
	for _, n := range s.jobSources(job) {
		pl.Srcs = append(pl.Srcs, srvFile{Name: n, Checked: true})
	}
	pl.SrcRoot, pl.SrcDir = srcRootName, srcDirName(job, time.Now())
	pl.Moves = []srvMove{}
	if _, mats, guessed := s.jobMaterials(job); !guessed && len(mats) > len(pl.Srcs) {
		pl.SrcMiss = len(mats) - len(pl.Srcs)
	}
	pl.BaseOK = dirExists(st.Base)
	pl.EndedOK = dirExists(st.Ended)
	if !pl.BaseOK {
		pl.Problem = "サーバーの利用者フォルダ（" + st.Base + "）が見つかりません。サーバーにつながっているか、フォルダを作ってあるかを確かめてください"
	}
	if pl.BaseOK {
		for _, d := range findUserDirs(st.Base, row.Name) {
			pl.Found = append(pl.Found, srvFound{"base", filepath.Join(st.Base, d), d})
		}
	}
	if pl.EndedOK {
		for _, d := range findUserDirs(st.Ended, row.Name) {
			pl.Found = append(pl.Found, srvFound{"ended", filepath.Join(st.Ended, d), d})
		}
	}
	if len(pl.Found) > 1 {
		pl.Problem = "この方の利用者フォルダが2つ以上見つかりました。どちらに入れるか決められないので保存しません。フォルダを1つにしてから、もう一度押してください"
	}
	if len(pl.Found) == 1 && pl.Problem == "" {
		// 改善28 No.50：支援経過（1日分）は、すでに「支援経過1日分_…xlsx」があれば、そのファイルにシートを足す
		for i, f := range dj.Files {
			if f.KeikaX {
				if ap := s.planDayAppend(pl.Found[0].Dir, f.Name, row.Name); ap != nil {
					if pl.appends == nil {
						pl.appends = map[string]*dayAppendPlan{}
					}
					pl.appends[f.Name] = ap
					pl.Files[i].Append = ap.info
					if ap.merge != nil {
						pl.Files[i].Merge = ap.merge.info
					}
					pl.Files[i].Checked = ap.info.Blocked == "" && ap.cands == nil
				}
			}
		}
		// 改善28 No.52：すでにある「元にした資料_…」（利用者フォルダのいちばん上）は、次の保存で「元にした資料」の中へ移す
		pl.Moves, pl.SrcProb = oldSrcMoves(pl.Found[0].Dir)
	}
	if len(pl.Found) == 0 && pl.Problem == "" && pl.BaseOK {
		// 利用者フォルダがまだない方：保存のときにフォルダを作る。支援経過（1日分）は、そこに「支援経過_氏名.xlsx」を新しく作る（改善29 No.57）
		for i, f := range dj.Files {
			if !f.KeikaX {
				continue
			}
			data, err := os.ReadFile(filepath.Join(s.p.out, f.Name))
			if err != nil {
				continue
			}
			day, err := parseDayXlsx(data)
			if err != nil {
				continue
			}
			sum := sha256.Sum256(data)
			ap := s.planCreateBook(bookNameFor("", row.Name), data, day, hex.EncodeToString(sum[:]))
			if pl.appends == nil {
				pl.appends = map[string]*dayAppendPlan{}
			}
			pl.appends[f.Name] = ap
			pl.Files[i].Append = ap.info
			pl.Files[i].Checked = ap.info.Blocked == ""
		}
	}
	for _, a := range aliasList(row.Alias) {
		// フォルダ名は「ひらがなの姓」だけ（例：さとう_佐藤 一郎）。呼び名が「さとう いちろう」なら最初の語を使う
		if fs := strings.Fields(strings.ReplaceAll(a, "　", " ")); len(fs) > 0 {
			a = fs[0]
		}
		if reReading.MatchString(a) {
			pl.Reading = a
			break
		}
	}
	pl.NewName = cleanDirName(row.Name)
	pl.ID = newPlanID()
	pl.created = time.Now()
	if s.srvPlans == nil {
		s.srvPlans = map[string]*srvPlan{}
	}
	for id, x := range s.srvPlans {
		if time.Since(x.created) > planLifetime {
			delete(s.srvPlans, id)
		}
	}
	s.srvPlans[pl.ID] = pl
	return pl, nil
}

var reBadDirChar = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]`)

func cleanDirName(n string) string {
	n = reBadDirChar.ReplaceAllString(strings.TrimSpace(n), "")
	return n
}

func sameFile(path string, data []byte) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	a := sha256.Sum256(data)
	return bytes.Equal(h.Sum(nil), a[:])
}

// 同じ名前があれば「名前（R8.9.29保存）.xlsx」「名前（R8.9.29保存2）.xlsx」…（上書きしない）
func freeName(dir, name string, now time.Time) string {
	if _, err := os.Lstat(filepath.Join(dir, name)); os.IsNotExist(err) {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	day := fmt.Sprintf("R%d.%d.%d保存", now.Year()-2018, int(now.Month()), now.Day())
	for i := 1; i < 100; i++ {
		tag := day
		if i > 1 {
			tag = fmt.Sprintf("%s%d", day, i)
		}
		n := base + "（" + tag + "）" + ext
		if _, err := os.Lstat(filepath.Join(dir, n)); os.IsNotExist(err) {
			return n
		}
	}
	return ""
}

// サーバーには、何があっても消す操作をしない（書きかけが残っても消さない）
func writeNewNoDelete(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644) // 上書きしない
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func strictlyWithin(root, p string) bool {
	return within(filepath.Clean(root), filepath.Clean(p)) && filepath.Clean(root) != filepath.Clean(p)
}

func (s *panelServer) serverSave(r *http.Request) (any, error) {
	var q struct {
		ID      string
		Files   []string
		Srcs    []string
		Reading string
		Modes   map[string]string // 支援経過（1日分）：ファイル名 → "append"（足す・初期値）／"new"（別のファイルとして保存。改善28 No.50）
		Merge   map[string]bool   // 支援経過（1日分）：ファイル名 → true なら、今ある1日分のExcelを「支援経過_氏名.xlsx」にまとめる（改善29 No.57。画面は初期値がまとめる）
		NoMove  bool              // true なら、すでにある「元にした資料_…」を移さない（改善28 No.52）
	}
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	pl, ok := s.srvPlans[q.ID]
	if !ok || time.Since(pl.created) > planLifetime {
		return nil, fmt.Errorf("確認画面が古くなりました。もう一度「サーバーへ保存」を押してください（何も保存していません）")
	}
	if pl.Problem != "" {
		return nil, fmt.Errorf("%s（何も保存していません）", pl.Problem)
	}
	if len(q.Files) == 0 && len(q.Srcs) == 0 {
		return nil, fmt.Errorf("保存するファイルを選んでください")
	}
	allowed := map[string]bool{}
	for _, f := range pl.Files {
		allowed[f.Name] = true
	}
	for _, f := range q.Files {
		if !allowed[f] || !safeName(f) {
			return nil, fmt.Errorf("確認画面にないファイルが送られてきました（何も保存していません）")
		}
	}
	allowedSrc := map[string]bool{}
	for _, f := range pl.Srcs {
		allowedSrc[f.Name] = true
	}
	for _, f := range q.Srcs {
		if !allowedSrc[f] || !safeName(f) {
			return nil, fmt.Errorf("確認画面にない元の資料が送られてきました（何も保存していません）")
		}
	}
	st := pl.Setting
	var dir, gdir string
	created := false
	if len(pl.Found) == 1 {
		dir = pl.Found[0].Dir
	} else {
		rd := strings.TrimSpace(q.Reading)
		if !reReading.MatchString(rd) {
			return nil, fmt.Errorf("フォルダを作るので、名字のふりがなを「ひらがな」で入れてください（例：さとう）")
		}
		if pl.NewName == "" {
			return nil, fmt.Errorf("氏名からフォルダ名を作れません")
		}
		g := gyouOf(rd)
		if g == "" {
			return nil, fmt.Errorf("ふりがなの1文字目から、あ・か・さ…のどの行か決められません（何も保存していません）")
		}
		gdir = filepath.Join(st.Base, g)
		dir = filepath.Join(gdir, rd+"_"+pl.NewName)
		if !dirExists(st.Base) {
			return nil, fmt.Errorf("サーバーの利用者フォルダ（%s）が見つかりません（何も保存していません）", st.Base)
		}
		if len(findUserDirs(st.Base, pl.Name)) > 0 {
			return nil, fmt.Errorf("この方のフォルダが、いまできています。もう一度「サーバーへ保存」を押してください（何も保存していません）")
		}
	}
	if !strictlyWithin(st.Base, dir) && !strictlyWithin(st.Ended, dir) {
		return nil, fmt.Errorf("保存先が決めた場所の外です（何も保存していません）")
	}
	if gdir != "" && !dirExists(gdir) {
		if !strictlyWithin(st.Base, gdir) {
			return nil, fmt.Errorf("保存先が決めた場所の外です（何も保存していません）")
		}
		if err := os.Mkdir(gdir, 0755); err != nil && !dirExists(gdir) {
			return nil, fmt.Errorf("行のフォルダ（%s）を作れませんでした：%v（何も保存していません）", filepath.Base(gdir), err)
		}
	}
	if !dirExists(dir) {
		if err := os.Mkdir(dir, 0755); err != nil {
			return nil, fmt.Errorf("利用者フォルダを作れませんでした：%v（何も保存していません）", err)
		}
		created = true
	}
	delete(s.srvPlans, q.ID)
	now := time.Now()
	type res struct {
		Name   string `json:"name"`
		SaveAs string `json:"saveAs,omitempty"`
		Note   string `json:"note,omitempty"`
	}
	saved, skipped, failed := []res{}, []res{}, []res{}
	appended := []dayAppendResult{}
	for _, f := range q.Files {
		data, err := os.ReadFile(filepath.Join(s.p.out, f))
		if err != nil {
			failed = append(failed, res{Name: f, Note: "2_完成 のファイルを読めませんでした"})
			continue
		}
		// 2_完成 で重なって付いた「(2)」は、サーバーでは付けない（改善10の5）
		base := reDupSuffix.ReplaceAllString(f, "$1")
		if sameFile(filepath.Join(dir, base), data) || sameFile(filepath.Join(dir, f), data) {
			skipped = append(skipped, res{Name: f, Note: "同じ中身のファイルが、もう保存されています"})
			continue
		}
		// 改善28 No.50：支援経過（1日分）で足す先のExcelがあれば、新しいファイルを作らずシートを足す
		ap := pl.appends[f]
		if ap != nil && ap.cands != nil && q.Modes[f] != "new" {
			// 足す先が2つ以上：確認画面で選ばれたファイルにだけ足す。選ばれていなければ足さない（保存もしない）
			t := strings.TrimPrefix(q.Modes[f], "append:")
			if !strings.HasPrefix(q.Modes[f], "append:") || ap.cands[t] == nil {
				skipped = append(skipped, res{Name: f, Note: "足す先のExcelが選ばれていないので、足していません（何も書いていません）"})
				continue
			}
			ap = ap.cands[t]
		}
		if ap != nil && q.Modes[f] != "new" {
			if ap.info.Dup != "" {
				skipped = append(skipped, res{Name: f, Note: ap.info.Blocked})
				continue
			}
			if ap.info.Blocked != "" {
				failed = append(failed, res{Name: f, Note: "足せないため、保存しませんでした（" + ap.info.Blocked + "）。別のファイルとして保存するときは、確認画面で選んでください"})
				continue
			}
			var r *dayAppendResult
			var err error
			switch {
			case ap.info.Kind == "create":
				r, err = s.doCreateBook(dir, f, ap, now)
			case ap.merge != nil && q.Merge[f]:
				r, err = s.doMerge(dir, f, ap, now)
			default:
				r, err = s.doDayAppend(dir, f, ap, now)
			}
			if err != nil {
				failed = append(failed, res{Name: f, Note: err.Error()})
				continue
			}
			appended = append(appended, *r)
			if r.Kind != "" {
				s.writeBookHistory(pl.job.User, r.Kind, r.Sheet)
			} else {
				s.writeDayAppendHistory(pl.job.User, r.Sheet)
			}
			continue
		}
		n := freeName(dir, base, now)
		if n == "" {
			failed = append(failed, res{Name: f, Note: "空いている名前が見つかりませんでした"})
			continue
		}
		if err := writeNewNoDelete(filepath.Join(dir, n), data); err != nil {
			failed = append(failed, res{Name: f, Note: "保存できませんでした：" + err.Error()})
			continue
		}
		it := res{Name: f}
		if n != f {
			it.SaveAs = n
			it.Note = "「" + n + "」の名前で保存しました"
		}
		if n != base {
			it.SaveAs = n
			it.Note = "同じ名前のファイルがあったので、別の名前で保存しました（前のものはそのまま）"
		}
		saved = append(saved, it)
	}
	// 元にした資料（改善27）：利用者フォルダの中の「元にした資料_R8.10.6_モニタリング」へ。
	// 同じ中身のファイルが利用者フォルダのどこかにもうあれば置かない（「すでにあり」）
	srcSaved, srcSkipped := []res{}, []res{}
	moved, moveSkipped := []srvMove{}, []srvMove{}
	if !q.NoMove && len(pl.Moves) > 0 {
		moved, moveSkipped = moveOldSrcDirs(dir, pl.Moves, now)
	}
	if len(q.Srcs) > 0 {
		idx := sizeIndex(dir)
		root := filepath.Join(dir, pl.SrcRoot)
		sub := filepath.Join(root, pl.SrcDir)
		for _, f := range q.Srcs {
			data, err := os.ReadFile(filepath.Join(s.p.done, f))
			if err != nil {
				failed = append(failed, res{Name: f, Note: "元の資料（1_元データ＼処理済み）を読めませんでした"})
				continue
			}
			if where := idx.same(data); where != "" {
				srcSkipped = append(srcSkipped, res{Name: f, Note: "同じ中身のファイルが、サーバーにもうあります（" + where + "）"})
				continue
			}
			if !strictlyWithin(dir, root) || !strictlyWithin(root, sub) {
				failed = append(failed, res{Name: f, Note: "保存先が決めた場所の外です"})
				continue
			}
			if st, err := os.Lstat(root); err == nil && !st.IsDir() {
				failed = append(failed, res{Name: f, Note: "「" + pl.SrcRoot + "」という名前のファイルがあるため、フォルダを作れません"})
				continue
			}
			if !dirExists(root) {
				if err := os.Mkdir(root, 0755); err != nil && !dirExists(root) {
					failed = append(failed, res{Name: f, Note: "「" + pl.SrcRoot + "」のフォルダを作れませんでした：" + err.Error()})
					continue
				}
			}
			if !dirExists(sub) {
				if err := os.Mkdir(sub, 0755); err != nil && !dirExists(sub) {
					failed = append(failed, res{Name: f, Note: "「" + pl.SrcRoot + "＼" + pl.SrcDir + "」のフォルダを作れませんでした：" + err.Error()})
					continue
				}
			}
			n := freeName(sub, f, now)
			if n == "" {
				failed = append(failed, res{Name: f, Note: "空いている名前が見つかりませんでした"})
				continue
			}
			if err := writeNewNoDelete(filepath.Join(sub, n), data); err != nil {
				failed = append(failed, res{Name: f, Note: "保存できませんでした：" + err.Error()})
				continue
			}
			it := res{Name: f}
			if n != f {
				it.SaveAs = n
				it.Note = "同じ名前のファイルがあったので、別の名前で保存しました（前のものはそのまま）"
			}
			srcSaved = append(srcSaved, it)
			idx.add(filepath.Join(sub, n), data)
		}
	}
	s.writeServerHistory(pl, len(saved)+len(appended)+len(srcSaved), len(skipped)+len(srcSkipped), len(failed))
	return map[string]any{"dir": dir, "created": created, "saved": saved, "skipped": skipped, "failed": failed, "appended": appended,
		"srcRoot": pl.SrcRoot, "srcDir": pl.SrcDir, "srcSaved": srcSaved, "srcSkipped": srcSkipped, "moved": moved, "moveSkipped": moveSkipped}, nil
}

func (s *panelServer) writeServerHistory(pl *srvPlan, nSaved, nSkipped, nFailed int) {
	line := time.Now().Format("2006/01/02 15:04") + "\t【" + pl.job.User + "】\t" + pl.job.Doc + "（" + pl.job.parenText() + "）\t保存 " + fmt.Sprint(nSaved) + "件"
	if nSkipped > 0 {
		line += "・同じもの " + fmt.Sprint(nSkipped) + "件"
	}
	if nFailed > 0 {
		line += "・保存できず " + fmt.Sprint(nFailed) + "件"
	}
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

// 保存先の置き場所を変える（設定ファイルに書く。マスキング作業の中）
func (s *panelServer) serverSetting(r *http.Request) (any, error) {
	var q struct{ Base, Ended string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	q.Base, q.Ended = strings.TrimSpace(q.Base), strings.TrimSpace(q.Ended)
	for _, v := range []string{q.Base, q.Ended} {
		if v == "" || strings.Contains(v, "..") || !(strings.HasPrefix(v, `\\`) || filepath.IsAbs(v)) {
			return nil, fmt.Errorf("置き場所は「\\\\server\\share\\利用者」のように、\\\\ から始まる形で入れてください")
		}
	}
	body := bom + "利用者フォルダ=" + q.Base + "\r\n終了した方のフォルダ=" + q.Ended + "\r\n"
	if err := os.WriteFile(filepath.Join(s.p.work, serverSettingName), []byte(body), 0600); err != nil {
		return nil, err
	}
	return map[string]string{"ok": "保存先の置き場所を変えました"}, nil
}

var reFixSuffix = regexp.MustCompile(`・修正\d+$`)

var reDupSuffix = regexp.MustCompile(`\(\d+\)(\.[A-Za-z]+)$`)

// ---- 元にした資料（改善27） ----

// 元にした資料は、利用者フォルダの中の「元にした資料」フォルダ1つにまとめる（改善28 No.52）
const (
	srcRootName   = "元にした資料"
	srcOldPrefix  = "元にした資料_" // 改善27のときの名前（利用者フォルダのいちばん上に、保存のたびにできていた）
	srcMoveRecord = "元にした資料の移動記録"
)

// srcDirName: 「元にした資料」の中に作るフォルダの名前（日付_書類名。例：R8.10.6_モニタリング）
func srcDirName(j jobID, now time.Time) string {
	return fmt.Sprintf("R%d.%d.%d_%s", now.Year()-2018, int(now.Month()), now.Day(), j.Doc)
}

// oldSrcMoves: 利用者フォルダのいちばん上にある「元にした資料_…」のフォルダと、移す先（「元にした資料」の中の、頭の「元にした資料_」を取った名前）。
// 同じ名前がもうあれば Conflict（移さずそのまま残して知らせる）
func oldSrcMoves(dir string) ([]srvMove, string) {
	out := []srvMove{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out, ""
	}
	root := filepath.Join(dir, srcRootName)
	prob := ""
	if st, err := os.Lstat(root); err == nil && !st.IsDir() {
		prob = "「" + srcRootName + "」という名前のファイルがあるため、「元にした資料」のフォルダを作れません。そのファイルの名前を変えてから、もう一度開いてください（移さず、資料も入れません）"
	}
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() || !strings.HasPrefix(n, srcOldPrefix) || n == srcOldPrefix {
			continue
		}
		if st, err := os.Lstat(filepath.Join(dir, n)); err != nil || !st.IsDir() {
			continue // リンクなどは動かさない
		}
		to := strings.TrimPrefix(n, srcOldPrefix)
		m := srvMove{From: n, To: to}
		if prob != "" {
			m.Conflict = true
		} else if _, err := os.Lstat(filepath.Join(root, to)); err == nil {
			m.Conflict = true
		}
		out = append(out, m)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].From < out[b].From })
	return out, prob
}

// moveOldSrcDirs: 確認画面で知らせた「元にした資料_…」を「元にした資料」の中へ移す。
// 名前の変更だけ（同じ場所の中での移し替え）で、中のファイルは触らない・消さない。同じ名前があれば移さずに残す。
// 移す前に、移す前と移した後の名前を「控え」に書き残す（元に戻せるように）
func moveOldSrcDirs(dir string, planned []srvMove, now time.Time) (moved, skipped []srvMove) {
	moved, skipped = []srvMove{}, []srvMove{}
	root := filepath.Join(dir, srcRootName)
	if !strictlyWithin(dir, root) {
		return
	}
	var todo []srvMove
	for _, m := range planned {
		if m.From == "" || m.From != filepath.Base(m.From) || !strings.HasPrefix(m.From, srcOldPrefix) || m.To != strings.TrimPrefix(m.From, srcOldPrefix) || m.To == "" {
			continue
		}
		src := filepath.Join(dir, m.From)
		if st, err := os.Lstat(src); err != nil || !st.IsDir() {
			continue
		}
		if st, err := os.Lstat(root); err == nil && !st.IsDir() {
			m.Conflict = true
			skipped = append(skipped, m)
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, m.To)); err == nil {
			m.Conflict = true
			skipped = append(skipped, m)
			continue
		}
		todo = append(todo, m)
	}
	if len(todo) == 0 {
		return
	}
	// 控え：移す前・移した後の名前（元に戻せるように）
	var rec strings.Builder
	rec.WriteString("利用者フォルダの中の「元にした資料_…」を、「元にした資料」の中へ移しました（名前の変更だけ。中のファイルは触っていません）。\r\n元に戻すときは、「元にした資料」の中のフォルダを、下の「移す前」の名前にして、利用者フォルダのいちばん上へ戻してください。\r\n\r\n")
	for _, m := range todo {
		rec.WriteString("移す前：" + m.From + "\r\n移した後：" + srcRootName + "＼" + m.To + "\r\n\r\n")
	}
	bdir := filepath.Join(dir, keikaBackupDir)
	if err := os.MkdirAll(bdir, 0755); err != nil {
		for _, m := range todo {
			m.Conflict = true
			skipped = append(skipped, m)
		}
		return
	}
	if _, err := writeNoDelete(bdir, now.Format("20060102_150405")+"_"+srcMoveRecord+".txt", []byte(bom+rec.String())); err != nil {
		for _, m := range todo {
			m.Conflict = true
			skipped = append(skipped, m)
		}
		return
	}
	if !dirExists(root) {
		if err := os.Mkdir(root, 0755); err != nil && !dirExists(root) {
			for _, m := range todo {
				m.Conflict = true
				skipped = append(skipped, m)
			}
			return
		}
	}
	for _, m := range todo {
		if err := os.Rename(filepath.Join(dir, m.From), filepath.Join(root, m.To)); err != nil {
			m.Conflict = true
			skipped = append(skipped, m)
			continue
		}
		moved = append(moved, m)
	}
	return
}

// jobSources: その仕事でAIに渡した資料の、置き換える前の元のファイル（1_元データ＼処理済み の中。片づけ用の控えのハッシュで決める）
func (s *panelServer) jobSources(j jobID) []string {
	ld := s.readLedger()
	key := j.key()
	stage := map[string]bool{}
	for h, keys := range ld.stageKey {
		if keys[key] {
			stage[h] = true
		}
	}
	if len(stage) == 0 {
		return nil
	}
	src := map[string]bool{}
	for sh, sts := range ld.srcStage {
		for st := range sts {
			if stage[st] {
				src[sh] = true
			}
		}
	}
	var out []string
	seen := map[string]int{}
	ents, err := os.ReadDir(s.p.done)
	if err != nil {
		return nil
	}
	for _, e := range ents {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "~$") {
			continue
		}
		h := s.hashFile(filepath.Join(s.p.done, e.Name()))
		if src[h] {
			if prev, ok := seen[h]; ok {
				// 同じ中身が2つ（同じファイルを何度か置き換えた）なら、短い名前の1つだけ
				if len(e.Name()) < len(out[prev]) {
					out[prev] = e.Name()
				}
				continue
			}
			seen[h] = len(out)
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// sizeIndex: 利用者フォルダの中のファイル（3階層・5000件まで）を大きさで引けるようにする。同じ大きさのものだけ中身を比べる
type sizeIdx struct {
	root   string
	bySize map[int64][]string
}

func sizeIndex(root string) *sizeIdx {
	ix := &sizeIdx{root: root, bySize: map[int64][]string{}}
	n := 0
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if rel, _ := filepath.Rel(root, p); rel != "." && strings.Count(rel, string(filepath.Separator)) >= 3 {
				return filepath.SkipDir
			}
			return nil
		}
		if n++; n > 5000 {
			return filepath.SkipAll
		}
		if st, err := d.Info(); err == nil && st.Mode().IsRegular() {
			ix.bySize[st.Size()] = append(ix.bySize[st.Size()], p)
		}
		return nil
	})
	return ix
}

func (ix *sizeIdx) same(data []byte) string {
	for _, p := range ix.bySize[int64(len(data))] {
		if sameFile(p, data) {
			rel, _ := filepath.Rel(ix.root, p)
			return rel
		}
	}
	return ""
}

func (ix *sizeIdx) add(p string, data []byte) {
	ix.bySize[int64(len(data))] = append(ix.bySize[int64(len(data))], p)
}
