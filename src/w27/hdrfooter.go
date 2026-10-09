package main

// ヘッダー・フッター（改善5・No.19）
//
// Excel のヘッダー・フッター（oddHeader・evenFooter・firstHeader など）の文字には、
// &L &C &R（左・中央・右）、&9（文字の大きさ）、&"ＭＳ 明朝,標準"（書体）、&K00FF00（色）、&P &N &D など
// の書式記号が混ざっている。「&9昭和15年…」の 9 が数字とつながって生年月日を見逃していたので、
// 書式記号を区切り（｜）に置き換えた「見るための文」で調べ、見つかった所だけを元の文字の中で置き換える。
// 書式記号そのものには一度も触れない（元に戻すと、元のヘッダーとバイト単位で同じになる）。
// Word のヘッダー・フッター（word/header*.xml・footer*.xml）は本文と同じ形なので、そのまま調べる。
// どちらも「ヘッダーの中」として、年齢（86歳）も記号にする。

import (
	"regexp"
	"strings"
)

// hdrSep: 書式記号の代わりに入れる区切り（どの規則の文字の並びにも入らない字）
const hdrSep = "｜"

var hdrElems = map[string]bool{"oddHeader": true, "oddFooter": true, "evenHeader": true, "evenFooter": true, "firstHeader": true, "firstFooter": true}

var reWordHdrPart = regexp.MustCompile(`^word/(header|footer)\d*\.xml$`)

// hdrSeg: 見るための文の中の、元の文字がそのまま入っている区間
type hdrSeg struct{ vStart, oStart, n int }

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// hdrCodeLen: s[i]=='&' から始まる書式記号の長さ（バイト）
func hdrCodeLen(s string, i int) int {
	if i+1 >= len(s) {
		return 1
	}
	c := s[i+1]
	switch {
	case c == '&':
		return 2 // && は文字の & だが、区切りとして扱う（中身は触らない）
	case c == '"':
		if j := strings.IndexByte(s[i+2:], '"'); j >= 0 {
			return j + 3
		}
		return len(s) - i
	case c >= '0' && c <= '9':
		k := i + 1
		for k < len(s) && k < i+4 && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		return k - i
	case c == 'K' || c == 'k':
		k := i + 2
		// &KRRGGBB または &Kttsnnn（テーマ色：&K01+000）
		for k < len(s) && k < i+8 && (isHexByte(s[k]) || s[k] == '+' || s[k] == '-') {
			k++
		}
		return k - i
	case c < 0x80:
		return 2 // &L &C &R &P &N &D &T &Z &F &A &G &B &I &U &E &X &Y &S &O &H など
	}
	return 1
}

// hdrView: Excel のヘッダーの文字から、書式記号を区切りに替えた文と、元の文字との対応を作る
func hdrView(t string) (string, []hdrSeg) {
	var b strings.Builder
	var segs []hdrSeg
	i, textStart := 0, 0
	lastSep := false
	flush := func(end int) {
		if end > textStart {
			segs = append(segs, hdrSeg{vStart: b.Len(), oStart: textStart, n: end - textStart})
			b.WriteString(t[textStart:end])
			lastSep = false
		}
	}
	for i < len(t) {
		if t[i] != '&' {
			i++
			continue
		}
		flush(i)
		n := hdrCodeLen(t, i)
		if !lastSep {
			b.WriteString(hdrSep)
			lastSep = true
		}
		i += n
		textStart = i
	}
	flush(len(t))
	return b.String(), segs
}

// hdrMapBack: 見るための文で見つかった所を、元の文字の位置に戻す。
// 区切りをまたぐものが1つでもあれば ok=false（呼ぶ側は、元の文字のまま調べ直す）
func hdrMapBack(ms []match, segs []hdrSeg) ([]match, bool) {
	out := make([]match, 0, len(ms))
	for _, m := range ms {
		found := false
		for _, sg := range segs {
			if m.start >= sg.vStart && m.end <= sg.vStart+sg.n {
				m2 := m
				m2.start = sg.oStart + (m.start - sg.vStart)
				m2.end = sg.oStart + (m.end - sg.vStart)
				out = append(out, m2)
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return out, true
}

// hdrTransform: Excel のヘッダー・フッターの文字を、書式記号に触れずに調べる
func hdrTransform(t string, sus bool, tf transformFunc) []match {
	if !strings.Contains(t, "&") {
		return tf(t, sus, true)
	}
	view, segs := hdrView(t)
	ms := tf(view, sus, true)
	if len(ms) == 0 {
		return nil
	}
	if back, ok := hdrMapBack(ms, segs); ok {
		return back
	}
	// 念のため（どの規則も区切りの字を含まないので、ここには来ない）：前の版と同じく元の文字のまま調べる
	return tf(t, sus, true)
}
