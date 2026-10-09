package main

// AI作業パネル：このパソコンの中だけで動くブラウザ画面（127.0.0.1 のみで待ち受け）

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed panel.html
var panelHTML string

type panelServer struct {
	p      *paths
	token  string
	host   string
	mu     sync.Mutex
	last   *maskReport
	extDir string                // exe のあるフォルダ（使い方.html）
	plans  map[string]*cleanPlan // 片づけ：確認画面に出した一覧（ごみ箱へ移すときに同じものか確かめる）
	hcache map[string]string     // 片づけ：ファイルのハッシュの控え（場所・大きさ・日時が同じなら計算し直さない）
	// 支援経過：確認画面に出した書き足しの計画（書き足すときに同じものか確かめる）
	keikaPlans  map[string]*keikaPlan
	keikaXPlans map[string]*keikaXPlan // 支援経過（Excel）を利用者別 .xlsm に書き足す確認画面（改善18）
	hyo1Plans   map[string]*hyo1Plan   // 完成した第1表にカイポケの情報を入れる確認画面（改善19）
	srvPlans    map[string]*srvPlan    // サーバーへ保存：確認画面に出した計画
}

type seg struct {
	T    string `json:"t"`
	Cls  string `json:"c,omitempty"` // rep / sus
	Orig string `json:"o,omitempty"`
}

type susJSON struct {
	Word    string `json:"word"`
	Name    string `json:"name"` // 氏名一覧に書く候補（敬称を外したもの）
	Context string `json:"context"`
	Reason  string `json:"reason"`
	Kind    string `json:"kind"`              // name / addr / other
	Reg     string `json:"reg,omitempty"`     // 住所として登録する語（改善6）
	Garbled bool   `json:"garbled,omitempty"` // 読み取りで字が化けた名前かもしれない
	Ex      string `json:"ex,omitempty"`      // 「除外」に入れる語（除外で消えるものだけ。改善12 No.34）
	Common  bool   `json:"common,omitempty"`  // よく出る語（職種・地名。改善12 No.34）
	// 現場試験 T1・T2（R8.10.1）
	Key   string `json:"key,omitempty"`   // 同じものを1つの箱にまとめる鍵（住所は都道府県の有無を問わない・名前は前に付いた職種・事業所の語を除く）
	Known string `json:"known,omitempty"` // すでに氏名一覧にある語（「氏名一覧の 12 行目（その他）」）。判断の数に入れない
	Orig  string `json:"orig,omitempty"`  // スペースを入れる前の名前（「除外」は書類に書かれているとおりの字で登録するため。T2）
	Staff bool   `json:"staff,omitempty"` // 前後に職種・事業所の語がある名前（まとめて「その他」の候補。現場試験 T1）
	Fam   bool   `json:"fam,omitempty"`   // 前後に続柄の語がある名前（まとめての候補にしない）
	// 改善22：伏せ字・名前の一部
	Fix    string    `json:"fix,omitempty"`    // 呼び名に足す書き方（元の字で。「山〇太〇」「長太郎」）
	Owners []nameRow `json:"owners,omitempty"` // だれの名前か（候補の方。1人なら、ボタン1回で呼び名に足せる）
	Part   bool      `json:"part,omitempty"`   // 名前の記号のすぐ前後に残った字（記号の持ち主が候補）
	Place  string    `json:"place,omitempty"`  // 事業所名・地名の形の中にある、登録した方のかなの名前（「さくら草」の「さくら」。改善23 E1）
	Self   bool      `json:"self,omitempty"`   // 除外に登録した方（自分の事業所の職員）と同じ姓（改善23）
	Birth  bool      `json:"birth,omitempty"`  // 生年月日の欄の崩れた日付（このファイルの利用者のボタンで呼び名に足す。改善26）
	Read   bool      `json:"read,omitempty"`   // フリガナの欄の名前の読み（このファイルの利用者のボタンで呼び名に足す。改善27）
}

type fileJSON struct {
	Name       string     `json:"name"`
	OutName    string     `json:"outName"`
	Status     string     `json:"status"` // ok / held / err
	Err        string     `json:"err,omitempty"`
	Count      int        `json:"count"`
	PDF        bool       `json:"pdf"`
	Pages      int        `json:"pages"`
	EmptyPages []int      `json:"emptyPages,omitempty"`
	PoorPages  []int      `json:"poorPages,omitempty"`
	Media      int        `json:"media"`
	Suspects   []susJSON  `json:"suspects"`
	Preview    [][]seg    `json:"preview"`
	Lefts      []leftHit  `json:"lefts,omitempty"`  // 赤：残った所（改善12 No.33）
	Guides     []fixGuide `json:"guides,omitempty"` // 赤：原因と直し方（改善12 No.36）
	Users      []string   `json:"users,omitempty"`  // このファイルに出てくる利用者の記号
}

type maskReport struct {
	At      string     `json:"at"`
	Files   []fileJSON `json:"files"`
	Skipped []string   `json:"skipped"`
	Aside   *asideJSON `json:"aside,omitempty"` // 前の方の残りを脇によけた知らせ（改善12 No.44）
	// Pend: この置き換えのあとに、②の箱から氏名一覧へ書いた登録・除外の件数（まだ置き換え直していない分。改善14 指摘帳15）。
	// last.json にも書くので、パネルを閉じても「まとめて置き換え直す」の件数が残る
	Pend int `json:"pend"`
	// Ver: この結果を作ったパネルの版。版が変わったら②を開いたときに自動で置き換え直す（改善26）
	Ver string `json:"ver,omitempty"`
}

// panelVer: パネルの版（直すたびに変える）
const panelVer = "改善27の2"

// bumpPend: 登録・除外を書いたら、まだ反映していない件数を増やす（置き換え直すと 0 に戻る）
func (s *panelServer) bumpPend(n int) int {
	if s.last == nil || n <= 0 {
		if s.last == nil {
			return n
		}
		return s.last.Pend
	}
	s.last.Pend += n
	if b, err := json.Marshal(s.last); err == nil {
		os.WriteFile(filepath.Join(s.p.report, "last.json"), b, 0600)
	}
	return s.last.Pend
}

var reHonorTail = regexp.MustCompile(`[ 　]?(様|さま|さん|氏|殿|ちゃん|くん|君|先生|医師)$`)

func runPanel(p *paths) error {
	if _, err := os.Stat(p.nameList); os.IsNotExist(err) {
		if err := os.WriteFile(p.nameList, templateNameList, 0600); err != nil {
			return err
		}
	}
	b := make([]byte, 16)
	rand.Read(b)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("画面を開けませんでした: %v", err)
	}
	s := &panelServer{p: p, token: hex.EncodeToString(b), host: ln.Addr().String()}
	if exe, err := os.Executable(); err == nil {
		s.extDir = filepath.Dir(exe)
	}
	if data, err := os.ReadFile(filepath.Join(p.report, "last.json")); err == nil {
		var r maskReport
		if json.Unmarshal(data, &r) == nil {
			s.last = &r
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.page)
	mux.HandleFunc("/api/state", s.api(s.state))
	mux.HandleFunc("/api/passed/poll", s.api(s.passedPoll)) // ④の自動更新（改善28 No.49）
	mux.HandleFunc("/api/download", s.api(s.downloadFiles))   // ⑥ 完成した書類をダウンロードフォルダへ写す（改善28 No.55）
	mux.HandleFunc("/api/upload", s.api(s.intakeWrap("", s.upload))) // 入れ方の記録（改善12 No.42）
	mux.HandleFunc("/api/mask", s.api(s.mask))
	mux.HandleFunc("/api/namelist", s.api(s.namelist))
	mux.HandleFunc("/api/send", s.api(s.send))
	mux.HandleFunc("/api/restore", s.api(s.restore))
	mux.HandleFunc("/api/open", s.api(s.open))
	mux.HandleFunc("/api/remove", s.api(s.remove))
	mux.HandleFunc("/api/newuser", s.api(s.newuser))
	mux.HandleFunc("/api/names/list", s.api(s.namesList))
	mux.HandleFunc("/api/names/add", s.api(s.namesAdd))
	mux.HandleFunc("/api/names/edit", s.api(s.namesEdit))
	mux.HandleFunc("/api/names/delete", s.api(s.namesDelete))
	mux.HandleFunc("/api/names/alias", s.api(s.namesAlias))
	mux.HandleFunc("/api/names/merge", s.api(s.namesMerge)) // 重複を1回でまとめる（改善22）
	mux.HandleFunc("/api/addr/add", s.api(s.addrAdd))
	mux.HandleFunc("/api/names/moveaddr", s.api(s.namesMoveAddr))
	mux.HandleFunc("/api/view", s.api(s.view))
	mux.HandleFunc("/api/paste", s.api(s.intakeWrap("貼り付け", s.paste)))
	mux.HandleFunc("/api/voice", s.api(s.voice))
	mux.HandleFunc("/api/clean/preview", s.api(s.cleanPreview))
	mux.HandleFunc("/api/clean/run", s.api(s.cleanRun))
	mux.HandleFunc("/api/clean/preview-multi", s.api(s.cleanPreviewMulti))
	mux.HandleFunc("/api/server/preview", s.api(s.serverPreview))
	mux.HandleFunc("/api/other/preview", s.api(s.otherPreview))
	mux.HandleFunc("/api/other/send", s.api(s.otherSend))
	mux.HandleFunc("/api/server/save", s.api(s.serverSave))
	mux.HandleFunc("/api/server/setting", s.api(s.serverSetting))
	mux.HandleFunc("/api/rename/scan", s.api(s.renameScan)) // サーバーの名前をそろえる（改善29 No.58）
	mux.HandleFunc("/api/rename/do", s.api(s.renameDo))
	mux.HandleFunc("/api/keika/today", s.api(s.intakeWrap(keikaDocLabel, s.keikaToday)))
	mux.HandleFunc("/api/keika/preview", s.api(s.keikaPreview))
	mux.HandleFunc("/api/keika/append", s.api(s.keikaAppend))
	mux.HandleFunc("/api/keika/xpreview", s.api(s.keikaXPreview))
	mux.HandleFunc("/api/keika/xappend", s.api(s.keikaXAppend))
	mux.HandleFunc("/api/hyo1/preview", s.api(s.hyo1Preview))
	mux.HandleFunc("/api/hyo1/apply", s.api(s.hyo1Apply))
	mux.HandleFunc("/api/kaipoke/status", s.api(s.kaipokeStatus)) // カイポケのCSVを入れる（改善20）
	mux.HandleFunc("/api/kaipoke/upload", s.api(s.kaipokeUpload))
	mux.HandleFunc("/api/kaipoke/trash", s.api(s.kaipokeTrash))
	mux.HandleFunc("/api/keika/extract", s.api(s.intakeWrap("支援経過一覧の切り出し", s.keikaExtract)))
	mux.HandleFunc("/api/keika/info", s.api(s.keikaInfo))
	mux.HandleFunc("/api/keika/upload", s.api(s.keikaUpload))
	mux.HandleFunc("/api/keika/rewrite", s.api(s.keikaRewrite))
	mux.HandleFunc("/api/fix/prepare", s.api(s.intakeWrap("⑥直してもらう", s.fixPrepare)))
	mux.HandleFunc("/api/aside/restore", s.api(s.asideRestore)) // 前の方の残りを戻す（改善12 No.44）
	mux.HandleFunc("/api/errlog", s.api(s.errlogAPI))           // 画面のエラーの記録（改善12 No.43）
	mux.HandleFunc("/api/names/exclude", s.api(s.namesExclude)) // まとめて除外（改善12 No.33・34）
	mux.HandleFunc("/api/kaizen/add", s.api(s.kaizenAdd))       // 気づいたことを書く（改善13 No.3）
	mux.HandleFunc("/api/names/addmany", s.api(s.namesAddMany)) // まとめて「その他」（現場試験 T1）
	url := "http://" + s.host + "/?t=" + s.token
	fmt.Println()
	fmt.Println("ブラウザで画面を開きます。開かないときは、次のアドレスをブラウザに貼り付けてください：")
	fmt.Println("  " + url)
	fmt.Println()
	fmt.Println("※ この黒い画面を閉じると、AI作業パネルも終わります。作業中は閉じないでください。")
	if f := os.Getenv("MASKTOOL_PANEL_URLFILE"); f != "" { // 試験用
		os.WriteFile(f, []byte(url), 0600)
	}
	openURL(url)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}

func openURL(target string) {
	if runtime.GOOS != "windows" || os.Getenv("MASKTOOL_NOPAUSE") != "" {
		return
	}
	exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
}

func openPath(target string, folder bool) {
	if runtime.GOOS != "windows" || os.Getenv("MASKTOOL_NOPAUSE") != "" {
		fmt.Println("（開く）", target)
		return
	}
	if folder {
		exec.Command("explorer", target).Start()
		return
	}
	exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
}

// ---- 共通 ----

func (s *panelServer) okHost(r *http.Request) bool { return r.Host == s.host }

func (s *panelServer) page(w http.ResponseWriter, r *http.Request) {
	if !s.okHost(r) || r.URL.Path != "/" || r.URL.Query().Get("t") != s.token {
		http.Error(w, "このページは開けません（AI作業パネルを起動し直してください）", 403)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
	io.WriteString(w, strings.Replace(panelHTML, "__TOKEN__", s.token, 1))
}

type apiFunc func(r *http.Request) (any, error)

func (s *panelServer) api(f apiFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.okHost(r) || r.Header.Get("X-Token") != s.token || r.Method != http.MethodPost {
			http.Error(w, "forbidden", 403)
			return
		}
		s.mu.Lock()
		v, err := f(r)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			// 操作のエラー（書き方の誤りなど）は 200 で {"error":…} を返す（ブラウザのコンソールを赤くしない）。守りの拒否は上の 403
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(v)
	}
}

// 「AIへのひとこと」「直してほしいこと」の上限（改善29 No.61）。超えたら黙って切らずに止める
const noteMaxRunes = 5000

func noteLengthErr(note string) error {
	if n := utf8.RuneCountInString(note); n > noteMaxRunes {
		return fmt.Errorf("「ひとこと」が長すぎます（%d字）。%d字までにしてください（何も渡していません）", n, noteMaxRunes)
	}
	return nil
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func listFiles(dir string) []string {
	out := []string{}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() || strings.HasPrefix(e.Name(), "~$") || strings.HasPrefix(e.Name(), ".") || strings.HasSuffix(e.Name(), ".part") {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func listDirs(dir string, skipPrefix string) []string {
	out := []string{}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() && (skipPrefix == "" || !strings.HasPrefix(e.Name(), skipPrefix)) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// 記号→元の名前（画面表示用。このパソコンの中だけ）
func (s *panelServer) codeBook() *codeBook {
	cb, err := loadCodeBook(s.p.codeBookF)
	if err != nil {
		return newCodeBook()
	}
	return cb
}

var reFolderUser = regexp.MustCompile(`^(利用者\d{3,})`)

func labelFor(folder string, cb *codeBook) string {
	if m := reFolderUser.FindStringSubmatch(folder); m != nil {
		if n, ok := cb.codeToOrig["【"+m[1]+"】"]; ok {
			return n + "（" + m[1] + "）　" + strings.TrimLeft(strings.TrimPrefix(folder, m[1]), "_")
		}
	}
	return folder
}

func restoreText(t string, cb *codeBook) string {
	ms, _ := restoreMatches(t, cb)
	return applyMatches(t, ms)
}

// ---- 状態 ----

type itemJSON struct {
	Folder   string   `json:"folder"`
	Label    string   `json:"label"`
	Files    []string `json:"files"`
	Info     []string `json:"info"`
	Memo     string   `json:"memo,omitempty"`
	Restored bool     `json:"restored"`
	At       string   `json:"at,omitempty"`         // 届いた日時（40）／止まった日時（50）
	RestAt   string   `json:"restoredAt,omitempty"` // 元に戻した日時
	Keep     string   `json:"keep,omitempty"`       // 保管.txt の1行目（AIが使う保管品。人の出番なし）
	t        time.Time
}

// wareki: 画面に出す日時（例：R8.9.27（日）15:40）
func wareki(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	w := "日月火水木金土"
	return fmt.Sprintf("R%d.%d.%d（%s）%02d:%02d", t.Year()-2018, int(t.Month()), t.Day(), string([]rune(w)[t.Weekday()]), t.Hour(), t.Minute())
}

// folderTime: 決まったファイル（判定記録.md・相談メモ.md）があればその更新日時、なければフォルダ内の一番新しいファイルの更新日時
func folderTime(dir, prefer string) time.Time {
	if st, err := os.Stat(filepath.Join(dir, prefer)); err == nil && !st.IsDir() {
		return st.ModTime()
	}
	var newest time.Time
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "~$") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		if st, err := os.Stat(dir); err == nil {
			newest = st.ModTime()
		}
	}
	return newest
}

type doneJSON struct {
	Name   string `json:"name"`
	At     string `json:"at"`
	Keika  bool   `json:"keika,omitempty"`  // 支援経過（1日分）の決まった形のWord（記録・一覧に書き足せる）
	KeikaX bool   `json:"keikax,omitempty"` // 支援経過（1日分）の新しい形のExcel（サーバーの利用者別 .xlsm に書き足せる・改善18）
	Hyo1   bool   `json:"hyo1,omitempty"`   // 第1表（Excel原紙）。カイポケの情報を入れられる（改善19）
	t      time.Time
}

var (
	reRound  = regexp.MustCompile(`(\d)回目\s*(?:で)?\s*(?:\*\*)?合格`)
	reYokaku = regexp.MustCompile(`要確認[^\n（(]*[（(](\d+)件[）)]`)
)

func (s *panelServer) state(r *http.Request) (any, error) {
	p := s.p
	cb := s.codeBook()
	users, err := s.userList()
	if err != nil {
		return nil, err
	}
	cb = s.codeBook()
	passed, nPassed := s.passedList(cb)
	consult := []itemJSON{}
	consultDir := filepath.Join(p.ai, "50_要相談")
	for _, d := range listDirs(consultDir, "マスキングツール") {
		dir := filepath.Join(consultDir, d)
		it := itemJSON{Folder: d, Label: labelFor(d, cb)}
		if b, err := os.ReadFile(filepath.Join(dir, "相談メモ.md")); err == nil {
			it.Memo = restoreText(string(b), cb)
		}
		it.t = folderTime(dir, "相談メモ.md")
		it.At = wareki(it.t)
		consult = append(consult, it)
	}
	// 新しい順
	sort.SliceStable(consult, func(a, b int) bool { return consult[a].t.After(consult[b].t) })
	src, _ := listTargets(p.src, true)
	if src == nil {
		src = []string{}
	}
	now := time.Now()
	var months []string
	for i := -1; i <= 1; i++ {
		t := now.AddDate(0, i, 0)
		months = append(months, fmt.Sprintf("R%d.%d", t.Year()-2018, int(t.Month())))
	}
	jobs, doneOther := s.doneJobs(cb)
	srcInfo, stageInfo := s.intakeInfo()
	return map[string]any{
		"srcInfo":    srcInfo,   // 入れた日時・入れ方（改善12 No.42）
		"stageInfo":  stageInfo, // 置き換え済みへ移っても引き継ぐ
		"doneJobs":   jobs,
		"fixDraft":   s.readFixDraft(),
		"doneOther":  doneOther,
		"src":        src,
		"stage":      listFiles(p.stage),
		"passed":     passed,
		"nPassed":    nPassed,
		"consult":    consult,
		"done":       listFiles(p.out),
		"doneInfo":   doneInfo(p.out),
		"users":      users,
		"ver":        panelVer,
		"stageUsers": stageUsers(p.stage),
		"stageFiles": stageFiles(p.stage),
		"months":     months,
		"last":       s.lastFresh(), // 登録済みの箱は、読み直しても「✓ 登録済み」（改善23）
		"family":     s.familyRows(),
	}, nil
}

// familyRows: 住所の要確認を「呼び名」に書き足す先（利用者・家族の行）
func (s *panelServer) familyRows() []nameRow {
	out := []nameRow{}
	rows, err := s.nameRowsWithCodes() // 記号も付ける（書類に出てくる方を、選ぶ欄の上に出すため。改善23）
	if err != nil {
		return out
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Kind == "利用者" || r.Kind == "家族" {
			k := r.Kind + "|" + normName(r.Name)
			if seen[k] {
				continue // 同じ区分・同じ氏名は1つにまとめる（書き足す先は上の行。改善5）
			}
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}

// ---- 1 資料を入れる ----

func (s *panelServer) upload(r *http.Request) (any, error) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		return nil, fmt.Errorf("ファイルを受け取れませんでした: %v", err)
	}
	saved, rejected, dup := []string{}, []string{}, []string{}
	for _, fh := range r.MultipartForm.File["files"] {
		name := filepath.Base(strings.ReplaceAll(fh.Filename, "\\", "/"))
		if !(isTargetExt(name) || strings.EqualFold(filepath.Ext(name), ".pdf")) || strings.HasPrefix(name, ".") {
			rejected = append(rejected, name)
			continue
		}
		f, err := fh.Open()
		if err != nil {
			rejected = append(rejected, name)
			continue
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			rejected = append(rejected, name)
			continue
		}
		if sameFileIn(s.p.src, data) {
			dup = append(dup, name)
			continue
		}
		dst := uniquePath(s.p.src, name)
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, err = out.Write(data)
			out.Close()
		}
		if err != nil {
			os.Remove(dst)
			rejected = append(rejected, name)
			continue
		}
		saved = append(saved, filepath.Base(dst))
	}
	// 前に置き換えて、まだ渡していないファイルが残っていたら知らせる（改善 No.41）
	stageLeft := []string{}
	if len(saved) > 0 {
		stageLeft = listFiles(s.p.stage)
	}
	return map[string]any{"saved": saved, "rejected": rejected, "dup": dup, "stageLeft": stageLeft}, nil
}

// ---- 2 置き換えて確認 ----

func (s *panelServer) mask(r *http.Request) (any, error) {
	runStart := time.Now()
	// 入れた記録（改善12 No.42）：今回はじめて置き換えるファイルか、前から残っているファイルか（No.44）
	ib := s.intakeLoad()
	s.intakeSync(ib)
	isNew := map[string]bool{}
	for n, rec := range ib.Src {
		if rec.Tries == 0 {
			isNew[n] = true
		}
	}
	prevStage := map[string]bool{}
	for _, f := range listFiles(s.p.stage) {
		prevStage[f] = true
	}
	// 片づけの紐付け用：置き換える前の元データのハッシュ（名前は控えに書かない）
	preHash := map[string]string{}
	docKind := map[string]string{}
	if names, _ := listTargets(s.p.src, true); len(names) > 0 {
		for _, n := range names {
			preHash[n] = s.hashFile(filepath.Join(s.p.src, n))
			docKind[n] = docKindOf(filepath.Join(s.p.src, n)) // エラー記録用（様式から分かるときだけ）
		}
	}
	// 渡す前のファイル（3_確認待ち）も、今の氏名一覧で置き換え直す（現場試験 S2・R8.10.1）：
	// 元データを 1_元データ＼処理済み から戻し、置き換え済みの写しは外してから、ほかのファイルと一緒に置き換える
	restaged := s.restage(ib)
	// 処理済みから戻した元データ（置き換え直すもの）のハッシュも控える。控えないと、元にした資料をたどれない（改善27 試験役 R1）
	for n := range restaged {
		if preHash[n] == "" {
			preHash[n] = s.hashFile(filepath.Join(s.p.src, n))
			docKind[n] = docKindOf(filepath.Join(s.p.src, n))
		}
	}
	sums, skipped, err := runMaskTo(s.p, s.p.stage, false)
	for _, x := range sums {
		if restaged[x.name] && x.outName != "" {
			prevStage[x.outName] = true // 前からあったファイル（前の方の残りを脇によける判断は今までどおり）
		}
	}
	var led [][]string
	for _, x := range sums {
		st, stageHash := "ok", ""
		switch {
		case x.err != "":
			st = "err"
		case x.held:
			st = "held"
		default:
			stageHash = s.hashFile(filepath.Join(s.p.stage, x.outName))
		}
		led = append(led, []string{"M", preHash[x.name], st, stageHash, x.report})
	}
	s.ledgerAppend(led)
	// 入れた記録を、置き換え済みへ引き継ぐ
	for _, x := range sums {
		rec := ib.Src[x.name]
		if rec == nil {
			rec = &intakeRec{At: runStart, How: howDirect}
		}
		rec.Tries++
		if x.err == "" && !x.held && x.outName != "" {
			ib.Stage[x.outName] = rec
			delete(ib.Src, x.name)
		} else {
			ib.Src[x.name] = rec
		}
	}
	if err != nil {
		s.intakeSave(ib)
		return nil, err
	}
	cb := s.codeBook()
	rep := &maskReport{At: time.Now().Format("2006/01/02 15:04"), Skipped: skipped, Ver: panelVer}
	// ファイルごとに出てくる利用者（置き換えた記号から。保留・処理できなかったファイルも）
	usersOf := map[string][]string{}
	stMap := stageUserMap(s.p.stage)
	var vm *masker // 保留・処理できなかったファイルの、見える所の利用者を調べるため（改善15）
	for _, x := range sums {
		seen := map[string]bool{}
		if x.outName != "" {
			// 渡せるファイル：置き換え済みの見える所の記号（改善15）
			for _, u := range stMap[x.outName] {
				seen[u] = true
			}
		} else if lines, ok := visibleText(filepath.Join(s.p.src, x.name)); ok {
			// 保留・処理できなかったファイル：元のファイルの見える所を、今の氏名一覧で調べる
			if vm == nil {
				if people, excl, err := readNameList(s.p.nameList); err == nil {
					vm = newMasker(people, excl, cb)
				}
			}
			if vm != nil {
				for _, l := range lines {
					for _, mt := range vm.findMatches(l, true) {
						for _, m := range reStageUser.FindAllStringSubmatch(mt.repl, -1) {
							seen[m[1]] = true
						}
					}
				}
			}
		} else {
			for k := range x.counts {
				f := strings.Split(k, "\t")
				for _, m := range reStageUser.FindAllStringSubmatch(f[len(f)-1], -1) {
					seen[m[1]] = true
				}
			}
		}
		us := []string{}
		for u := range seen {
			us = append(us, u)
		}
		sort.Strings(us)
		usersOf[x.name] = us
	}
	for _, x := range sums {
		fj := fileJSON{Name: x.name, OutName: x.outName, Err: x.err, PDF: x.pdf, Pages: x.pdfPages, EmptyPages: x.emptyPages, PoorPages: x.poorPages, Media: x.media, Suspects: []susJSON{}, Users: usersOf[x.name]}
		for _, c := range x.counts {
			fj.Count += c
		}
		switch {
		case x.err != "":
			fj.Status = "err"
			fj.Lefts = x.lefts
			fj.Guides = leftGuides(x.lefts, x.err)
		case x.held:
			fj.Status = "held"
		default:
			fj.Status = "ok"
		}
		var words []string
		kn := s.knownIndex()
		fam := s.familyRows()
		for _, su := range x.suspects {
			k := "other"
			if strings.HasPrefix(su.reason, "敬称") || strings.HasPrefix(su.reason, "氏名一覧の方の姓") || strings.HasPrefix(su.reason, "氏名欄") ||
				strings.HasPrefix(su.reason, "日付のすぐ後ろ") || strings.HasPrefix(su.reason, "名前の一部") ||
				strings.HasPrefix(su.reason, "氏名一覧の方と1字だけ違う") || strings.HasPrefix(su.reason, "敬称のない") { // 1字違いも名前の箱に（だれの名前かのボタンを出す。改善23）
				k = "name"
			} else if strings.HasPrefix(su.reason, "番地") {
				k = "addr"
			}
			sj := susJSON{Word: su.word, Name: reHonorTail.ReplaceAllString(su.word, ""), Context: su.context, Reason: su.reason, Kind: k, Garbled: strings.Contains(su.reason, "字が化けた")}
			if k == "addr" {
				sj.Reg = su.reg
				if sj.Reg == "" || !isAddrShape(sj.Reg) {
					sj.Reg = cleanAddr(su.word)
				}
				sj.Reg = stripAddrHead(sj.Reg) // 「住所秦野市…」の見出し「住所」を外す（改善23）
			}
			sj.Ex = suspectExWord(sj)
			sj.Common = k == "name" && isCommonWord(sj.Name)
			s.boxKey(&sj, kn)
			if m := rePlaceKana.FindStringSubmatch(su.reason); m != nil {
				sj.Place, sj.Staff, sj.Fam = m[1], false, false // 職員の箱に入れない（改善23 E1）
			}
			fuseInfo(&sj, su, cb, fam)
			if strings.HasPrefix(su.reason, "生年月日の欄の中身") {
				birthInfo(&sj, fam, usersOf[x.name])
			}
			if strings.HasPrefix(su.reason, "氏名欄（フリガナ）の中身") {
				readingInfo(&sj, fam, usersOf[x.name])
			}
			if sj.Known == "" {
				sj.Known = knownNow(sj, kn) // 呼び名・除外に登録済みも「✓ 登録済み」（改善23）
			}
			fj.Suspects = append(fj.Suspects, sj)
			if k != "other" {
				words = append(words, su.word)
			}
		}
		fj.Suspects = dedupeBoxes(fj.Suspects, cb) // 1か所に2つの箱を出さない（改善23）
		for _, line := range x.preview {
			fj.Preview = append(fj.Preview, segment(line, cb, words))
		}
		rep.Files = append(rep.Files, fj)
	}
	// 前回「渡せます」で、まだ 3_確認待ち に残っているファイルは表示を引き継ぐ
	if s.last != nil {
		inNew := map[string]bool{}
		for _, f := range rep.Files {
			inNew[f.Name] = true
		}
		staged := map[string]bool{}
		for _, f := range listFiles(s.p.stage) {
			staged[f] = true
		}
		var keep []fileJSON
		for _, f := range s.last.Files {
			if f.Status == "ok" && staged[f.OutName] && !inNew[f.Name] {
				keep = append(keep, f)
			}
		}
		rep.Files = append(keep, rep.Files...)
	}
	// 前の方の残りを脇によける（改善12 No.44）
	cur := map[string]bool{}
	nNew := 0
	for _, x := range sums {
		if isNew[x.name] {
			nNew++
			for _, u := range usersOf[x.name] {
				cur[u] = true
			}
		}
	}
	if nNew > 0 && len(cur) > 0 {
		var cands []asideCand
		for _, f := range listFiles(s.p.stage) {
			if !prevStage[f] {
				continue // 今回置き換えたファイルは動かさない
			}
			if us := stMap[f]; len(us) > 0 && disjoint(us, cur) {
				cands = append(cands, asideCand{"stage", f, us})
			}
		}
		inSrc := map[string]bool{}
		srcNow, _ := listTargets(s.p.src, true)
		for _, n := range srcNow {
			inSrc[n] = true
		}
		for _, x := range sums {
			rec := ib.Src[x.name]
			if isNew[x.name] || !inSrc[x.name] || rec == nil || !rec.At.Before(runStart) {
				continue
			}
			if us := usersOf[x.name]; len(us) > 0 && disjoint(us, cur) {
				cands = append(cands, asideCand{"src", x.name, us})
			}
		}
		rep.Aside = s.moveAside(cands, rep, ib)
	}
	s.intakeSave(ib)
	s.last = rep
	if b, err := json.Marshal(rep); err == nil {
		os.WriteFile(filepath.Join(s.p.report, "last.json"), b, 0600)
	}
	s.logMask(sums, usersOf, docKind)
	return rep, nil
}

// suspectExWord: 橙の確認を「除外」で消すときに氏名一覧に書く語（除外では消えないものは空）
func suspectExWord(sj susJSON) string {
	switch {
	case sj.Kind == "name":
		return strings.TrimSpace(sj.Name)
	case sj.Kind == "addr":
		return sj.Word
	case strings.HasPrefix(sj.Reason, "生年月日らしき日付"): // 改善13 No.46：「生年月日ではない（通す）」で除外に入れる語
		return sj.Word
	case strings.HasPrefix(sj.Reason, "8けた"):
		if i := strings.LastIndex(sj.Word, "："); i >= 0 {
			return sj.Word[i+len("："):]
		}
	default:
		if m := reNbChar.FindStringSubmatch(sj.Reason); m != nil {
			return m[1]
		}
	}
	return ""
}

var reNbChar = regexp.MustCompile(`すぐ[前後]に漢字「([^」]+)」`)

// logMask: 赤・橙をエラー記録に書く（改善12 No.43。ファイル名・語は書かない）
func (s *panelServer) logMask(sums []*fileSummary, usersOf map[string][]string, docKind map[string]string) {
	for i, x := range sums {
		if x.err == "" && !x.held {
			continue
		}
		fl := fileLabel(i+1, x.name, docKind[x.name])
		us := ""
		for _, u := range usersOf[x.name] {
			us += "【" + u + "】"
		}
		if us == "" {
			us = "利用者の記号なし"
		}
		if x.err != "" {
			if len(x.lefts) == 0 {
				k := lkOther
				if strings.Contains(x.err, "名前の定義") {
					k = lkDefName
				}
				s.errLog("②置き換え", "赤（処理できませんでした）："+k, fl, us, "決まり：置き換えの処理")
				continue
			}
			for _, h := range x.lefts {
				s.errLog("②置き換え", "赤（処理できませんでした）："+h.Kind, fl, h.LogLoc, h.Shape, us, "決まり：置き換えた後の残りチェック")
			}
			continue
		}
		cnt := map[string]int{}
		var order []string
		for _, su := range x.suspects {
			c := susCategory(su.reason)
			if cnt[c] == 0 {
				order = append(order, c)
			}
			cnt[c]++
		}
		var parts []string
		for _, c := range order {
			parts = append(parts, fmt.Sprintf("%s %d", c, cnt[c]))
		}
		s.errLog("②置き換え", fmt.Sprintf("橙（確かめてほしい所）%d件", len(x.suspects)), fl, us, "決まり："+strings.Join(parts, "・"))
	}
}

// segment: 置き換え後の1行を、記号（rep）・要確認（sus）・ふつうの文字に分ける
func segment(line string, cb *codeBook, sus []string) []seg {
	var out []seg
	plain := func(t string) {
		for t != "" {
			best, bw := -1, ""
			for _, w := range sus {
				if w == "" {
					continue
				}
				if i := strings.Index(t, w); i >= 0 && (best < 0 || i < best) {
					best, bw = i, w
				}
			}
			if best < 0 {
				out = append(out, seg{T: t})
				return
			}
			if best > 0 {
				out = append(out, seg{T: t[:best]})
			}
			out = append(out, seg{T: bw, Cls: "sus"})
			t = t[best+len(bw):]
		}
	}
	pos := 0
	for _, ix := range reTokenAny.FindAllStringIndex(line, -1) {
		tok := line[ix[0]:ix[1]]
		orig, ok := cb.codeToOrig[tok]
		if !ok {
			continue
		}
		plain(line[pos:ix[0]])
		out = append(out, seg{T: tok, Cls: "rep", Orig: orig})
		pos = ix[1]
	}
	plain(line[pos:])
	return out
}

// ---- 氏名一覧に追加 ----

func (s *panelServer) namelist(r *http.Request) (any, error) {
	var q struct{ Word, Kind string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	q.Word = strings.TrimSpace(q.Word)
	switch q.Kind {
	case "利用者", "家族", "その他", "除外":
	default:
		return nil, fmt.Errorf("区分が正しくありません")
	}
	if q.Word == "" || len([]rune(q.Word)) > 40 || strings.ContainsAny(q.Word, "\t\r\n【】") {
		return nil, fmt.Errorf("書き込む語が正しくありません")
	}
	// 1文字の除外もできる（R8.9.29 注文）。1文字の除外は「名前のすぐ前後に残った字」の確認にだけ効く
	// 同じ区分・同じ氏名の二重登録は止めて知らせる（改善5）
	if rows, err := readNameRows(s.p.nameList); err == nil {
		for _, r := range rows {
			if r.Kind == q.Kind && normName(r.Name) == normName(q.Word) {
				// 「氏名一覧のその行を開く」ボタン用に、行の番号も返す（改善12 No.37）
				return map[string]string{"ok": q.Kind + "「" + r.Name + "」は、すでに氏名一覧の " + fmt.Sprint(r.Row) + " 行目に登録されています（二重には登録しません）", "already": "1", "row": fmt.Sprint(r.Row)}, nil
			}
		}
	}
	if err := appendNameRow(s.p.nameList, q.Kind, q.Word); err != nil {
		return nil, err
	}
	return map[string]any{"ok": q.Kind + "「" + q.Word + "」を氏名一覧に追加しました", "pend": s.bumpPend(1)}, nil
}

// namesExclude: いくつかの語をまとめて「除外」に登録する（改善12 No.33・No.34）
// よく出る語をまとめて除外・このファイルの残りの橙を全部除外・赤の「これは個人情報ではない」から使う。
// 氏名一覧に名前・住所として登録してある語は、除外に入れない（置き換えを甘くしない）。
func (s *panelServer) namesExclude(r *http.Request) (any, error) {
	var q struct{ Words []string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	if len(q.Words) == 0 || len(q.Words) > 200 {
		return nil, fmt.Errorf("除外する語がありません")
	}
	rows, err := readNameRows(s.p.nameList)
	if err != nil {
		return nil, err
	}
	people, excl, err := readNameList(s.p.nameList)
	if err != nil {
		return nil, err
	}
	cb, err := loadCodeBook(s.p.codeBookF)
	if err != nil {
		return nil, err
	}
	m := newMasker(people, excl, cb)
	var added, already, refused []string
	seen := map[string]bool{}
	for _, w := range q.Words {
		w = strings.TrimSpace(w)
		if w == "" || seen[normName(w)] {
			continue
		}
		seen[normName(w)] = true
		if len([]rune(w)) > 40 || strings.ContainsAny(w, "\t\r\n【】") {
			refused = append(refused, w)
			continue
		}
		if m.isRegisteredWord(w) {
			refused = append(refused, w) // 氏名一覧の名前・住所そのもの
			continue
		}
		dup := false
		for _, x := range rows {
			if x.Kind == "除外" && normName(x.Name) == normName(w) {
				dup = true
				break
			}
		}
		if dup {
			already = append(already, w)
			continue
		}
		if err := appendNameRow(s.p.nameList, "除外", w); err != nil {
			return nil, err
		}
		rows = append(rows, nameRow{Kind: "除外", Name: w})
		added = append(added, w)
	}
	msg := fmt.Sprintf("%d 語を「除外」に登録しました", len(added))
	if len(added) > 0 {
		msg += "（" + strings.Join(added, "、") + "）"
	}
	if len(already) > 0 {
		msg += "。すでに登録済み：" + strings.Join(already, "、")
	}
	if len(refused) > 0 {
		msg += "。氏名一覧に名前・住所として登録してある語などは除外に入れませんでした：" + strings.Join(refused, "、")
	}
	return map[string]any{"ok": msg, "added": added, "already": already, "refused": refused, "pend": s.bumpPend(len(added))}, nil
}

// ---- 3 AIに渡す ----

var docFolder = map[string]string{"モニタリング表": "モニタリング", "支援経過記録": "支援経過", "ケアプラン": "ケアプラン", "アセスメントシート": "アセスメント", keikaDocLabel: keikaDocKey}
var reUserID = regexp.MustCompile(`^利用者\d{3,}$`)
var reMonth = regexp.MustCompile(`^R\d{1,2}\.\d{1,2}$`)

func (s *panelServer) send(r *http.Request) (any, error) {
	var q struct {
		User, Doc, Month, Note, Date string
		Made                         string `json:"made"`  // 作成日（ケアプラン・アセスメントシート。改善12 No.30）
		Start                        string `json:"start"` // 適用開始日（ケアプラン）
		Confirm                      bool
		MixOK                        bool `json:"mixOK"`
	}
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	if err := noteLengthErr(q.Note); err != nil { // 改善29 No.61：黙って切らずに、渡す前に止める
		return nil, err
	}
	// 支援経過（1日分）：対象の日付（画面の日付 2026-09-27）から月も決める
	var day time.Time
	if q.Doc == keikaDocLabel {
		d, ok := parseISODate(q.Date)
		if !ok {
			return nil, fmt.Errorf("支援経過（1日分）の日付を選んでください")
		}
		day = d
		q.Month = fmt.Sprintf("R%d.%d", d.Year()-2018, int(d.Month()))
	}
	// ケアプラン：作成日と適用開始日、アセスメントシート：作成日（実施日）（改善12 No.30・39）
	// 仕事のフォルダ名（利用者014_ケアプラン（R8.9実施））の月は、作成日の月から作る（形は変えない）
	var made, start time.Time
	if (q.Doc == "ケアプラン" || q.Doc == "アセスメントシート") && (q.Made != "" || q.Start != "" || q.Month == "") {
		lab := "作成日"
		if q.Doc == "アセスメントシート" {
			lab = "作成日（実施日）"
		}
		d, ok := parseISODate(q.Made)
		if !ok || d.Before(time.Now().AddDate(-2, 0, 0)) || d.After(time.Now().AddDate(2, 0, 0)) {
			return nil, fmt.Errorf("%sを選んでください", lab)
		}
		made = d
		{ // 改善28 No.56：適用開始日（予定）は、ケアプランだけでなくアセスメントシートでも必ず入れてもらう
			d2, ok := parseISODate(q.Start)
			if !ok || d2.Before(time.Now().AddDate(-2, 0, 0)) || d2.After(time.Now().AddDate(2, 0, 0)) {
				return nil, fmt.Errorf("適用開始日を選んでください")
			}
			start = d2
		}
		q.Month = fmt.Sprintf("R%d.%d", made.Year()-2018, int(made.Month()))
	}
	dk, ok := docFolder[q.Doc]
	if !ok || !reUserID.MatchString(q.User) || !reMonth.MatchString(q.Month) {
		return nil, fmt.Errorf("利用者・書類・月を選んでください")
	}
	su := stageUsers(s.p.stage)
	// ③の止めをエラー記録に書く（改善12 No.43。ファイル名・実名は書かず、決まりの名前と記号だけ）
	stop := func(rule string, err error) (any, error) {
		us := "【" + q.User + "】を選択"
		for _, u := range su {
			us += "【" + u + "】"
		}
		s.errLog("③AIに渡す", "止めました："+rule, "書類："+q.Doc, "渡す前のファイル "+fmt.Sprint(len(listFiles(s.p.stage)))+"件", us)
		return nil, err
	}
	if src, _ := listTargets(s.p.src, true); len(src) > 0 {
		return stop("置き換えていない・保留のファイルが残っている", fmt.Errorf("まだ置き換えていない、または保留のファイルが「1_元データ」に %d 件あります。先に片づけてください", len(src)))
	}
	files := listFiles(s.p.stage)
	// 最後の関所（現場試験 S2）：渡すファイル全部を今の氏名一覧で確かめ、登録済みの名前・住所が記号になっていない所が1つでもあれば渡さない
	if bad := s.finalGate(files); len(bad) > 0 {
		return stop("渡すファイルに登録済みの名前が残っている", fmt.Errorf("渡すファイルに、氏名一覧に登録済みの名前・住所が記号になっていない所があります。渡していません。「2 置き換えて確認する」で「もう一度置き換える」を押してから渡してください：%s", strings.Join(bad, "、")))
	}
	// 氏名一覧を変えたあと、まだ置き換え直していないときは渡さない（同じ方が古い記号のまま渡るのを防ぐ。改善23）
	if s.last != nil && s.last.Pend > 0 && len(files) > 0 {
		return stop("氏名一覧を変えたあと置き換え直していない", fmt.Errorf("氏名一覧を変えたあと（登録・除外・直す・まとめる %d 件）、まだ置き換え直していません。「2 置き換えて確認する」の「まとめて置き換え直す」を押してから渡してください", s.last.Pend))
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("渡すファイルがありません")
	}
	if len(su) == 0 && !q.Confirm {
		return stop("利用者の記号が見つからない（確認なし）", fmt.Errorf("書類の中に、登録済みの利用者の名前が1つも見つかりません。確認のチェックを入れてから押してください"))
	}
	if len(su) > 0 {
		found := false
		for _, u := range su {
			if u == q.User {
				found = true
			}
		}
		if !found {
			return stop("選んだ利用者と書類の利用者が違う", fmt.Errorf("書類に出てくる利用者は %s です。選んだ利用者（%s）と違います", strings.Join(su, "・"), q.User))
		}
	}
	// ファイルごとに判定する（改善11の2）
	//  (a) 選んだ利用者が出てこず、ほかの利用者が出てくる → 止める（確認のチェックでも通さない。前の方の書類の混ざり）
	//  (b) 選んだ利用者も、ほかの利用者も出てくる → mixOK（確認のチェック）があれば通す
	//  (c) 利用者が誰も出てこない → 上の confirm の扱い（今までどおり）
	var foreign, mixed []string
	mixLine := map[string]string{}
	for _, f := range stageFiles(s.p.stage) {
		if len(f.Users) == 0 {
			continue
		}
		hasU := false
		var others []string
		for _, u := range f.Users {
			if u == q.User {
				hasU = true
			} else {
				others = append(others, u)
			}
		}
		switch {
		case !hasU:
			foreign = append(foreign, f.Name+"（"+strings.Join(f.Users, "・")+"）")
		case len(others) > 0:
			mixed = append(mixed, f.Name+"（"+strings.Join(others, "・")+"）")
			mixLine[f.Name] = strings.Join(others, "・")
		}
	}
	if len(foreign) > 0 {
		return stop("別の方のファイルが混ざっている（2人以上の利用者）", fmt.Errorf("渡すファイルに2人以上の利用者（%s）が出てきます。選んだ利用者（%s）が出てこないファイルは、別の方のファイルです。「このファイルは渡さない」で外してから渡してください：%s", strings.Join(su, "・"), q.User, strings.Join(foreign, "、")))
	}
	if len(mixed) > 0 && !q.MixOK {
		return stop("別の利用者の記号も出てくる（確認なし）", fmt.Errorf("選んだ利用者（%s）のファイルに、別の利用者の記号も出てきます：%s。名前が少し出るだけなら、確認のチェックを入れてから押してください", q.User, strings.Join(mixed, "、")))
	}
	// ひとことも置き換える（名前が書かれていたら渡さない）
	people, excl, err := readNameList(s.p.nameList)
	if err != nil {
		return nil, err
	}
	cb, err := loadCodeBook(s.p.codeBookF)
	if err != nil {
		return nil, err
	}
	m := newMasker(people, excl, cb)
	note := strings.TrimSpace(strings.ReplaceAll(q.Note, "\r", ""))
	note = applyMatches(note, m.findMatches(note, true))
	if sus := m.findSuspects(note); len(sus) > 0 {
		return stop("AIへのひとことに名前らしき語", fmt.Errorf("「AIへのひとこと」に名前らしき語（%s）があります。書き方を変えるか、氏名一覧に登録してください", sus[0].word))
	}
	if cb.dirty {
		if err := saveCodeBook(s.p.codeBookF, cb); err != nil {
			return nil, fmt.Errorf("記号対応表を保存できませんでした: %v", err)
		}
	}
	job := q.User + "_" + dk
	keikaSeq := 1
	if q.Doc == keikaDocLabel {
		// 1日ごとに別の仕事（利用者001_支援経過1日分R8.9.27）。同じ日の2件目からは ②③…（改善5）
		job, keikaSeq, err = s.keikaJobName(q.User, day)
		if err != nil {
			return nil, err
		}
	}
	dst := filepath.Join(s.p.aiIn, job, "資料")
	if err := os.MkdirAll(dst, 0755); err != nil {
		return nil, err
	}
	var moved []string
	var led [][]string
	defer func() { s.ledgerAppend(led) }() // 片づけの紐付け用：渡したファイルのハッシュと仕事（名前は書かない）
	for _, f := range files {
		h := s.hashFile(filepath.Join(s.p.stage, f))
		to := uniquePath(dst, f)
		if err := copyFile(filepath.Join(s.p.stage, f), to); err != nil {
			return nil, fmt.Errorf("「%s」を渡せませんでした: %v", f, err)
		}
		os.Remove(filepath.Join(s.p.stage, f))
		moved = append(moved, filepath.Base(to))
		led = append(led, []string{"S", h, job + "|" + q.Month})
	}
	// 00_指示 に依頼の書き置き
	var b strings.Builder
	b.WriteString("# 依頼：" + q.User + " " + q.Doc + "（" + q.Month + "実施分）\n\n")
	b.WriteString("- 依頼日時：" + time.Now().Format("2006/01/02 15:04") + "（AI作業パネルから）\n")
	if !made.IsZero() { // 改善12 No.30・39
		if q.Doc == "ケアプラン" {
			b.WriteString("- 作成日：" + warekiDate(made) + "\n")
			b.WriteString("- 適用開始日：" + warekiDate(start) + "\n")
		} else {
			b.WriteString("- 作成日（実施日）：" + warekiDate(made) + "\n")
			b.WriteString("- 適用開始日：" + warekiDate(start) + "\n") // 改善28 No.56（ケアプランと同じ書き方）
		}
	}
	if q.Doc == keikaDocLabel {
		b.WriteString("- 書類：" + keikaDocLabel + "\n")
		b.WriteString("- 日付：" + warekiDateW(day) + "\n")
		if keikaSeq > 1 {
			b.WriteString(fmt.Sprintf("- この日の%d件目（同じ日の前の件とは別の依頼です。前の件の合格品とは別のフォルダに置いてください）\n", keikaSeq))
		}
		b.WriteString("- 合格品の置き場所：`40_合格・確認待ち\\" + job + "（" + warekiDate(day) + "）\\`（1日ごとに別のフォルダ）\n")
		b.WriteString("- 作るもの：この日の支援経過（1日分）の **Excel（.xlsx）1ファイル**（Wordではありません）。新しい原紙「支援経過記録_原紙（R8.10.3案）.xlsx」の形で、シートは1つだけ\n")
		b.WriteString("- 道具：`AI作業\\00_指示\\道具\\支援経過1日分\\`（make_day_v2.py と 原紙）。例：`python3 make_day_v2.py 原紙.xlsx " + warekiDate(day) + " 本文.txt 出力.xlsx --code 【" + q.User + "】 --fields 欄.json`（この日の2件目以降は `--seq 2` を付ける）\n")
		b.WriteString("- シート名：「月.日」（この日が2件目以降なら「月.日②」）。上の欄：区分（C4）・時間（M4）・場所（C5）・相手（M5）・目的（C6）、年（L2）・月（N2）・日（P2）。区分・場所・相手・目的は、プルダウンの言葉か手入力\n")
		b.WriteString("- 本文：A7 に入れる（37行まで・1行47字）。「・」の箇条書き、対応は「→」、小見出しは「■」\n")
		b.WriteString("- 新規面談の記録は、この依頼には含めません\n")
	}
	b.WriteString("- 資料の場所：`30_記入係作業中\\" + job + "\\資料\\`\n")
	b.WriteString("- 今回入れた資料：\n")
	for i, f := range moved {
		b.WriteString("  - " + f + "\n")
		if o := mixLine[files[i]]; o != "" {
			// (b) で通したときの控え（記号だけ。実名は書かない。改善11の2）
			b.WriteString("    - このファイルには別の利用者（" + o + "）の記号も出てきます。確認済み\n")
		}
	}
	if note != "" {
		b.WriteString("- AIへのひとこと：" + strings.ReplaceAll(note, "\n", " ") + "\n")
	}
	b.WriteString("- 状態：未着手\n")
	reqName := "依頼_" + job + "_" + q.Month + "_" + time.Now().Format("0102_1504") + ".md"
	reqPath := uniquePath(filepath.Join(s.p.ai, "00_指示"), reqName)
	if err := copyBytes(reqPath, []byte(b.String())); err != nil {
		return nil, fmt.Errorf("依頼の書き置きを作れませんでした: %v", err)
	}
	if s.last != nil {
		var keep []fileJSON
		for _, f := range s.last.Files {
			if f.Status != "ok" {
				keep = append(keep, f)
			}
		}
		s.last.Files = keep
		if b, err := json.Marshal(s.last); err == nil {
			os.WriteFile(filepath.Join(s.p.report, "last.json"), b, 0600)
		}
	}
	os.Remove(s.fixDraftPath()) // 渡したら直すお願いの下書きは消す
	return map[string]any{"job": job, "moved": moved, "request": filepath.Base(reqPath), "dest": "30_記入係作業中\\" + job + "\\資料"}, nil
}

func copyBytes(dst string, data []byte) error {
	part := dst + ".part"
	if err := os.WriteFile(part, data, 0644); err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, dst)
}

// ---- 4 届いた合格品を元に戻す ----

func (s *panelServer) restore(r *http.Request) (any, error) {
	var q struct{ Folder string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	if q.Folder == "" || q.Folder != filepath.Base(q.Folder) || strings.HasPrefix(q.Folder, "マスキングツール") || strings.HasPrefix(q.Folder, ".") {
		return nil, fmt.Errorf("フォルダが正しくありません")
	}
	dir := filepath.Join(s.p.aiOut, q.Folder)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("フォルダが見つかりません")
	}
	sums, err := runRestoreDir(s.p, dir, false)
	if err != nil {
		return nil, err
	}
	outs, errs, unknown := []string{}, []string{}, []string{}
	var led [][]string
	for _, x := range sums {
		if x.err != "" {
			errs = append(errs, x.name+"："+x.err)
			continue
		}
		// 片づけの紐付け用：2_完成 に置いたファイルのハッシュと 40 のフォルダ名（名前は書かない）
		led = append(led, []string{"R", s.hashFile(filepath.Join(s.p.out, x.outName)), q.Folder, x.report})
		outs = append(outs, x.outName)
		unknown = append(unknown, x.unknownTok...)
	}
	s.ledgerAppend(led)
	if len(outs) == 0 && len(errs) == 0 {
		return nil, fmt.Errorf("このフォルダに、元に戻す書類（記号の入ったExcel・Word）がありません")
	}
	if len(errs) == 0 {
		copyBytes(filepath.Join(dir, "元に戻し済み.txt"), []byte("元に戻した日時："+time.Now().Format("2006/01/02 15:04")+"\r\n"))
	}
	return map[string]any{"outs": outs, "errs": errs, "unknown": unknown}, nil
}

// ---- 開く ----

func (s *panelServer) open(r *http.Request) (any, error) {
	var q struct{ Where, Name string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	p := s.p
	fixed := map[string]struct {
		path   string
		folder bool
	}{
		"namelist": {p.nameList, false},
		"work":     {p.work, true},
		"src":      {p.src, true},
		"doneDir":  {p.out, true},
		"shiji":    {filepath.Join(p.ai, "00_指示"), true},
		"careplan": {filepath.Join(p.ai, "ケアプラン"), true},
		"assess":   {filepath.Join(p.ai, "アセスメント"), true},
		"memo":     {filepath.Join(p.ai, "引き継ぎメモ.md"), false},
		"howto":    {filepath.Join(s.extDir, "使い方.html"), false},
		"aiOut":    {p.aiOut, true},
		"consult":  {filepath.Join(p.ai, "50_要相談"), true},
	}
	if f, ok := fixed[q.Where]; ok {
		if _, err := os.Stat(f.path); err != nil {
			return nil, fmt.Errorf("見つかりません：%s", filepath.Base(f.path))
		}
		openPath(f.path, f.folder)
		return map[string]string{"ok": "開きました"}, nil
	}
	if q.Where == "downloadDir" { // ダウンロードフォルダを開く（改善28 No.55）
		dl := downloadsDir()
		if st, err := os.Stat(dl); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("ダウンロードフォルダが見つかりません")
		}
		openPath(dl, true)
		return map[string]string{"ok": "開きました"}, nil
	}
	if q.Where == "keikaDir" { // 支援経過＼（氏名）のフォルダ（q.Name は 利用者001）
		_, dir, err := s.keikaUser(q.Name)
		if err != nil {
			return nil, err
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("この方の支援経過のフォルダは、まだありません（書き足す・入れるときに作ります）")
		}
		openPath(dir, true)
		return map[string]string{"ok": "開きました"}, nil
	}
	name := q.Name
	if q.Where == "careplanFile" || q.Where == "assessFile" {
		name = filepath.Base(name) // 2段目のフォルダは safeRel で確かめる
	}
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return nil, fmt.Errorf("名前が正しくありません")
	}
	var target string
	folder := false
	switch q.Where {
	case "done":
		target = filepath.Join(p.out, name)
	case "passedDir":
		target, folder = filepath.Join(p.aiOut, name), true
	case "consultDir":
		target, folder = filepath.Join(p.ai, "50_要相談", name), true
	case "careplanFile", "assessFile": // 原本フォルダの様式をExcel・Wordで開く（ページ内の「様式そのものを直すときだけ」のボタンから）
		root := filepath.Join(p.ai, "ケアプラン")
		if q.Where == "assessFile" {
			root = filepath.Join(p.ai, "アセスメント")
		}
		t, ok := safeRel(root, q.Name)
		if !ok {
			return nil, fmt.Errorf("名前が正しくありません")
		}
		target = t
	default:
		return nil, fmt.Errorf("開けません")
	}
	if _, err := os.Stat(target); err != nil {
		return nil, fmt.Errorf("見つかりません：%s", name)
	}
	openPath(target, folder)
	return map[string]string{"ok": "開きました"}, nil
}

// userList: 氏名一覧の「利用者」全員（記号がまだなければここで決める）
func (s *panelServer) userList() ([]map[string]string, error) {
	people, excl, err := readNameList(s.p.nameList)
	if err != nil {
		return nil, err
	}
	cb, err := loadCodeBook(s.p.codeBookF)
	if err != nil {
		return nil, err
	}
	newMasker(people, excl, cb)
	if cb.dirty {
		if err := saveCodeBook(s.p.codeBookF, cb); err != nil {
			return nil, fmt.Errorf("記号対応表を保存できませんでした（開いていたら閉じてください）: %v", err)
		}
	}
	type u struct{ id, label string }
	var us []u
	seen := map[string]bool{}
	for _, p := range people {
		if sanitizePrefix(p.kind) != "利用者" {
			continue
		}
		code := cb.keyToCode["名|利用者|"+p.name]
		id := strings.Trim(code, "【】")
		if seen[id] {
			continue // 氏名一覧に同じ方が二重に登録されていても、選ぶ欄には1つだけ（改善5）
		}
		seen[id] = true
		us = append(us, u{id, normName(p.name) + "（" + id + "）"})
	}
	sort.Slice(us, func(a, b int) bool { return us[a].id < us[b].id })
	out := []map[string]string{}
	for _, x := range us {
		out = append(out, map[string]string{"id": x.id, "label": x.label})
	}
	return out, nil
}

var reStageUser = regexp.MustCompile(`【(利用者\d{3,})(?:の[^【】]*)?】`)

// stageUsers: 3_確認待ち の書類に出てくる利用者の記号
func stageUsers(dir string) []string {
	seen := map[string]bool{}
	for _, us := range stageUserMap(dir) {
		for _, u := range us {
			seen[u] = true
		}
	}
	out := []string{}
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// stageFileJSON: 渡す前のファイルと、その中に出てくる利用者（改善 No.41）
type stageFileJSON struct {
	Name  string   `json:"name"`
	Users []string `json:"users"`
}

func stageFiles(dir string) []stageFileJSON {
	m := stageUserMap(dir)
	out := []stageFileJSON{}
	for _, f := range listFiles(dir) {
		us := m[f]
		if us == nil {
			us = []string{}
		}
		out = append(out, stageFileJSON{Name: f, Users: us})
	}
	return out
}

// stageUserMap: ファイルごとに、名前と見える所（改善15）に出てくる利用者の記号
func stageUserMap(dir string) map[string][]string {
	res := map[string][]string{}
	for _, f := range listFiles(dir) {
		seen := map[string]bool{}
		for _, m := range reStageUser.FindAllStringSubmatch(f, -1) {
			seen[m[1]] = true
		}
		// 見える所だけ（改善15）。xlsx・docx 以外（pdf など）は今までどおり中の XML 全部
		if vu, ok := visibleUsers(filepath.Join(dir, f)); ok {
			for k := range vu {
				seen[k] = true
			}
		} else if zr, err := zip.OpenReader(filepath.Join(dir, f)); err == nil {
			for _, zf := range zr.File {
				if !strings.HasSuffix(zf.Name, ".xml") {
					continue
				}
				rc, err := zf.Open()
				if err != nil {
					continue
				}
				b, _ := io.ReadAll(io.LimitReader(rc, 32<<20))
				rc.Close()
				x := strings.NewReplacer("&#12304;", "【", "&#12305;", "】", "&#x3010;", "【", "&#x3011;", "】").Replace(string(b))
				for _, m := range reStageUser.FindAllStringSubmatch(x, -1) {
					seen[m[1]] = true
				}
			}
			zr.Close()
		}
		us := []string{}
		for k := range seen {
			us = append(us, k)
		}
		sort.Strings(us)
		res[f] = us
	}
	return res
}

// ---- 入れ間違いを取り消す ----

func (s *panelServer) remove(r *http.Request) (any, error) {
	var q struct{ Where, Name string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	if q.Name == "" || q.Name != filepath.Base(q.Name) || strings.HasPrefix(q.Name, ".") {
		return nil, fmt.Errorf("名前が正しくありません")
	}
	var dir string
	switch q.Where {
	case "src":
		dir = s.p.src // パネルで入れたコピー（元のファイルは入れる前の場所に残っている）
	case "stage":
		dir = s.p.stage // 置き換え済みのコピー（元データは 1_元データ\処理済み に残っている）
	default:
		return nil, fmt.Errorf("取り消せません")
	}
	target := filepath.Join(dir, q.Name)
	if st, err := os.Stat(target); err != nil || st.IsDir() {
		return nil, fmt.Errorf("見つかりません：%s", q.Name)
	}
	if err := os.Remove(target); err != nil {
		return nil, fmt.Errorf("取り消せませんでした。ファイルを開いていたら閉じてください（%v）", err)
	}
	if s.last != nil {
		var keep []fileJSON
		for _, f := range s.last.Files {
			if q.Where == "stage" && f.Status == "ok" && f.OutName == q.Name {
				continue
			}
			// ②の保留・処理できなかったファイルを取り消したら、②の一覧からも外す（No.35）
			if !(q.Where == "src" && f.Status != "ok" && f.Name == q.Name) {
				keep = append(keep, f)
			}
		}
		s.last.Files = keep
		if b, err := json.Marshal(s.last); err == nil {
			os.WriteFile(filepath.Join(s.p.report, "last.json"), b, 0600)
		}
	}
	return map[string]string{"ok": "「" + q.Name + "」を取り消しました"}, nil
}

// ---- 新しい利用者・家族を氏名一覧に登録 ----

func (s *panelServer) newuser(r *http.Request) (any, error) {
	var q struct{ Kind, Name, Alias string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	q.Name = strings.Join(strings.Fields(strings.ReplaceAll(q.Name, "　", " ")), " ")
	q.Alias = strings.TrimSpace(q.Alias)
	if q.Kind != "利用者" && q.Kind != "家族" {
		return nil, fmt.Errorf("区分が正しくありません")
	}
	if !strings.Contains(q.Name, " ") {
		return nil, fmt.Errorf("姓と名の間にスペースを入れてください（例：大和 一郎）")
	}
	if len([]rune(q.Name)) > 30 || len([]rune(q.Alias)) > 80 || strings.ContainsAny(q.Name+q.Alias, "\t\r\n【】") {
		return nil, fmt.Errorf("書き方が正しくありません")
	}
	people, _, err := readNameList(s.p.nameList)
	if err != nil {
		return nil, err
	}
	cb0, err := loadCodeBook(s.p.codeBookF)
	if err != nil {
		return nil, err
	}
	for _, p := range people {
		if normName(p.name) == normName(q.Name) {
			code := strings.Trim(cb0.keyToCode["名|"+rowPrefix(p.kind, p.name)+"|"+p.name], "【】")
			row := ""
			if rows, err := readNameRows(s.p.nameList); err == nil {
				for _, x := range rows {
					if x.Kind == p.kind && normName(x.Name) == normName(p.name) {
						row = fmt.Sprint(x.Row) // 「氏名一覧のその行を開く」（改善12 No.37）
						break
					}
				}
			}
			return map[string]string{"row": row, "ok": "「" + p.name + "」は、すでに氏名一覧に登録されています（区分：" + p.kind + "　記号：" + code + "）。同じ方なら、このまま「置き換える」に進んでください。同姓同名の別の方の場合は、登録せずにご相談ください", "id": code, "already": "1"}, nil
		}
	}
	// 氏名一覧から消したあとでも、記号対応表には残っている（同じ方なら同じ記号に戻る）
	past := ""
	for _, k := range cb0.order {
		f := strings.SplitN(k, "|", 3)
		if len(f) == 3 && f[0] == "名" && normName(f[2]) == normName(q.Name) {
			past = f[1] + "（記号：" + strings.Trim(cb0.keyToCode[k], "【】") + "）"
		}
	}
	if err := appendNameRow(s.p.nameList, q.Kind, q.Name, q.Alias); err != nil {
		return nil, err
	}
	users, err := s.userList()
	if err != nil {
		return nil, err
	}
	id := ""
	for _, u := range users {
		if strings.HasPrefix(u["label"], normName(q.Name)+"（") {
			id = u["id"]
		}
	}
	msg := q.Kind + "「" + q.Name + "」を氏名一覧に登録しました"
	if id != "" {
		msg += "（記号：" + id + "）"
	}
	if past != "" {
		msg += "。※この名前は以前にも " + past + " として登録されていました。同じ方なら、前と同じ記号で続けて使えます。同姓同名の別の方の場合は、ご相談ください"
	}
	return map[string]string{"ok": msg, "id": id}, nil
}

func sameFileIn(dir string, data []byte) bool {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err != nil || info.Size() != int64(len(data)) {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil && bytes.Equal(b, data) {
			return true
		}
	}
	return false
}

// doneInfo: 2_完成 のファイルと、元に戻した日時（ファイルの更新日時）。新しい順
func doneInfo(dir string) []doneJSON {
	out := []doneJSON{}
	for _, n := range listFiles(dir) {
		if st, err := os.Stat(filepath.Join(dir, n)); err == nil {
			out = append(out, doneJSON{Name: n, At: wareki(st.ModTime()), t: st.ModTime()})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].t.After(out[b].t) })
	return out
}
