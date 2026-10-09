package main

// 改善C（R8.9.30）：置き換える前に、書類の文字全体を読む（下の名前が同じ別の人の記号の選び方・薬情の見分け・読みの取り出しに使う）。
// 書類そのものには触れない。

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

var (
	reSIBlock  = regexp.MustCompile(`(?s)<si>(.*?)</si>|<is>(.*?)</is>`)
	reRPhBlock = regexp.MustCompile(`(?s)<rPh\b[^>]*>(.*?)</rPh>`)
	reTText    = regexp.MustCompile(`(?s)<t(?:\s[^>]*)?>([^<]*)</t>`)
)

// rphPairs: Excel の文字（共有文字・セル内の文字）の、元の文字とふりがなの組
func rphPairs(x []byte) [][2]string {
	var out [][2]string
	for _, m := range reSIBlock.FindAllSubmatch(x, -1) {
		inner := m[1]
		if inner == nil {
			inner = m[2]
		}
		if !bytes.Contains(inner, []byte("<rPh")) {
			continue
		}
		var reading strings.Builder
		for _, r := range reRPhBlock.FindAllSubmatch(inner, -1) {
			for _, t := range reTText.FindAllSubmatch(r[1], -1) {
				reading.WriteString(decodeEntities(string(t[1])))
			}
		}
		base := reRPhBlock.ReplaceAll(inner, nil)
		var b strings.Builder
		for _, t := range reTText.FindAllSubmatch(base, -1) {
			b.WriteString(decodeEntities(string(t[1])))
		}
		if reading.Len() > 0 && b.Len() > 0 {
			out = append(out, [2]string{b.String(), reading.String()})
		}
	}
	return out
}

// docTextOOXML: 書類（docx/xlsx/xlsm）の文字を、段落・セルごとの行にして返す（と Excel のふりがな）
func docTextOOXML(path string) ([]string, [][2]string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	return docTextOOXMLData(data)
}

// docTextOOXMLData: docTextOOXML の中身（バイト列から）
func docTextOOXMLData(data []byte) ([]string, [][2]string) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil
	}
	var lines []string
	var rph [][2]string
	for _, f := range zr.File {
		rule := ruleFor(f.Name)
		if rule == nil {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		x, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		if f.Name == "xl/sharedStrings.xml" || reSheetPart.MatchString(f.Name) {
			rph = append(rph, rphPairs(x)...)
		}
		nodes, _, ok := scanXML(x, *rule)
		if !ok {
			continue
		}
		var order []int
		texts := map[int]*strings.Builder{}
		for _, n := range nodes {
			if n.blank {
				continue
			}
			b, seen := texts[n.group]
			if !seen {
				b = &strings.Builder{}
				texts[n.group] = b
				order = append(order, n.group)
			}
			b.WriteString(n.text)
		}
		sort.Ints(order)
		for _, g := range order {
			if t := texts[g].String(); strings.TrimSpace(t) != "" {
				lines = append(lines, t)
			}
		}
	}
	return lines, rph
}
