package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const bom = "\xef\xbb\xbf"

// codebook file: TSV (UTF-8 with BOM): key \t 元の文字列 \t 記号
func loadCodeBook(path string) (*codeBook, error) {
	cb := newCodeBook()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cb, nil
	}
	if err != nil {
		return nil, err
	}
	s := strings.TrimPrefix(string(data), bom)
	for i, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if i == 0 || line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		cb.add(unesc(f[0]), unesc(f[1]), unesc(f[2]))
	}
	return cb, nil
}

func esc(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\n", "\\n", "\r", "").Replace(s)
}
func unesc(s string) string {
	return strings.NewReplacer("\\t", "\t", "\\n", "\n", "\\\\", "\\").Replace(s)
}

func saveCodeBook(path string, cb *codeBook) error {
	var b strings.Builder
	b.WriteString(bom + "種別キー\t元の文字列\t記号\r\n")
	for _, k := range cb.order {
		b.WriteString(esc(k) + "\t" + esc(cb.keyOrig[k]) + "\t" + esc(cb.keyToCode[k]) + "\r\n")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---- 氏名一覧.xlsx reader (read-only) ----

type xSST struct {
	SI []struct {
		T string `xml:"t"`
		R []struct {
			T string `xml:"t"`
		} `xml:"r"`
	} `xml:"si"`
}

type xSheet struct {
	Rows []struct {
		C []struct {
			R  string `xml:"r,attr"`
			T  string `xml:"t,attr"`
			V  string `xml:"v"`
			IS struct {
				T string `xml:"t"`
				R []struct {
					T string `xml:"t"`
				} `xml:"r"`
			} `xml:"is"`
		} `xml:"c"`
	} `xml:"sheetData>row"`
}

func readZipEntry(zr *zip.ReadCloser, name string) ([]byte, bool) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, false
			}
			defer rc.Close()
			b, err := io.ReadAll(rc)
			return b, err == nil
		}
	}
	return nil, false
}

func colOf(ref string) int {
	n := 0
	for _, c := range ref {
		if c >= 'A' && c <= 'Z' {
			n = n*26 + int(c-'A'+1)
		} else {
			break
		}
	}
	return n
}

// readNameList returns people and exclude words from 氏名一覧.xlsx (first sheet).
func readNameList(path string) ([]person, []string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, nil, fmt.Errorf("氏名一覧.xlsx を開けません。Excelで開いている場合は閉じてください（%v）", err)
	}
	defer zr.Close()
	var sst []string
	if b, ok := readZipEntry(zr, "xl/sharedStrings.xml"); ok {
		var x xSST
		if err := xml.Unmarshal(b, &x); err == nil {
			for _, si := range x.SI {
				s := si.T
				for _, r := range si.R {
					s += r.T
				}
				sst = append(sst, s)
			}
		}
	}
	b, ok := readZipEntry(zr, "xl/worksheets/sheet1.xml")
	if !ok {
		return nil, nil, fmt.Errorf("氏名一覧.xlsx の1枚目のシートが読めません")
	}
	var sh xSheet
	if err := xml.Unmarshal(bytes.TrimPrefix(b, []byte(bom)), &sh); err != nil {
		return nil, nil, fmt.Errorf("氏名一覧.xlsx の読み取りに失敗しました: %v", err)
	}
	var people []person
	var excl []string
	readingCol := false // 4列目の見出しが「読み」「フリガナ」なら、その列を読みとして使う（改善C No.40）
	for ri, row := range sh.Rows {
		if ri == 0 {
			for _, c := range row.C {
				if colOf(c.R) != 4 {
					continue
				}
				v := c.V
				switch c.T {
				case "s":
					if i, err := strconv.Atoi(c.V); err == nil && i < len(sst) {
						v = sst[i]
					}
				case "inlineStr":
					v = c.IS.T
					for _, r := range c.IS.R {
						v += r.T
					}
				}
				readingCol = strings.Contains(v, "読") || strings.Contains(v, "フリガナ") || strings.Contains(v, "ふりがな") || strings.Contains(v, "カナ")
			}
			continue // header
		}
		cols := map[int]string{}
		for _, c := range row.C {
			v := c.V
			switch c.T {
			case "s":
				if i, err := strconv.Atoi(c.V); err == nil && i < len(sst) {
					v = sst[i]
				}
			case "inlineStr":
				v = c.IS.T
				for _, r := range c.IS.R {
					v += r.T
				}
			}
			cols[colOf(c.R)] = strings.TrimSpace(v)
		}
		kind, name, al := cols[1], cols[2], cols[3]
		if name == "" {
			continue
		}
		if kind == "除外" {
			excl = append(excl, name)
			continue
		}
		if kind == "" {
			kind = "人物"
		}
		var aliases []string
		for _, a := range strings.FieldsFunc(al, func(r rune) bool { return r == '/' || r == '／' || r == '、' || r == ',' || r == '，' }) {
			if a = strings.TrimSpace(a); a != "" {
				aliases = append(aliases, a)
			}
		}
		rd := ""
		if readingCol {
			rd = cols[4]
		}
		people = append(people, person{kind: kind, name: name, aliases: aliases, reading: rd})
	}
	return mergeDupPeople(people), excl, nil
}

// mergeDupPeople: 同じ区分・同じ氏名（スペースの違いは同じ）の行が何行あっても、1人として扱う（改善23）。
// 重複行のせいで、同じ方が2つの記号（利用者134 と 利用者142）に分かれてAIに渡るのを防ぐ。
// 残す書き方は「重複をまとめる」と同じ（利用者・家族は姓と名の間にスペースのある行、ほかは上の行）。呼び名は集める。
func mergeDupPeople(people []person) []person {
	idx := map[string]int{}
	var out []person
	for _, p := range people {
		k := p.kind + "|" + normName(p.name)
		i, ok := idx[k]
		if !ok {
			idx[k] = len(out)
			out = append(out, p)
			continue
		}
		q := &out[i]
		if (p.kind == "利用者" || p.kind == "家族") && !strings.ContainsAny(q.name, " 　") && strings.ContainsAny(p.name, " 　") {
			q.aliases, p.aliases = p.aliases, q.aliases
			q.name = p.name
		}
		seen := map[string]bool{}
		for _, a := range q.aliases {
			seen[normName(a)] = true
		}
		for _, a := range p.aliases {
			if !seen[normName(a)] {
				seen[normName(a)] = true
				q.aliases = append(q.aliases, a)
			}
		}
		if q.reading == "" {
			q.reading = p.reading
		}
	}
	return out
}
