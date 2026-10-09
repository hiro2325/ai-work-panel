package main

// 氏名一覧の重複を、何人分でも1回でまとめる（改善22・R8.10.5 注文）
//   - 同じ区分・同じ氏名（スペースの違いは同じとみなす）の行を1行にまとめる。
//   - 残す行：利用者・家族は姓と名の間にスペースのある行を優先、ほかは上の行（行番号の小さいほう）。
//   - 呼び名は消さずに、残す行に集める（同じ呼び名は1つに）。
//   - まとめる前に、氏名一覧.xlsx の控えを 対応表＼控え に取る。
//   - 押す前に画面で一覧を見せ、チェックした人だけをまとめる。画面を開いたあとでファイルが変わっていたら、何も書かない。
//   - 記号対応表は消さない（前に渡した書類も元に戻せる）。

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type dupGroup struct {
	Key   string    `json:"key"`
	Kind  string    `json:"kind"`
	Name  string    `json:"name"`  // 残す行の氏名
	Keep  int       `json:"keep"`  // 残す行の番号
	Alias string    `json:"alias"` // まとめたあとの呼び名
	Rows  []nameRow `json:"rows"`
	Codes []string  `json:"codes,omitempty"` // 記号（書き方の違いで記号が分かれているとき2つ以上）
}

func dupKey(r nameRow) string { return r.Kind + "|" + normName(r.Name) }

// dupGroups: 同じ区分・同じ氏名の行のまとまり（2行以上のもの）。上の行から順
func dupGroups(rows []nameRow) []dupGroup {
	idx := map[string]int{}
	var gs []dupGroup
	for _, r := range rows {
		if strings.TrimSpace(r.Name) == "" {
			continue
		}
		k := dupKey(r)
		i, ok := idx[k]
		if !ok {
			i = len(gs)
			idx[k] = i
			gs = append(gs, dupGroup{Key: k, Kind: r.Kind})
		}
		gs[i].Rows = append(gs[i].Rows, r)
	}
	var out []dupGroup
	for _, g := range gs {
		if len(g.Rows) < 2 {
			continue
		}
		keep := g.Rows[0]
		if g.Kind == "利用者" || g.Kind == "家族" {
			for _, r := range g.Rows {
				if strings.ContainsAny(r.Name, " 　") {
					keep = r
					break
				}
			}
		}
		g.Keep, g.Name = keep.Row, keep.Name
		g.Alias = mergedAlias(keep, g.Rows)
		seen := map[string]bool{}
		for _, r := range g.Rows {
			if r.Code != "" && !seen[r.Code] {
				seen[r.Code] = true
				g.Codes = append(g.Codes, r.Code)
			}
		}
		out = append(out, g)
	}
	return out
}

// mergedAlias: 残す行の呼び名のあとに、ほかの行の呼び名を足す（同じものは1つ）
func mergedAlias(keep nameRow, rows []nameRow) string {
	var out []string
	seen := map[string]bool{}
	add := func(a string) {
		for _, x := range aliasList(a) {
			if !seen[normName(x)] {
				seen[normName(x)] = true
				out = append(out, x)
			}
		}
	}
	add(keep.Alias)
	for _, r := range rows {
		if r.Row != keep.Row {
			add(r.Alias)
		}
	}
	// ほかの行の氏名がスペースの付け方だけ違う書き方のときは、呼び名にしなくてよい（同じ書き方とみなして置き換わる）
	return strings.Join(out, "／")
}

// /api/names/merge：チェックした人の重複を1回でまとめる
func (s *panelServer) namesMerge(r *http.Request) (any, error) {
	var q struct{ Groups []dupGroup }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	if len(q.Groups) == 0 {
		return nil, fmt.Errorf("まとめる人が選ばれていません")
	}
	if len(q.Groups) > 2000 {
		return nil, fmt.Errorf("まとめる人が多すぎます")
	}
	if nameListOpenInExcel(s.p.nameList) {
		return nil, fmt.Errorf("氏名一覧.xlsx がExcelで開かれています。Excelを閉じてから、もう一度押してください")
	}
	// 控えと書き換えの元は、同じ1回の読み取りから（点検役 R8.10.5）
	zr, data, sheet, err := readNameBook(s.p.nameList)
	if err != nil {
		return nil, err
	}
	cur, err := parseNameRows(sheet, zipSST(zr))
	if err != nil {
		return nil, err
	}
	byRow := map[int]nameRow{}
	for _, x := range cur {
		byRow[x.Row] = x
	}
	// 画面で見た内容と、今のファイルが同じかを全部確かめてから書く（1つでも違えば何も書かない）
	type plan struct {
		keep  nameRow
		alias string
		drop  []int
	}
	var plans []plan
	used := map[int]bool{}
	for _, g := range q.Groups {
		if len(g.Rows) < 2 {
			continue
		}
		var rows []nameRow
		for _, o := range g.Rows {
			c, ok := byRow[o.Row]
			if !ok || o.Row < 2 || c.Kind != o.Kind || c.Name != o.Name || c.Alias != o.Alias || used[o.Row] {
				return nil, fmt.Errorf("氏名一覧が、画面を開いたあとで変わっています。画面を読み直してから、もう一度押してください（まだ何も変えていません）")
			}
			used[o.Row] = true
			rows = append(rows, c)
		}
		k := dupKey(rows[0])
		for _, x := range rows[1:] {
			if dupKey(x) != k {
				return nil, fmt.Errorf("同じ区分・同じ氏名ではない行がまじっています。画面を読み直してください（まだ何も変えていません）")
			}
		}
		// 残す行・呼び名はサーバーで決め直す（画面の値は使わない）
		gg := dupGroups(rows)
		if len(gg) != 1 {
			continue
		}
		var keep nameRow
		var drop []int
		for _, x := range rows {
			if x.Row == gg[0].Keep {
				keep = x
			} else {
				drop = append(drop, x.Row)
			}
		}
		plans = append(plans, plan{keep: keep, alias: gg[0].Alias, drop: drop})
	}
	if len(plans) == 0 {
		return nil, fmt.Errorf("まとめる重複がありませんでした")
	}
	// 控え（まとめる前のファイルをそのまま）
	bdir := filepath.Join(filepath.Dir(s.p.nameList), "控え")
	if err := os.MkdirAll(bdir, 0700); err != nil {
		return nil, fmt.Errorf("「控え」のフォルダを作れませんでした（まだ何も変えていません）：%v", err)
	}
	bp := uniquePath(bdir, "氏名一覧_重複をまとめる前_"+time.Now().Format("20060102_150405")+".xlsx")
	if err := writeNew(bp, data); err != nil {
		return nil, fmt.Errorf("控えを取れませんでした（まだ何も変えていません）：%v", err)
	}
	x := string(sheet)
	long := 0
	// 1) 残す行の呼び名を書き換える（行の番号は変わらない）
	for _, p := range plans {
		if p.alias == p.keep.Alias {
			continue
		}
		if utf8.RuneCountInString(p.alias) > 120 {
			long++
		}
		nx, err := editNameRowXML(x, p.keep.Row, p.keep.Kind, p.keep.Name, p.alias)
		if err != nil {
			return nil, err
		}
		x = nx
	}
	// 2) いらない行を、下の行から消す（上の行の番号がずれないように）
	var drops []int
	for _, p := range plans {
		drops = append(drops, p.drop...)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(drops)))
	shiftedAll := true
	for _, n := range drops {
		nx, sh, err := deleteNameRowXML(x, n)
		if err != nil {
			return nil, err
		}
		shiftedAll = shiftedAll && sh
		x = nx
	}
	if err := writeNameSheet(s.p.nameList, zr, x); err != nil {
		return nil, err
	}
	var who []string
	for _, p := range plans {
		who = append(who, fmt.Sprintf("%s（%d行→1行）", p.keep.Name, len(p.drop)+1))
	}
	msg := fmt.Sprintf("%d 人分の重複をまとめ、%d 行を減らしました：%s。呼び名は残した行に集めました。まとめる前の氏名一覧は「控え」に残してあります（%s）。記号対応表は消していないので、前に渡した書類も元に戻せます",
		len(plans), len(drops), strings.Join(who, "、"), filepath.Base(bp))
	if !shiftedAll {
		msg += "。※この氏名一覧には結合セルなどがあるため、行の中身だけを消し、空いた行はそのまま残しました"
	}
	if long > 0 {
		msg += fmt.Sprintf("。※呼び名が長くなった方が %d 人います（そのまま使えます）", long)
	}
	// 渡す前のファイルも、まとめたあとの氏名一覧で置き換え直す（②を開くと自動で。改善23）
	return map[string]any{"ok": msg, "merged": len(plans), "removed": len(drops), "backup": filepath.Base(bp), "pend": s.bumpPend(1)}, nil
}

// uniqueAliases: old の呼び名のうち、target の呼び名にないもの（同じ書き方は1つとみなす）
func uniqueAliases(old, target nameRow) []string {
	have := map[string]bool{}
	for _, a := range aliasList(target.Alias) {
		have[normName(a)] = true
	}
	var out []string
	for _, a := range aliasList(old.Alias) {
		if !have[normName(a)] {
			have[normName(a)] = true
			out = append(out, a)
		}
	}
	return out
}

// namesDeleteMove：重複行を1行消すとき、その行にしかない呼び名を、同じ方の残す行に移してから消す（改善25）。
// 画面で見た2行と今のファイルが同じことを確かめてから、1回で書く（違えば何も書かない）
func (s *panelServer) namesDeleteMove(old, target nameRow) (any, error) {
	if nameListOpenInExcel(s.p.nameList) {
		return nil, fmt.Errorf("氏名一覧.xlsx がExcelで開かれています。Excelを閉じてから、もう一度押してください")
	}
	if old.Row == target.Row || dupKey(old) != dupKey(target) {
		return nil, fmt.Errorf("呼び名を移す先は、同じ区分・同じ氏名のほかの行にしてください（まだ何も変えていません）")
	}
	zr, _, sheet, err := readNameBook(s.p.nameList)
	if err != nil {
		return nil, err
	}
	cur, err := parseNameRows(sheet, zipSST(zr))
	if err != nil {
		return nil, err
	}
	ok := 0
	for _, c := range cur {
		for _, o := range []nameRow{old, target} {
			if c.Row == o.Row && c.Kind == o.Kind && c.Name == o.Name && c.Alias == o.Alias {
				ok++
			}
		}
	}
	if ok != 2 {
		return nil, fmt.Errorf("氏名一覧が、画面を開いたあとで変わっていました。新しい内容で表示し直したので、もう一度押してください（まだ何も変えていません）")
	}
	add := uniqueAliases(old, target)
	x := string(sheet)
	na := target.Alias
	if len(add) > 0 {
		na = cleanAlias(target.Alias + "／" + strings.Join(add, "／"))
		// 先に残す行の呼び名を書き換える（行の番号は変わらない）。そのあと消す
		if x, err = editNameRowXML(x, target.Row, target.Kind, target.Name, na); err != nil {
			return nil, err
		}
	}
	nx, shifted, err := deleteNameRowXML(x, old.Row)
	if err != nil {
		return nil, err
	}
	if err := writeNameSheet(s.p.nameList, zr, nx); err != nil {
		return nil, err
	}
	msg := old.Kind + "「" + old.Name + "」の重複の行（" + fmt.Sprint(old.Row) + "行目）を消しました"
	if len(add) > 0 {
		msg += "。この行にしかなかった呼び名「" + strings.Join(add, "／") + "」は、" + fmt.Sprint(target.Row) + "行目の呼び名に移しました"
		if utf8.RuneCountInString(na) > 120 {
			msg += "（呼び名が長くなりました。そのまま使えますが、「直す」で書き足すときは短くしてください）"
		}
	}
	if !shifted {
		msg += "（この氏名一覧には結合セルなどがあるため、行の中身だけを消し、空いた行はそのまま残しました）"
	}
	msg += "。記号対応表は消していません"
	s.bumpPend(1)
	return map[string]string{"ok": msg}, nil
}
