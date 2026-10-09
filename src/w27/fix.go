package main

// 改善10（R8.9.29 注文）：⑥ 完成した書類から「AIに直してもらう」
// 2_完成 の書類を 1_元データ に写し、いつもの ②置き換え → ③AIに渡す の流れに乗せる。
// 2_完成 のファイルは動かさない・変えない（写すだけ）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var reFixFolder = regexp.MustCompile(`^(利用者\d+)_([^（]+)`)

func fixDocOf(kind string) string {
	switch {
	case strings.Contains(kind, "モニタリング"):
		return "モニタリング表"
	case strings.Contains(kind, "ケアプラン"):
		return "ケアプラン"
	case strings.Contains(kind, "アセスメント"):
		return "アセスメントシート"
	case strings.Contains(kind, "支援経過"):
		return "支援経過記録"
	}
	return ""
}

func (s *panelServer) fixPrepare(r *http.Request) (any, error) {
	var req struct {
		Folder string   `json:"folder"`
		Files  []string `json:"files"`
		Note   string   `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, fmt.Errorf("受け取れませんでした")
	}
	if !safeName(req.Folder) {
		return nil, fmt.Errorf("仕事の名前が正しくありません")
	}
	if len(req.Files) == 0 {
		return nil, fmt.Errorf("直してもらう書類を1つ以上選んでください")
	}
	// 改善29 No.61：③のひとことには「【修正のお願い】」が頭に付いて入るので、その分も含めて5000字まで（黙って切らない）
	if err := noteLengthErr("【修正のお願い】" + strings.Join(strings.Fields(req.Note), " ")); err != nil {
		return nil, err
	}
	jobs, _ := s.doneJobs(s.codeBook())
	var job *doneJob
	for i := range jobs {
		if jobs[i].Folder == req.Folder {
			job = &jobs[i]
		}
	}
	if job == nil {
		return nil, fmt.Errorf("この仕事は ⑥ 完成した書類 に見つかりません")
	}
	inJob := map[string]bool{}
	for _, f := range job.Files {
		inJob[f.Name] = true
	}
	copied, dup := []string{}, []string{}
	for _, name := range req.Files {
		if !inJob[name] || !safeName(name) {
			return nil, fmt.Errorf("「%s」はこの仕事の書類ではありません", name)
		}
		data, err := os.ReadFile(filepath.Join(s.p.out, name))
		if err != nil {
			return nil, fmt.Errorf("「%s」を読めません（Excel・Wordで開いていたら閉じてください）: %v", name, err)
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
			return nil, fmt.Errorf("「%s」を 1_元データ に写せませんでした: %v", name, err)
		}
		copied = append(copied, filepath.Base(dst))
	}
	user, doc := "", ""
	if m := reFixFolder.FindStringSubmatch(req.Folder); m != nil {
		user, doc = m[1], fixDocOf(m[2])
	}
	note := "【修正のお願い】" + strings.Join(strings.Fields(req.Note), " ")
	// パネルを閉じて開き直しても消えないように、下書きを残す（改善10の3）
	if b, err := json.Marshal(fixDraft{User: user, Doc: doc, Note: note, Folder: req.Folder}); err == nil {
		os.WriteFile(s.fixDraftPath(), b, 0600)
	}
	return map[string]any{"copied": copied, "dup": dup, "user": user, "doc": doc, "note": note}, nil
}

type fixDraft struct {
	User   string `json:"user"`
	Doc    string `json:"doc"`
	Note   string `json:"note"`
	Folder string `json:"folder"`
}

func (s *panelServer) fixDraftPath() string { return filepath.Join(s.p.report, "修正のお願い_下書き.json") }

// 下書き：1_元データか渡す前のファイルが残っている間だけ使う
func (s *panelServer) readFixDraft() *fixDraft {
	b, err := os.ReadFile(s.fixDraftPath())
	if err != nil {
		return nil
	}
	if len(listFiles(s.p.src)) == 0 && len(listFiles(s.p.stage)) == 0 {
		os.Remove(s.fixDraftPath())
		return nil
	}
	var d fixDraft
	if json.Unmarshal(b, &d) != nil {
		return nil
	}
	return &d
}
