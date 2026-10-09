package main

// 前の方の残りを自動で脇によける（改善12 No.44）
// ②を押して置き換えが終わったとき、今回入れたファイルに出てくる利用者と、
// 前から残っていたファイル（渡す前の置き場 3_確認待ち・保留の 1_元データ）の利用者が1人も重ならなければ、
// その前からのファイルを マスキング作業＼_前の方の残り＼（日時）＼ へ移す（消さない）。「戻す」で元の置き場に戻る。
// 利用者が分からないファイル（記号が1つもない）や、1人でも重なるファイルは動かさない（今までどおり①の知らせを出す）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const asideDirName = "_前の方の残り"

type asideFile struct {
	Where string     `json:"where"` // stage / src
	Name  string     `json:"name"`
	Users []string   `json:"users"`
	Rec   *intakeRec `json:"rec,omitempty"`
}

type asideRecord struct {
	ID    string      `json:"id"`
	At    time.Time   `json:"at"`
	Users []string    `json:"users"`
	Files []asideFile `json:"files"`
	Last  []fileJSON  `json:"last,omitempty"` // ②の画面の表示（戻したときに元に戻す）
}

// asideJSON: 画面に出す知らせ
type asideJSON struct {
	ID    string   `json:"id"`
	Users []string `json:"users"`
	NSt   int      `json:"nStage"`
	NSrc  int      `json:"nSrc"`
	Files []string `json:"files"`
}

var reAsideID = regexp.MustCompile(`^\d{8}_\d{6}(?:_\d+)?$`)

func (s *panelServer) asideRoot() string { return filepath.Join(s.p.work, asideDirName) }

type asideCand struct {
	where, name string
	users       []string
}

func disjoint(a []string, b map[string]bool) bool {
	for _, x := range a {
		if b[x] {
			return false
		}
	}
	return true
}

// moveAside: 候補のファイルを脇へ移す。移せなかったファイルはそのまま残す
func (s *panelServer) moveAside(cands []asideCand, rep *maskReport, ib *intakeBook) *asideJSON {
	if len(cands) == 0 {
		return nil
	}
	id := time.Now().Format("20060102_150405")
	dir := filepath.Join(s.asideRoot(), id)
	for i := 2; ; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		id = time.Now().Format("20060102_150405") + fmt.Sprintf("_%d", i)
		dir = filepath.Join(s.asideRoot(), id)
	}
	rec := &asideRecord{ID: id, At: time.Now()}
	users := map[string]bool{}
	out := &asideJSON{ID: id}
	moved := map[string]bool{}
	for _, c := range cands {
		sub := "3_確認待ち"
		from := filepath.Join(s.p.stage, c.name)
		if c.where == "src" {
			sub = "1_元データ"
			from = filepath.Join(s.p.src, c.name)
		}
		if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
			continue
		}
		if err := os.Rename(from, filepath.Join(dir, sub, c.name)); err != nil {
			continue // 開いているなどで移せないものは、そのまま残す
		}
		af := asideFile{Where: c.where, Name: c.name, Users: c.users}
		if c.where == "src" {
			af.Rec = ib.Src[c.name]
			delete(ib.Src, c.name)
			out.NSrc++
		} else {
			af.Rec = ib.Stage[c.name]
			delete(ib.Stage, c.name)
			out.NSt++
		}
		rec.Files = append(rec.Files, af)
		moved[c.where+"|"+c.name] = true
		out.Files = append(out.Files, c.name)
		for _, u := range c.users {
			users[u] = true
		}
	}
	if len(rec.Files) == 0 {
		os.Remove(dir)
		return nil
	}
	for u := range users {
		rec.Users = append(rec.Users, u)
	}
	sort.Strings(rec.Users)
	out.Users = rec.Users
	// ②の一覧から外す（戻したときに戻せるよう控える）
	var keep []fileJSON
	for _, f := range rep.Files {
		if (f.Status == "ok" && moved["stage|"+f.OutName]) || (f.Status != "ok" && moved["src|"+f.Name]) {
			rec.Last = append(rec.Last, f)
			continue
		}
		keep = append(keep, f)
	}
	rep.Files = keep
	if b, err := json.MarshalIndent(rec, "", " "); err == nil {
		copyBytes(filepath.Join(dir, "よけた記録.json"), b)
	}
	return out
}

// asideRestore: 「戻す」。脇へよけたファイルを元の置き場に戻す
func (s *panelServer) asideRestore(r *http.Request) (any, error) {
	var q struct{ ID string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	if !reAsideID.MatchString(q.ID) {
		return nil, fmt.Errorf("戻す先が正しくありません")
	}
	dir := filepath.Join(s.asideRoot(), q.ID)
	b, err := os.ReadFile(filepath.Join(dir, "よけた記録.json"))
	if err != nil {
		return nil, fmt.Errorf("よけたファイルが見つかりません（もう戻したか、動かされました）")
	}
	var rec asideRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("よけた記録を読めません")
	}
	ib := s.intakeLoad()
	var back, fail []string
	for _, f := range rec.Files {
		if f.Name != filepath.Base(f.Name) || strings.HasPrefix(f.Name, ".") {
			continue
		}
		sub, to := "3_確認待ち", s.p.stage
		if f.Where == "src" {
			sub, to = "1_元データ", s.p.src
		}
		dst := uniquePath(to, f.Name)
		if err := os.Rename(filepath.Join(dir, sub, f.Name), dst); err != nil {
			fail = append(fail, f.Name)
			continue
		}
		back = append(back, filepath.Base(dst))
		r := f.Rec
		if r == nil {
			r = &intakeRec{At: rec.At, How: howDirect}
		}
		if f.Where == "src" {
			ib.Src[filepath.Base(dst)] = r
		} else {
			ib.Stage[filepath.Base(dst)] = r
		}
	}
	s.intakeSave(ib)
	if len(fail) == 0 {
		os.Remove(filepath.Join(dir, "よけた記録.json"))
		os.Remove(filepath.Join(dir, "3_確認待ち"))
		os.Remove(filepath.Join(dir, "1_元データ"))
		os.Remove(dir)
		os.Remove(s.asideRoot()) // 空のときだけ消える
	}
	if s.last != nil {
		if s.last.Aside != nil && s.last.Aside.ID == q.ID {
			s.last.Aside = nil
		}
		s.last.Files = append(rec.Last, s.last.Files...)
		if b, err := json.Marshal(s.last); err == nil {
			os.WriteFile(filepath.Join(s.p.report, "last.json"), b, 0600)
		}
	}
	msg := fmt.Sprintf("脇によけたファイル %d 件を、元の置き場に戻しました", len(back))
	if len(fail) > 0 {
		msg += "。戻せなかったファイル：" + strings.Join(fail, "、") + "（開いていたら閉じて、もう一度押してください）"
	}
	return map[string]any{"ok": msg, "back": back, "fail": fail}, nil
}
