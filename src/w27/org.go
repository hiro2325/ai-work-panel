package main

// 組織図と進み具合（改善7・R8.9.28）
// 読むだけの画面。AI作業の中のファイルを読むだけで、何も書かない・動かさない・消さない。
// 利用者は記号（【利用者011】など）のまま出す（元の名前には戻さない）。
//
// 進み具合の出どころ
//   自動：00_指示\依頼_*.md の「状態」、40_合格・確認待ち・50_要相談 のフォルダ、パネル改善リスト.md の「状態」の列、
//         佐藤さんへ_確認のお願い.md の「いま、お願いしたいこと（N つ）」
//   手書き：00_指示\係の状況.md（統括リーダーが仕事の区切りごとに書き直す）

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 体制図（構想と直すことの一覧 5章）
type orgDef struct {
	Key   string
	Name  string
	Role  string
	Group string // 運営係チームなど
}

var orgMembers = []orgDef{
	{"youshiki", "様式係", "ケアプラン・アセスメント・頭紙・契約書類の様式を作る・直す", ""},
	{"kinyu", "記入係", "モニタリング表・ケアプラン・要点・アセスメントを書く", ""},
	{"masking", "マスキングツール作成係", "AI作業パネル（置き換え・元に戻す）を作る・直す", ""},
	{"skill", "スキル管理係", "スキルの重なりの整理・新しいスキルの提案", ""},
	{"shishin", "指針・計画係", "虐待防止・BCP・感染症などの指針や計画を作る", "運営係チーム"},
	{"gyouji", "年間行事係", "委員会・研修・訓練の年間計画を回す", "運営係チーム"},
	{"kasan", "加算係", "特定事業所加算（III）の要件と日程を管理する", "運営係チーム"},
	{"uriage", "売上管理係", "毎月の売上・担当人数を記録してグラフにする", ""},
	{"kankyou", "事務所環境係", "物品の買い足し・部屋のレイアウト・届出の平面図", ""},
}

type orgStatus struct {
	State   string `json:"state"`   // 作業中／確認待ち／待機中／準備中
	Now     string `json:"now"`     // いまの仕事
	Next    string `json:"next"`    // 次にやること
	Recent  string `json:"recent"`  // 最近済んだこと
	Updated string `json:"updated"` // 更新
}

type orgItem struct {
	Title string `json:"title"`
	Note  string `json:"note,omitempty"`
	Kind  string `json:"kind"` // wait（確認待ち）／done／consult／work
	At    string `json:"at,omitempty"`
	Go    string `json:"go,omitempty"` // 押したときの飛び先（パネルの画面）
	t     time.Time
}

type orgJob struct {
	User  string `json:"user"`
	Doc   string `json:"doc"`
	Month string `json:"month"`
	Stage int    `json:"stage"` // 0 受付待ち 1 記入・判定中 2 40で確認待ち 3 完成 -1 要相談
	State string `json:"state"`
	At    string `json:"at"`
	Go    string `json:"go,omitempty"`
	t     time.Time
}

// session log の1行（今あるデータから組み立てるだけ。どこにも書かない）
type orgLog struct {
	At  string `json:"at"`  // 10/01 14:32
	Who string `json:"who"` // 係の名前
	Msg string `json:"msg"`
	Res string `json:"res"` // ok／run／wait／consult／error
	Go  string `json:"go,omitempty"`
	t   time.Time
}

type orgMemberJSON struct {
	Key    string    `json:"key"`
	Name   string    `json:"name"`
	Role   string    `json:"role"`
	Group  string    `json:"group,omitempty"`
	Status orgStatus `json:"status"`
	Items  []orgItem `json:"items"`
	Jobs   []orgJob  `json:"jobs,omitempty"`
	Counts [][2]any  `json:"counts,omitempty"`
}

var (
	reOrgHead   = regexp.MustCompile(`(?m)^##\s+(.+?)\s*$`)
	reOrgKV     = regexp.MustCompile(`(?m)^[ \t]*[-*][ \t]*(状態|いまの仕事|次にやること|最近済んだこと|更新)[ \t]*[：:][ \t　]*(.*?)[ \t　]*$`)
	reOrgUser   = regexp.MustCompile(`^(利用者\d{3,})_(.+?)[（(]([^（）()]+)[）)]$`) // かっこの中は「R8.10実施」「R8.10.7～適用・R8.10.7作成」「R8.10.7作成」「R8.10.6」（改善29 No.62）
	reOrgKaizen = regexp.MustCompile(`^AI作業パネル_改善(\d+)`)
	reOrgCur    = regexp.MustCompile(`中身は\s*改善(\d+)`)
	reOrgAsk    = regexp.MustCompile(`いま、?お願いしたいこと[（(]\s*(\d+)\s*つ\s*[）)]`)
	reOrgAskNo  = regexp.MustCompile(`いま、?お願いしたいこと[（(]\s*(なし|0)`)
	reOrgReqDt  = regexp.MustCompile(`(\d{4})/(\d{1,2})/(\d{1,2})\s+(\d{1,2}):(\d{2})`)
)

// 00_指示\係の状況.md を読む（見出し＝係の名前）
func readOrgStatus(path string) map[string]orgStatus {
	m, _ := readOrgStatusOrdered(path)
	return m
}

// 見出しの並び順も返す（係の状況.md にだけある係＝現場試験係・点検役などを組織図に出すため）
func readOrgStatusOrdered(path string) (map[string]orgStatus, []string) {
	out := map[string]orgStatus{}
	order := []string{}
	t, err := readSmallText(path)
	if err != nil {
		return out, order
	}
	t = strings.ReplaceAll(t, "\r\n", "\n")
	idx := reOrgHead.FindAllStringSubmatchIndex(t, -1)
	for i, m := range idx {
		name := strings.TrimSpace(t[m[2]:m[3]])
		end := len(t)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		var st orgStatus
		for _, kv := range reOrgKV.FindAllStringSubmatch(t[m[1]:end], -1) {
			v := strings.Trim(kv[2], "* ")
			switch kv[1] {
			case "状態":
				st.State = v
			case "いまの仕事":
				st.Now = v
			case "次にやること":
				st.Next = v
			case "最近済んだこと":
				st.Recent = v
			case "更新":
				st.Updated = v
			}
		}
		out[name] = st
		order = append(order, name)
	}
	return out, order
}

func reqTime(it reqItem) time.Time {
	if m := reOrgReqDt.FindStringSubmatch(it.Date); m != nil {
		n := func(s string) int { v, _ := strconv.Atoi(s); return v }
		return time.Date(n(m[1]), time.Month(n(m[2])), n(m[3]), n(m[4]), n(m[5]), 0, 0, time.Local)
	}
	if t, err := time.ParseInLocation("2006/01/02 15:04", it.MTime, time.Local); err == nil {
		return t
	}
	return time.Time{}
}

// 依頼の状態 → 段階
func reqStage(state string) int {
	switch {
	case strings.Contains(state, "未着手"):
		return 0
	case strings.Contains(state, "完了") && strings.Contains(state, "50"):
		return -1
	case strings.Contains(state, "完了") && strings.Contains(state, "40"):
		return 2
	case strings.Contains(state, "完了") || strings.Contains(state, "受領"):
		return 3
	}
	return 1 // 着手・差し戻しなど
}

func folderKey(user, doc, month string) string {
	return strings.Trim(user, "【】") + "_" + strings.TrimSuffix(doc, "表") + "（" + month + "実施）"
}

// orgUserParse: 40・50のフォルダ名が利用者の仕事なら、（利用者・書類・かっこの中）。かっこの中の日付が読めない名前は仕事とみなさない
func orgUserParse(d string) (user, doc, paren string, ok bool) {
	m := reOrgUser.FindStringSubmatch(d)
	if m == nil {
		return "", "", "", false
	}
	if _, ok := jobMonthOfParen(m[3]); !ok {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// orgKey: フォルダ名から、仕事の目印（依頼の folderKey と同じ形）。前の形の名前も新しい形の名前も同じ仕事なら同じ目印になる
func orgKey(d string) string {
	if u, doc, paren, ok := orgUserParse(d); ok {
		mo, _ := jobMonthOfParen(paren)
		return folderKey(u, doc, mo)
	}
	return d
}

func (s *panelServer) orgView() (any, error) {
	p := s.p
	empty := newCodeBook() // 記号のまま出す
	manual, manualOrder := readOrgStatusOrdered(filepath.Join(p.ai, "00_指示", "係の状況.md"))

	// --- 依頼（記入係） ---
	dir := filepath.Join(p.ai, "00_指示")
	reqs := []reqItem{}
	for _, e := range entriesIn(dir) {
		if strings.HasPrefix(e.Name, "依頼_") && strings.EqualFold(filepath.Ext(e.Name), ".md") {
			t, _ := readSmallText(filepath.Join(dir, e.Name))
			it := parseRequest(e.Name, t, empty)
			it.MTime = e.MTime
			reqs = append(reqs, it)
		}
	}
	passedKeys := map[string]bool{}
	for _, r := range reqs {
		if reqStage(r.State) == 2 {
			passedKeys[folderKey(r.User, r.Doc, r.Month)] = true
		}
	}

	// --- 40 ---
	cur := 0
	if t, err := readSmallText(filepath.Join(dir, "パネル改善リスト.md")); err == nil {
		if m := reOrgCur.FindStringSubmatch(t); m != nil {
			cur, _ = strconv.Atoi(m[1])
		}
	}
	items := map[string][]orgItem{}
	restored := map[string]bool{}
	byKey40, byKey50 := map[string]string{}, map[string]string{} // 仕事の目印 → 実際のフォルダ名（前の形も新しい形も）
	for _, d := range listDirs(p.aiOut, "_") {
		if strings.HasPrefix(d, "マスキングツール") {
			continue // 最初の道具（画面の④と同じく出さない）
		}
		full := filepath.Join(p.aiOut, d)
		ft := folderTime(full, "判定記録.md")
		keepNote := ""
		if b, err := os.ReadFile(filepath.Join(full, "保管.txt")); err == nil {
			keepNote = strings.TrimSpace(strings.SplitN(strings.TrimPrefix(string(b), "\ufeff"), "\n", 2)[0])
		}
		uOK := false
		var uCode, uDoc, uParen string
		if u, doc, paren, ok := orgUserParse(d); ok {
			uOK, uCode, uDoc, uParen = true, u, doc, paren
			if prev, dup := byKey40[orgKey(d)]; !dup || d > prev {
				byKey40[orgKey(d)] = d
			}
		}
		if uOK && keepNote != "" {
			u, doc, paren := uCode, uDoc, uParen
			items["kinyu"] = append(items["kinyu"], orgItem{Title: "【" + u + "】" + doc + "（" + paren + "）", Note: "保管中：" + keepNote, Kind: "keep", At: wareki(ft), Go: "p4", t: ft})
			continue
		}
		if m := reOrgKaizen.FindStringSubmatch(d); m != nil && keepNote != "" {
			items["masking"] = append(items["masking"], orgItem{Title: d, Note: "保管中：" + keepNote, Kind: "keep", At: wareki(ft), Go: "p4", t: ft})
			continue
		}
		if uOK {
			m := []string{"", uCode, uDoc, uParen}
			if _, err := os.Stat(filepath.Join(full, "元に戻し済み.txt")); err == nil {
				restored[orgKey(d)] = true
				items["kinyu"] = append(items["kinyu"], orgItem{Title: "【" + m[1] + "】" + m[2] + "（" + m[3] + "）", Note: "完成（元に戻し済み）", Kind: "done", At: wareki(ft), Go: "p6/" + d, t: ft})
			} else {
				items["kinyu"] = append(items["kinyu"], orgItem{Title: "【" + m[1] + "】" + m[2] + "（" + m[3] + "）", Note: "合格。佐藤さんの確認待ち（④で元に戻す）", Kind: "wait", At: wareki(ft), Go: "p4/" + d, t: ft})
			}
			continue
		}
		if m := reOrgKaizen.FindStringSubmatch(d); m != nil {
			n, _ := strconv.Atoi(m[1])
			if cur > 0 && n <= cur {
				items["masking"] = append(items["masking"], orgItem{Title: d, Note: "採用済み", Kind: "done", At: wareki(ft), Go: "ai40/" + d, t: ft})
			} else {
				items["masking"] = append(items["masking"], orgItem{Title: d, Note: "40に提案中。佐藤さんの確認待ち", Kind: "wait", At: wareki(ft), Go: "ai40/" + d, t: ft})
			}
			continue
		}
		key := "youshiki"
		if strings.Contains(d, "マスキング") {
			key = "masking"
		}
		if keepNote != "" {
			items[key] = append(items[key], orgItem{Title: d, Note: "保管中：" + keepNote, Kind: "keep", At: wareki(ft), Go: "p4", t: ft})
			continue
		}
		if _, err := os.Stat(filepath.Join(full, "採用済み.txt")); err == nil {
			items[key] = append(items[key], orgItem{Title: d, Note: "採用済み", Kind: "done", At: wareki(ft), Go: "ai40/" + d, t: ft})
		} else {
			items[key] = append(items[key], orgItem{Title: d, Note: "40に提案中。佐藤さんの確認待ち", Kind: "wait", At: wareki(ft), Go: "ai40/" + d, t: ft})
		}
	}

	// --- 50 ---
	consultDir := filepath.Join(p.ai, "50_要相談")
	openConsult := 0
	for _, d := range listDirs(consultDir, "_") {
		full := filepath.Join(consultDir, d)
		ft := folderTime(full, "相談メモ.md")
		key := "kinyu"
		title := d
		if u, doc, paren, ok := orgUserParse(d); ok {
			title = "【" + u + "】" + doc + "（" + paren + "）"
			if prev, dup := byKey50[orgKey(d)]; !dup || d > prev {
				byKey50[orgKey(d)] = d
			}
		} else if strings.Contains(d, "パネル") || strings.Contains(d, "マスキング") {
			key = "masking"
		} else {
			key = "youshiki"
		}
		if passedKeys[orgKey(d)] {
			continue // あとの依頼で合格している（解決済み）
		} else {
			openConsult++
			items[key] = append(items[key], orgItem{Title: title, Note: "要相談で止まっています（⑤要相談で理由を読めます）", Kind: "consult", At: wareki(ft), Go: "p5/" + d, t: ft})
		}
	}

	// --- 記入係の仕事の段階 ---
	jobs := []orgJob{}
	for _, r := range reqs {
		if r.User == "" {
			continue
		}
		st := reqStage(r.State)
		if st == 2 && restored[folderKey(r.User, r.Doc, r.Month)] {
			st = 3
		}
		if st == -1 && (passedKeys[folderKey(r.User, r.Doc, r.Month)] || strings.Contains(r.State, "解決済み")) {
			continue // 同じ仕事があとで合格している
		}
		t := reqTime(r)
		state := r.State
		if len([]rune(state)) > 60 {
			state = string([]rune(state)[:60]) + "…"
		}
		fk := folderKey(r.User, r.Doc, r.Month)
		goTo := "shijidoc/" + r.File
		isDir := func(d string) bool { st, err := os.Stat(d); return err == nil && st.IsDir() }
		d40, d50 := byKey40[fk], byKey50[fk] // 前の形の名前も新しい形の名前も、同じ仕事なら見つける（改善29 No.62）
		switch {
		case st == 2 && d40 != "" && isDir(filepath.Join(p.aiOut, d40)):
			goTo = "p4/" + d40
		case st == 3 && d40 != "" && isDir(filepath.Join(p.aiOut, d40)):
			goTo = "p6/" + d40
		case st == -1 && d50 != "" && isDir(filepath.Join(consultDir, d50)):
			goTo = "p5/" + d50
		}
		jobs = append(jobs, orgJob{User: "【" + strings.Trim(r.User, "【】") + "】", Doc: r.Doc, Month: r.Month, Stage: st, State: state, At: wareki(t), Go: goTo, t: t})
	}
	sort.SliceStable(jobs, func(a, b int) bool { return jobs[a].t.After(jobs[b].t) })
	if len(jobs) > 8 {
		jobs = jobs[:8]
	}
	active := 0
	for _, j := range jobs {
		if j.Stage == 0 || j.Stage == 1 {
			active++
		}
	}

	// --- パネル改善リスト（マスキングツール作成係） ---
	var maskCounts [][2]any
	if t, err := readSmallText(filepath.Join(dir, "パネル改善リスト.md")); err == nil {
		c := map[string]int{}
		for _, line := range strings.Split(strings.ReplaceAll(t, "\r\n", "\n"), "\n") {
			cols := strings.Split(line, "|")
			if len(cols) < 8 {
				continue
			}
			if _, err := strconv.Atoi(strings.TrimSpace(cols[1])); err != nil {
				continue
			}
			st := strings.TrimSpace(cols[6])
			for _, k := range []string{"作業中", "提案中", "承認済み", "採用", "見送り", "候補"} {
				if strings.HasPrefix(st, k) {
					c[k]++
					break
				}
			}
		}
		for _, k := range []string{"作業中", "提案中", "候補", "採用"} {
			maskCounts = append(maskCounts, [2]any{"改善リスト " + k, c[k]})
		}
	}

	// --- 組み立て ---
	members := []orgMemberJSON{}
	for _, d := range orgMembers {
		m := orgMemberJSON{Key: d.Key, Name: d.Name, Role: d.Role, Group: d.Group, Items: items[d.Key]}
		if m.Items == nil {
			m.Items = []orgItem{}
		}
		sort.SliceStable(m.Items, func(a, b int) bool {
			ra, rb := kindRank(m.Items[a].Kind), kindRank(m.Items[b].Kind)
			if ra != rb {
				return ra < rb
			}
			return m.Items[a].t.After(m.Items[b].t)
		})
		// 済んだものは新しい3件まで
		kept, done := []orgItem{}, 0
		for _, it := range m.Items {
			if it.Kind == "done" {
				done++
				if done > 3 {
					continue
				}
			}
			kept = append(kept, it)
		}
		m.Items = kept
		m.Status = manual[d.Name]
		if d.Key == "kinyu" {
			m.Jobs = jobs
			if active > 0 {
				m.Status.State = "作業中"
				for _, j := range jobs {
					if j.Stage == 0 || j.Stage == 1 {
						m.Status.Now = j.User + " " + j.Doc + "（" + j.Month + "実施）" + map[int]string{0: "…受付待ち", 1: "…記入・判定中"}[j.Stage]
						break
					}
				}
			}
		}
		if d.Key == "masking" {
			m.Counts = maskCounts
		}
		if m.Status.State == "" || (m.Status.State == "待機中" && hasKind(m.Items, "wait")) {
			switch {
			case hasKind(m.Items, "wait"):
				m.Status.State = "確認待ち"
			case m.Status.Now != "":
				m.Status.State = "作業中"
			default:
				m.Status.State = "待機中"
			}
		}
		members = append(members, m)
	}

	// 係の状況.md にだけある係（現場試験係・点検役など）も出す
	known := map[string]bool{"統括リーダー": true}
	for _, d := range orgMembers {
		known[d.Name] = true
	}
	for i, nm := range manualOrder {
		if known[nm] || strings.TrimSpace(nm) == "" {
			continue
		}
		known[nm] = true
		st := manual[nm]
		if st.State == "" {
			st.State = "待機中"
			if st.Now != "" {
				st.State = "作業中"
			}
		}
		members = append(members, orgMemberJSON{Key: "x" + strconv.Itoa(i), Name: nm, Role: "係の状況.md より", Status: st, Items: []orgItem{}})
	}

	// --- session log（新しい順に10行） ---
	logs := []orgLog{}
	addLog := func(t time.Time, who, msg, res, goTo string) {
		if t.IsZero() {
			return
		}
		logs = append(logs, orgLog{At: t.Format("01/02 15:04"), Who: who, Msg: msg, Res: res, Go: goTo, t: t})
	}
	for _, j := range jobs {
		lbl := j.User + " " + j.Doc
		switch j.Stage {
		case 0:
			addLog(j.t, "記入係", lbl+" 依頼を受付", "wait", j.Go)
		case 1:
			addLog(j.t, "記入係", lbl+" 作成中", "run", j.Go)
		case 2:
			addLog(j.t, "記入係", lbl+" 完了（40へ）", "ok", j.Go)
		case 3:
			addLog(j.t, "記入係", lbl+" 完成", "ok", j.Go)
		case -1:
			addLog(j.t, "記入係", lbl+" 要相談（50へ）", "consult", j.Go)
		}
	}
	for _, m := range members {
		for _, it := range m.Items {
			if it.t.IsZero() || it.Kind == "keep" {
				continue
			}
			switch it.Kind {
			case "wait":
				addLog(it.t, m.Name, "40へ納品 "+it.Title, "ok", it.Go)
			case "consult":
				addLog(it.t, m.Name, "50で相談 "+it.Title, "consult", it.Go)
			}
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "パネル改善リスト.md")); err == nil {
		if t, err := readSmallText(filepath.Join(dir, "パネル改善リスト.md")); err == nil {
			for _, line := range strings.Split(strings.ReplaceAll(t, "\r\n", "\n"), "\n") {
				cols := strings.Split(line, "|")
				if len(cols) < 8 {
					continue
				}
				n, err := strconv.Atoi(strings.TrimSpace(cols[1]))
				if err != nil {
					continue
				}
				sv := strings.TrimSpace(cols[6])
				switch {
				case strings.HasPrefix(sv, "作業中"):
					addLog(st.ModTime(), "マスキングツール作成係", "改善"+strconv.Itoa(n)+" 作業中", "run", "")
				case strings.HasPrefix(sv, "提案中"):
					addLog(st.ModTime(), "マスキングツール作成係", "改善"+strconv.Itoa(n)+" 提案中", "ok", "")
				}
			}
		}
	}
	// エラー記録（きょう・きのう）
	for back := 0; back < 2; back++ {
		day := time.Now().AddDate(0, 0, -back)
		if t, err := readSmallText(s.errLogPath(day)); err == nil {
			for _, line := range strings.Split(strings.ReplaceAll(t, "\r\n", "\n"), "\n") {
				if len(line) < 5 || line[2] != ':' {
					continue
				}
				hh, e1 := strconv.Atoi(line[:2])
				mm, e2 := strconv.Atoi(line[3:5])
				if e1 != nil || e2 != nil {
					continue
				}
				body := strings.TrimSpace(strings.ReplaceAll(line[5:], "　", " "))
				if r := []rune(body); len(r) > 40 {
					body = string(r[:40]) + "…"
				}
				addLog(time.Date(day.Year(), day.Month(), day.Day(), hh, mm, 0, 0, time.Local), "パネル", body, "error", "")
			}
		}
	}
	sort.SliceStable(logs, func(a, b int) bool { return logs[a].t.After(logs[b].t) })
	if len(logs) > 10 {
		logs = logs[:10]
	}

	leader := manual["統括リーダー"]
	if leader.State == "" {
		leader.State = "待機中"
		if active > 0 {
			leader.State = "作業中"
		}
	}
	ask := -1
	if t, err := readSmallText(filepath.Join(p.ai, "佐藤さんへ_確認のお願い.md")); err == nil {
		if m := reOrgAsk.FindStringSubmatch(t); m != nil {
			ask, _ = strconv.Atoi(m[1])
		} else if reOrgAskNo.MatchString(t) {
			ask = 0
		}
	}
	waits := 0
	for _, m := range members {
		for _, it := range m.Items {
			if it.Kind == "wait" {
				waits++
			}
		}
	}
	return map[string]any{
		"leader":      leader,
		"members":     members,
		"ask":         ask,
		"waits":       waits,
		"openConsult": openConsult,
		"hasManual":   len(manual) > 0,
		"log":         logs,
		"nreq":        len(reqs),
		"active":      active,
		"at":          time.Now().Format("15:04:05"),
	}, nil
}

func kindRank(k string) int {
	switch k {
	case "consult":
		return 0
	case "wait":
		return 1
	case "work":
		return 2
	case "keep":
		return 3
	}
	return 4
}

func hasKind(items []orgItem, k string) bool {
	for _, it := range items {
		if it.Kind == k {
			return true
		}
	}
	return false
}
