package main

// AI作業パネル：住所の登録（改善6・No.22）
//   - ②の確認画面の「住所として置き換える」（/api/addr/add）
//   - 氏名一覧のページの「住所の登録へ移す」（/api/names/moveaddr）：呼び名に入っている住所を、区分「住所」の行へ移す
// どちらも、ボタンを押したときだけ氏名一覧.xlsx を書き換える。記号対応表には触れない（前に渡した書類も元に戻せる）

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"
)

// checkAddrWord: 住所として登録できる語か
func checkAddrWord(w string) error {
	if w == "" {
		return fmt.Errorf("登録する住所が空です")
	}
	if utf8.RuneCountInString(w) > 80 || strings.ContainsAny(w, "\t【】") {
		return fmt.Errorf("住所の書き方が正しくありません（長すぎる、または【】が入っています）")
	}
	if !isAddrShape(w) {
		return fmt.Errorf("「%s」は番地まで書かれた住所ではないため、住所として登録しません（番地のない地名は置き換えません）。施設・建物・事業所の名前や、読み取りが崩れた字なら「事業所などの住所（除外）」か「住所ではない（除外）」を押してください", w)
	}
	return nil
}

// registeredAddrRow: すでに同じ住所（書き方をそろえた形が同じ）が登録されていれば、その行
func registeredAddrRow(rows []nameRow, w string) *nameRow {
	c := addrCanon(w)
	for i, r := range rows {
		if r.Kind == addrKind && addrCanon(r.Name) == c {
			return &rows[i]
		}
	}
	return nil
}

func (s *panelServer) addrAdd(r *http.Request) (any, error) {
	var q struct{ Word string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	w := cleanAddr(q.Word)
	if err := checkAddrWord(w); err != nil {
		return nil, err
	}
	if nameListOpenInExcel(s.p.nameList) {
		return nil, fmt.Errorf("氏名一覧.xlsx がExcelで開かれています。Excelを閉じてから、もう一度押してください")
	}
	rows, err := readNameRows(s.p.nameList)
	if err != nil {
		return nil, err
	}
	if ex := registeredAddrRow(rows, w); ex != nil {
		return map[string]string{"ok": "住所「" + ex.Name + "」は、すでに氏名一覧の " + fmt.Sprint(ex.Row) + " 行目に登録されています（二重には登録しません）", "already": "1"}, nil
	}
	if err := appendNameRow(s.p.nameList, addrKind, w); err != nil {
		return nil, err
	}
	return map[string]any{"ok": "住所「" + w + "」を登録しました（氏名一覧の区分「住所」）。次の「置き換える」から【住所…】の記号になります。この方の呼び名には入れていません", "pend": s.bumpPend(1)}, nil
}

// isBuildingAlias: 建物名・部屋番号だけの呼び名（前の版で「この語も呼び名に書き足す」で入れたもの）
func isBuildingAlias(a string) bool {
	return !isAddrShape(a) && (hasBuildingWord(a) || roomLike(a) || roomOnlyStrong(a))
}

// splitAddrAliases: 呼び名を、住所（番地まで書かれたもの）と、それ以外に分ける。
// 住所として登録する語には、住所のすぐ後ろに書き足してあった建物名・部屋番号をつなげた形も加える
func splitAddrAliases(alias string) (addrs, regs, rest []string) {
	lastAddr := ""
	for _, a := range aliasList(alias) {
		if isAddrShape(a) {
			addrs = append(addrs, a)
			regs = append(regs, a)
			lastAddr = a
			continue
		}
		rest = append(rest, a)
		if lastAddr != "" && isBuildingAlias(a) {
			regs = append(regs, lastAddr+" "+a)
			continue
		}
		lastAddr = ""
	}
	return
}

func (s *panelServer) namesMoveAddr(r *http.Request) (any, error) {
	var q nameReq
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	if q.Old.Row < 2 || q.Old.Kind == "除外" || q.Old.Kind == addrKind {
		return nil, fmt.Errorf("移す行が正しくありません")
	}
	addrs, regs, rest := splitAddrAliases(q.Old.Alias)
	if len(addrs) == 0 {
		return map[string]string{"ok": q.Old.Name + " の呼び名には、住所は入っていません"}, nil
	}
	added, already := []string{}, []string{}
	if err := modifyNameRow(s.p.nameList, q.Old, func(x string) (string, error) {
		nx, err := editNameRowXML(x, q.Old.Row, q.Old.Kind, q.Old.Name, strings.Join(rest, "／"))
		if err != nil {
			return "", err
		}
		rows, err := readNameRowsXML(s.p.nameList, nx)
		if err != nil {
			return "", err
		}
		seen := map[string]bool{}
		for _, w := range regs {
			w = cleanAddr(w)
			c := addrCanon(w)
			if seen[c] || checkAddrWord(w) != nil {
				continue
			}
			seen[c] = true
			if ex := registeredAddrRow(rows, w); ex != nil {
				already = append(already, w)
				continue
			}
			if nx, err = insertNameRow(nx, addrKind, w, ""); err != nil {
				return "", err
			}
			added = append(added, w)
		}
		return nx, nil
	}); err != nil {
		return nil, err
	}
	msg := q.Old.Kind + "「" + q.Old.Name + "」の呼び名から住所 " + fmt.Sprint(len(addrs)) + " 件（" + strings.Join(addrs, "、") + "）を外し、住所の登録に " + fmt.Sprint(len(added)) + " 件足しました"
	if len(added) > 0 {
		msg += "（" + strings.Join(added, "、") + "）"
	}
	if len(already) > 0 {
		msg += "。すでに登録済み：" + strings.Join(already, "、")
	}
	msg += "。記号対応表はそのままなので、前に渡した書類も元に戻せます"
	return map[string]string{"ok": msg}, nil
}
