package main

// 入れた日時と入れ方（改善12 No.42）
// マスキング作業＼確認リスト＼入れた記録.json に、ファイルごとの「入れた日時」と「入れ方」を残す。
//   入れ方：ドラッグ／パソコンから選ぶ／貼り付け／支援経過（1日分）／支援経過一覧の切り出し／⑥直してもらう／フォルダに直接
// パネルの外から 1_元データ に直接入ったファイルは、パネルが最初に気づいた日時を「入れた日時」にする（フォルダに直接）。
// 置き換え済み（3_確認待ち）へ移っても、同じ記録を引き継ぐ。
// 記録にはファイル名が入るので、AI作業には置かない（マスキング作業の中だけ）。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const intakeFileName = "入れた記録.json"

const howDirect = "フォルダに直接"

type intakeRec struct {
	At    time.Time `json:"at"`
	How   string    `json:"how"`
	Tries int       `json:"tries,omitempty"` // 置き換え（②）を通った回数（No.44：前からの残りか、今回入れたものか）
}

type intakeBook struct {
	Src   map[string]*intakeRec `json:"src"`
	Stage map[string]*intakeRec `json:"stage"`
}

func (s *panelServer) intakePath() string { return filepath.Join(s.p.report, intakeFileName) }

func (s *panelServer) intakeLoad() *intakeBook {
	b := &intakeBook{Src: map[string]*intakeRec{}, Stage: map[string]*intakeRec{}}
	if data, err := os.ReadFile(s.intakePath()); err == nil {
		json.Unmarshal(data, b)
	}
	if b.Src == nil {
		b.Src = map[string]*intakeRec{}
	}
	if b.Stage == nil {
		b.Stage = map[string]*intakeRec{}
	}
	return b
}

func (s *panelServer) intakeSave(b *intakeBook) {
	if data, err := json.MarshalIndent(b, "", " "); err == nil {
		copyBytes(s.intakePath(), data)
	}
}

// intakeSync: 記録のないファイル（フォルダに直接入ったもの）を足し、なくなったファイルの記録を外す
func (s *panelServer) intakeSync(b *intakeBook) bool {
	changed := false
	now := time.Now()
	sync := func(m map[string]*intakeRec, files []string) {
		have := map[string]bool{}
		for _, f := range files {
			have[f] = true
			if m[f] == nil {
				m[f] = &intakeRec{At: now, How: howDirect}
				changed = true
			}
		}
		for k := range m {
			if !have[k] {
				delete(m, k)
				changed = true
			}
		}
	}
	src, _ := listTargets(s.p.src, true)
	sync(b.Src, src)
	sync(b.Stage, listFiles(s.p.stage))
	return changed
}

// intakeAdd: パネルから入れたファイルを記録する
func (s *panelServer) intakeAdd(names []string, how string) {
	if len(names) == 0 {
		return
	}
	b := s.intakeLoad()
	now := time.Now()
	for _, n := range names {
		b.Src[n] = &intakeRec{At: now, How: how}
	}
	s.intakeSave(b)
}

// intakeWrap: 1_元データ にファイルを入れる API の前後で、増えたファイルに入れ方を記録する
func (s *panelServer) intakeWrap(how string, f apiFunc) apiFunc {
	return func(r *http.Request) (any, error) {
		before := map[string]bool{}
		for _, n := range listFiles(s.p.src) {
			before[n] = true
		}
		v, err := f(r)
		if err != nil {
			return v, err
		}
		h := how
		if how == "" { // ドラッグ・パソコンから選ぶ（画面が how を付けて送る）
			h = "パネルから入れた"
			if r.MultipartForm != nil {
				switch vs := r.MultipartForm.Value["how"]; {
				case len(vs) > 0 && vs[0] == "drag":
					h = "ドラッグ"
				case len(vs) > 0 && vs[0] == "pick":
					h = "パソコンから選ぶ"
				}
			}
		}
		var added []string
		for _, n := range listFiles(s.p.src) {
			if !before[n] {
				added = append(added, n)
			}
		}
		s.intakeAdd(added, h)
		return v, nil
	}
}

type intakeJSON struct {
	At  string `json:"at"`
	How string `json:"how"`
}

// intakeInfo: 画面に出す「入れた日時・入れ方」（①の2つの一覧）
func (s *panelServer) intakeInfo() (map[string]intakeJSON, map[string]intakeJSON) {
	b := s.intakeLoad()
	if s.intakeSync(b) {
		s.intakeSave(b)
	}
	conv := func(m map[string]*intakeRec) map[string]intakeJSON {
		out := map[string]intakeJSON{}
		for k, r := range m {
			out[k] = intakeJSON{At: wareki(r.At), How: r.How}
		}
		return out
	}
	return conv(b.Src), conv(b.Stage)
}
