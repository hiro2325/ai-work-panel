package main

// ④ 届いた合格品の一覧（40_合格・確認待ち）を作る部分。state（画面全体の状態）と、10秒ごとの確かめ（改善28 No.49）で同じものを使う。

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// passedList: 40の合格品の一覧（新しい順）と、人の出番がある件数（元に戻し済み・保管品は数えない）
func (s *panelServer) passedList(cb *codeBook) ([]itemJSON, int) {
	passed := []itemJSON{}
	nPassed := 0
	p := s.p
	for _, d := range listDirs(p.aiOut, "マスキングツール") {
		dir := filepath.Join(p.aiOut, d)
		it := itemJSON{Folder: d, Label: labelFor(d, cb), Files: []string{}, Info: []string{}}
		for _, f := range listFiles(dir) {
			if isTargetExt(f) {
				it.Files = append(it.Files, restoreText(f, cb))
			}
		}
		if b, err := os.ReadFile(filepath.Join(dir, "保管.txt")); err == nil {
			k := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(string(b), "\ufeff"), "\n", 2)[0])
			if k == "" {
				k = "AIが使う保管品"
			}
			it.Keep = k
		}
		if _, err := os.Stat(filepath.Join(dir, "元に戻し済み.txt")); err == nil {
			it.Restored = true
		} else if it.Keep == "" {
			nPassed++
		}
		if b, err := os.ReadFile(filepath.Join(dir, "判定記録.md")); err == nil {
			if m := reRound.FindStringSubmatch(string(b)); m != nil {
				it.Info = append(it.Info, "合格（"+m[1]+"回目）")
			} else {
				it.Info = append(it.Info, "合格")
			}
		}
		if b, err := os.ReadFile(filepath.Join(dir, "作業メモ.md")); err == nil {
			if m := reYokaku.FindStringSubmatch(string(b)); m != nil {
				it.Info = append(it.Info, "要確認 "+m[1]+"件")
			}
		}
		it.t = folderTime(dir, "判定記録.md")
		it.At = wareki(it.t)
		if st, err := os.Stat(filepath.Join(dir, "元に戻し済み.txt")); err == nil {
			it.RestAt = wareki(st.ModTime())
		}
		passed = append(passed, it)
	}
	sort.SliceStable(passed, func(a, b int) bool { return passed[a].t.After(passed[b].t) })
	return passed, nPassed
}

// /api/passed/poll：④の一覧だけを返す（画面を開いている間、10秒ごとに画面が確かめる。何も書き換えない）
func (s *panelServer) passedPoll(r *http.Request) (any, error) {
	passed, n := s.passedList(s.codeBook())
	return map[string]any{"passed": passed, "nPassed": n}, nil
}
