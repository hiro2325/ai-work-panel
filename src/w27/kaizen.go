package main

// 気づいたことを書く（改善13 No.3）
//
// トップ画面の「気づいたことを書く」欄から、AI作業フォルダの 00_指示＼パネル改善リスト.md の
// 「候補の一覧」の表の最後に1行足す：| 次の番号 | R8.9.30 | 佐藤（パネル） | 書いた文 | 任せる | 候補 | |
//   - 書く前に、置き換えの仕組みで文を調べる。記号に置き換わる語・保留（要確認）になる語が1つでもあれば、書かずに知らせる
//   - 表を壊さない：改行は「／」、| は「｜」に。1000字まで（改善20 No.48）
//   - ファイルが無い・表が見つからないときは、表を作らずに知らせる。ほかの行は1文字も変えない（足す1行だけ）

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const kaizenListName = "パネル改善リスト.md"
const kaizenMaxRunes = 1000 // 改善20 No.48（前は200）

var reKaizenNL = regexp.MustCompile(`(?:\r\n|\r|\n)+`)

// kaizenClean: 表の1マスに入れる形（改行は「／」、| は「｜」、前後の空白は外す）
func kaizenClean(t string) string {
	// 目に見えない制御文字（改善13 No.3 差し戻し）：行を分ける字（縦タブ・改ページ・NEL・U+2028/2029）は改行と同じ「／」、
	// ゼロ幅の字は取り除き（名前の間に入っていても調べられるように）、そのほかの制御文字は空白にする
	t = strings.Map(func(r rune) rune {
		switch {
		case r == '\v' || r == '\f' || r == 0x85 || r == 0x2028 || r == 0x2029:
			return '\n'
		case r == 0x200B || r == 0x200C || r == 0x200D || r == 0x2060 || r == 0xFEFF || r == 0x00AD:
			return -1
		case r == '\n' || r == '\r' || r == '\t':
			return r
		case r < 0x20 || r == 0x7F || (r >= 0x80 && r < 0xA0):
			return ' '
		}
		return r
	}, t)
	t = strings.TrimSpace(t)
	t = reKaizenNL.ReplaceAllString(t, "／")
	t = strings.ReplaceAll(t, "|", "｜")
	t = strings.ReplaceAll(t, "\t", " ")
	return strings.TrimSpace(t)
}

// kaizenTable: 「候補の一覧」の表の行の範囲（行番号。start は見出し行、end は最後の行の次）
//
//	「候補の一覧」の見出しのあとの最初の表から、次の見出し（またはファイルの終わり）までの表の行すべて（空行をはさんでも同じ表）。見出しがなければ、「| No | 日付 | 出どころ | 内容 | 区分 | 状態 | メモ |」の表が1つだけのとき、その表
func kaizenTable(lines []string) (int, int, error) {
	isRow := func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "|") }
	isHead := func(l string) bool {
		c := strings.Split(strings.TrimSpace(l), "|")
		return len(c) >= 8 && strings.TrimSpace(c[1]) == "No"
	}
	tableAt := func(i int) (int, int, bool) {
		if i+1 >= len(lines) || !isHead(lines[i]) || !strings.HasPrefix(strings.TrimSpace(lines[i+1]), "|---") && !strings.HasPrefix(strings.TrimSpace(lines[i+1]), "| ---") && !strings.HasPrefix(strings.TrimSpace(lines[i+1]), "|:") {
			return 0, 0, false
		}
		e := i + 2
		for e < len(lines) && isRow(lines[e]) {
			e++
		}
		return i, e, true
	}
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "#") && strings.Contains(t, "候補の一覧") {
			for j := i + 1; j < len(lines); j++ {
				tj := strings.TrimSpace(lines[j])
				if strings.HasPrefix(tj, "#") {
					break // 次の見出しまでに表がない
				}
				if st, en, ok := tableAt(j); ok {
					// 次の見出しかファイルの終わりまでの表の行は、空行をはさんでも全部同じ表（本物は No.11 のあとに空行があり、2つのかたまりに分かれている）
					for k := en; k < len(lines); k++ {
						tk := strings.TrimSpace(lines[k])
						if strings.HasPrefix(tk, "#") {
							break
						}
						if isRow(lines[k]) {
							en = k + 1
						}
					}
					return st, en, nil
				}
			}
			return 0, 0, fmt.Errorf("「候補の一覧」の見出しの下に表が見つかりません")
		}
	}
	var found [][2]int
	for i := range lines {
		if st, en, ok := tableAt(i); ok {
			found = append(found, [2]int{st, en})
		}
	}
	if len(found) == 1 {
		return found[0][0], found[0][1], nil
	}
	if len(found) == 0 {
		return 0, 0, fmt.Errorf("「候補の一覧」の表（| No | 日付 | … |）が見つかりません")
	}
	return 0, 0, fmt.Errorf("「候補の一覧」の見出しがなく、表が %d つあるため、どこに書くか決められません", len(found))
}

// kaizenCheck: 名前・住所・番号らしきものがあれば、その語を返す
func (s *panelServer) kaizenCheck(text string) (string, error) {
	people, excl, err := readNameList(s.p.nameList)
	if err != nil {
		return "", err
	}
	cb, err := loadCodeBook(s.p.codeBookF)
	if err != nil {
		return "", err
	}
	m := newMasker(people, excl, cb) // 記号対応表は保存しない（調べるだけ）
	if ms := m.findMatches(text, true); len(ms) > 0 {
		return text[ms[0].start:ms[0].end], nil
	}
	if sus := m.findSuspects(text); len(sus) > 0 {
		return sus[0].word, nil
	}
	return "", nil
}

func (s *panelServer) kaizenAdd(r *http.Request) (any, error) {
	var q struct{ Text string }
	if err := readJSON(r, &q); err != nil {
		return nil, fmt.Errorf("書いた文を受け取れませんでした")
	}
	text := kaizenClean(q.Text)
	if text == "" {
		return nil, fmt.Errorf("書く欄が空です。気づいたことを一言書いてから「書く」を押してください")
	}
	if n := utf8.RuneCountInString(text); n > kaizenMaxRunes {
		return nil, fmt.Errorf("長すぎます（%d字）。%d字までにしてください", n, kaizenMaxRunes)
	}
	if w, err := s.kaizenCheck(text); err != nil {
		return nil, err
	} else if w != "" {
		return nil, fmt.Errorf("名前・番号・住所らしき語『%s』があるので書きませんでした。その語を消してもう一度書いてください", w)
	}
	if s.p.ai == "" {
		return nil, fmt.Errorf("AI作業フォルダが見つからないため、書きませんでした")
	}
	path := filepath.Join(s.p.ai, "00_指示", kaizenListName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("00_指示＼%s が見つからないため、書きませんでした", kaizenListName)
	}
	nl := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		nl = "\r\n"
	}
	// 行の頭の位置（バイト）を数える。ほかの行は1文字も変えないため、元の文字の並びにそのまま差し込む
	str := string(data)
	var starts []int
	var lines []string
	for i := 0; i < len(str); {
		starts = append(starts, i)
		j := strings.IndexByte(str[i:], '\n')
		if j < 0 {
			lines = append(lines, strings.TrimSuffix(str[i:], "\r"))
			i = len(str)
			break
		}
		lines = append(lines, strings.TrimSuffix(str[i:i+j], "\r"))
		i += j + 1
	}
	if len(lines) > 0 {
		lines[0] = strings.TrimPrefix(lines[0], bom)
	}
	st, en, err := kaizenTable(lines)
	if err != nil {
		return nil, fmt.Errorf("%s の%s。書きませんでした", kaizenListName, err.Error())
	}
	maxNo := 0
	for _, l := range lines[st+2 : en] {
		c := strings.Split(strings.TrimSpace(l), "|")
		if len(c) < 2 {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(c[1])); err == nil && n > maxNo {
			maxNo = n
		}
	}
	no := maxNo + 1
	row := fmt.Sprintf("| %d | %s | 佐藤（パネル） | %s | 任せる | 候補 | |", no, warekiDate(time.Now()), text)
	// 差し込む位置：表の最後の行の次の行の頭
	var out string
	if en < len(starts) {
		out = str[:starts[en]] + row + nl + str[starts[en]:]
	} else if strings.HasSuffix(str, "\n") {
		out = str + row + nl
	} else {
		out = str + nl + row // 最後の行に改行がないときは、改行してから足す（元の行の文字は変えない）
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0644); err != nil {
		return nil, fmt.Errorf("書けませんでした: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("書けませんでした（%s が開かれているかもしれません）: %v", kaizenListName, err)
	}
	return map[string]any{"ok": fmt.Sprintf("パネル改善リストに No.%d として書きました。ありがとうございます", no), "no": no}, nil
}
