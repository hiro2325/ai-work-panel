package main

// 同じ資料でほかの書類も作る（改善27・R8.10.6 了承）
//   ⑥ 完成した書類の仕事から、その仕事でAIに渡した資料（記号に置き換え済みの写し。30_記入係作業中＼…＼資料）を、
//   別の書類の仕事の資料へ写し、00_指示 に依頼を書く。置き換えのやり直しはいらない（記号は前と同じ）。
//   - どの資料がその仕事のものかは、片づけ用の控え（渡したファイルのハッシュと仕事）で決める
//   - 写す先に同じ中身のファイルがあれば写さない（「すでにあり」）
//   - 何も消さない（元の仕事の資料はそのまま）

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// jobOfFolder: ⑥ の仕事のフォルダ名（「…（R8.10実施）」「…・修正1」）から仕事
func jobOfFolder(folder string) (jobID, bool) {
	return parseJobFolder(reFixSuffix.ReplaceAllString(folder, ""))
}

// docLabelOf: 仕事のフォルダの書類の名前（モニタリング）→ 画面の書類の名前（モニタリング表）
func docLabelOf(dk string) string {
	for lab, d := range docFolder {
		if d == dk && lab != keikaDocLabel {
			return lab
		}
	}
	return dk
}

type otherFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// jobMaterials: その仕事でAIに渡した資料（30_記入係作業中＼利用者NNN_書類＼資料 の中の、控えでこの仕事に紐づくもの）。
// 控えで1つも分からないとき（控えより前の仕事）は、資料フォルダの全部を返し guessed=true
func (s *panelServer) jobMaterials(j jobID) (dir string, files []otherFile, guessed bool) {
	dir = filepath.Join(s.p.aiIn, j.dir30(), "資料")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return dir, nil, false
	}
	ld := s.readLedger()
	var all []otherFile
	for _, e := range ents {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "~$") || strings.HasSuffix(e.Name(), ".part") {
			continue
		}
		st, err := e.Info()
		if err != nil {
			continue
		}
		f := otherFile{Name: e.Name(), Size: st.Size()}
		all = append(all, f)
		if ld.stageKey[s.hashFile(filepath.Join(dir, e.Name()))][j.key()] {
			files = append(files, f)
		}
	}
	if len(files) == 0 && len(all) > 0 {
		return dir, all, true
	}
	return dir, files, false
}

// /api/other/preview：写せる資料の一覧
func (s *panelServer) otherPreview(r *http.Request) (any, error) {
	var q struct{ Folder string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	j, ok := jobOfFolder(q.Folder)
	if !ok || !reUserID.MatchString(j.User) {
		return nil, fmt.Errorf("この仕事からは、ほかの書類を作れません（利用者の仕事ではありません）")
	}
	_, files, guessed := s.jobMaterials(j)
	if len(files) == 0 {
		return nil, fmt.Errorf("この仕事でAIに渡した資料が「30_記入係作業中＼%s＼資料」に見つかりません（片づけた・移した など）。①から資料を入れてください", j.dir30())
	}
	return map[string]any{"user": j.User, "doc": docLabelOf(j.Doc), "month": j.Month, "files": files, "guessed": guessed}, nil
}

// /api/other/send：資料を写して依頼を書く
func (s *panelServer) otherSend(r *http.Request) (any, error) {
	var q struct {
		Folder, Doc, Month, Note string
		Made                     string `json:"made"`
		Start                    string `json:"start"`
		Files                    []string
		Again                    bool
	}
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	if err := noteLengthErr(q.Note); err != nil { // 改善29 No.61
		return nil, err
	}
	j, ok := jobOfFolder(q.Folder)
	if !ok || !reUserID.MatchString(j.User) {
		return nil, fmt.Errorf("この仕事からは、ほかの書類を作れません")
	}
	dk, ok := docFolder[q.Doc]
	if !ok || q.Doc == keikaDocLabel {
		return nil, fmt.Errorf("作る書類を選んでください")
	}
	if len(q.Files) == 0 {
		return nil, fmt.Errorf("使う資料に✓を付けてください")
	}
	var made, start time.Time
	if q.Doc == "ケアプラン" || q.Doc == "アセスメントシート" {
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
	if !reMonth.MatchString(q.Month) {
		return nil, fmt.Errorf("何月実施分かを選んでください")
	}
	if q.Doc == docLabelOf(j.Doc) && q.Month == j.Month {
		return nil, fmt.Errorf("元の仕事と同じ書類・同じ月（%s の %s・%s実施分）です。同じ書類なら、別の月を選んでください", j.User, q.Doc, q.Month)
	}
	job := j.User + "_" + dk
	// 同じ方・同じ書類・同じ月の依頼が、まだ手つかずで残っていれば先に聞く（同じ書類を2回作らないように。改善27 試験役 P4）
	if !q.Again {
		if at := s.pendingRequest(job, q.Month); at != "" {
			return nil, fmt.Errorf("%s の %s（%s実施分）は、もう頼んであります（%s・まだ手つかず）。もう一度頼むときは、下の「それでも、もう一度頼む」にチェックを入れて押してください", j.User, q.Doc, q.Month, at)
		}
	}
	srcDir, files, _ := s.jobMaterials(j)
	okName := map[string]bool{}
	for _, f := range files {
		okName[f.Name] = true
	}
	for _, f := range q.Files {
		if !okName[f] || f != filepath.Base(f) {
			return nil, fmt.Errorf("資料の一覧が、画面を開いたあとで変わっています。画面を開き直してください（まだ何も写していません）")
		}
	}
	// ③と同じ最後の確かめ：前の仕事のあとで氏名一覧に足した名前・住所が、資料に記号にならずに残っていれば写さない（点検役 7）
	if bad := s.finalGateIn(srcDir, q.Files); len(bad) > 0 {
		return nil, fmt.Errorf("資料に、いまの氏名一覧で記号になる名前・住所が残っています（%s）。前の仕事のあとで氏名一覧に足した方などです。①から元の書類を入れ直して置き換えてください（まだ何も写していません）", strings.Join(bad, "、"))
	}
	// ひとことも置き換える（名前が書かれていたら依頼を書かない。③と同じ）
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
		return nil, fmt.Errorf("「AIへのひとこと」に名前らしき語（%s）があります。書き方を変えるか、氏名一覧に登録してください", sus[0].word)
	}
	if cb.dirty {
		if err := saveCodeBook(s.p.codeBookF, cb); err != nil {
			return nil, fmt.Errorf("記号対応表を保存できませんでした: %v", err)
		}
	}
	dst := filepath.Join(s.p.aiIn, job, "資料")
	if err := os.MkdirAll(dst, 0755); err != nil {
		return nil, err
	}
	// 写す先にすでにある中身（ハッシュ）
	have := map[string]string{}
	if ents, err := os.ReadDir(dst); err == nil {
		for _, e := range ents {
			if !e.IsDir() {
				have[s.hashFile(filepath.Join(dst, e.Name()))] = e.Name()
			}
		}
	}
	sort.Strings(q.Files)
	var moved, already []string
	var led [][]string
	defer func() { s.ledgerAppend(led) }()
	for _, f := range q.Files {
		h := s.hashFile(filepath.Join(srcDir, f))
		if n, ok := have[h]; ok && h != "" {
			already = append(already, n)
			led = append(led, []string{"S", h, job + "|" + q.Month})
			continue
		}
		to := uniquePath(dst, f)
		if err := copyFile(filepath.Join(srcDir, f), to); err != nil {
			return nil, fmt.Errorf("「%s」を写せませんでした: %v", f, err)
		}
		moved = append(moved, filepath.Base(to))
		led = append(led, []string{"S", h, job + "|" + q.Month})
	}
	var b strings.Builder
	b.WriteString("# 依頼：" + j.User + " " + q.Doc + "（" + q.Month + "実施分）\n\n")
	b.WriteString("- 依頼日時：" + time.Now().Format("2006/01/02 15:04") + "（AI作業パネルから・同じ資料でほかの書類も作る）\n")
	if !made.IsZero() {
		if q.Doc == "ケアプラン" {
			b.WriteString("- 作成日：" + warekiDate(made) + "\n")
			b.WriteString("- 適用開始日：" + warekiDate(start) + "\n")
		} else {
			b.WriteString("- 作成日（実施日）：" + warekiDate(made) + "\n")
			b.WriteString("- 適用開始日：" + warekiDate(start) + "\n") // 改善28 No.56（ケアプランと同じ書き方）
		}
	}
	b.WriteString("- 元の仕事：" + q.Folder + "（そのときAIに渡した資料をもう一度使う。置き換えは前と同じ）\n")
	b.WriteString("- 資料の場所：`30_記入係作業中\\" + job + "\\資料\\`\n")
	b.WriteString("- 今回入れた資料：\n")
	for _, f := range moved {
		b.WriteString("  - " + f + "\n")
	}
	for _, f := range already {
		b.WriteString("  - " + f + "（同じ中身がもう入っていたので、写していません）\n")
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
	msg := fmt.Sprintf("%s の %s（%s実施分）を、同じ資料 %d 件で頼みました。AIに「00_指示の依頼を処理して」と伝えてください", j.User, q.Doc, q.Month, len(moved)+len(already))
	if len(already) > 0 {
		msg += fmt.Sprintf("（%d 件は同じ中身がもう入っていたので、写していません）", len(already))
	}
	return map[string]any{"ok": msg, "job": job, "moved": moved, "already": already, "request": filepath.Base(reqPath)}, nil
}

// pendingRequest: 00_指示 に、同じ仕事・同じ月で「未着手」の依頼があれば、その依頼日時
func (s *panelServer) pendingRequest(job, month string) string {
	ms, _ := filepath.Glob(filepath.Join(s.p.ai, "00_指示", "依頼_"+job+"_"+month+"_*.md"))
	for _, f := range ms {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		t := string(b)
		if m := reReqState.FindStringSubmatch(t); m != nil && strings.HasPrefix(strings.TrimSpace(m[1]), "未着手") {
			if d := reReqDate.FindStringSubmatch(t); d != nil {
				return strings.TrimSpace(d[1])
			}
			return filepath.Base(f)
		}
	}
	return ""
}
