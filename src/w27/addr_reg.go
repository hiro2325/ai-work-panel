package main

// 住所の登録と【住所NNN】（改善6・No.22）
//
// 住所は人（利用者・家族の呼び名）に結び付けず、登録した住所だけを【住所001】【住所002】…の記号にする。
//   - 登録先は 氏名一覧.xlsx の区分「住所」の行（氏名の欄に住所を書く。呼び名は使わない）
//   - 〒・郵便番号、都道府県・市区町村・町域・番地・建物名・部屋番号を「ひとかたまり」として1つの記号にする。
//     番地のあとの改行（セル内改行・ヘッダーの改行）や書式記号をまたいで続く建物名・部屋番号も含める。
//     改行そのもの・書式記号・Word の文字の区切り（run）は記号の外に残す（元に戻すとバイト単位で元どおり）。
//     その場合は【住所001】【住所001の続き】のように分けて置き換える（前の段落・セルに続く建物名も【…の続き】）
//   - 登録の照合は「書き方をそろえた形」（空白・改行を除き、全角数字・ハイフンをそろえ、〒郵便番号・都道府県名を除く）で行う。
//     同じ書き方（空白・全角半角の違いを除く）は同じ記号。書き方だけが違う同じ住所は【住所001の書き方2】
//   - 番地までの部分（建物名の前まで）も登録済みとみなす（建物名が別なら、その建物名は今までどおり保留）
//   - 番地の違う住所（郵便番号・町域だけが同じ住所）は登録済みとみなさない（今までどおり保留）
//   - 番地のない地名（「桜町の公園」など）は登録できない・置き換えない

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const addrKind = "住所" // 氏名一覧の区分

var (
	reZipBefore = regexp.MustCompile(`(?:〒[ 　]*)?` + D + `{3}` + H + D + `{4}[ 　\t]*(?:\r\n|\n|\r|｜)?[ 　\t]*$`)
	reCanonZip  = regexp.MustCompile(`^〒?[0-9]{3}-[0-9]{4}`)
	reCanonPref = regexp.MustCompile(`^` + addrPref)
)

// addrNorm: 書き方をそろえる（空白・改行・区切りを除き、全角の数字・英字を半角に、ハイフンの仲間を - に）
func addrNorm(s string) string {
	if strings.Contains(s, "_x000") {
		s = reXEscAll.ReplaceAllString(s, "")
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '０' && r <= '９':
			r = r - '０' + '0'
		case r >= 'Ａ' && r <= 'Ｚ':
			r = r - 'Ａ' + 'A'
		case r >= 'ａ' && r <= 'ｚ':
			r = r - 'ａ' + 'a'
		case strings.ContainsRune("－ー‐−―", r):
			r = '-'
		case r == '’':
			r = '\''
		case r == '｜' || r == '　' || unicode.IsSpace(r):
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// addrCanon: 登録の照合に使う形（郵便番号・都道府県名を除く）
func addrCanon(s string) string {
	n := addrNorm(s)
	if loc := reCanonZip.FindStringIndex(n); loc != nil {
		n = n[loc[1]:]
	}
	if loc := reCanonPref.FindStringIndex(n); loc != nil && loc[1] < len(n) {
		n = n[loc[1]:]
	}
	return n
}

// addrCoreLoc: 住所の番地までの部分（市区町村のある形、なければ市区町村のない形）。番地がなければ nil
func addrCoreLoc(s string) []int {
	if loc := addrFind(s); loc != nil {
		return loc
	}
	return reAddrNC.FindStringIndex(s)
}

// isAddrShape: 住所として登録できる形か（番地まで書かれている）
func isAddrShape(s string) bool { return addrCoreLoc(s) != nil }

// cleanAddr: 登録するときの書き方（改行・タブは空白1つに、続いた空白は1つに）
func cleanAddr(s string) string {
	s = reXEscAll.ReplaceAllString(s, " ")
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ", "｜", " ").Replace(s)
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == ' ' }), " ")
}

// addAddr: 登録された住所を照合の一覧に加える（番地までの部分も）
func (m *masker) addAddr(a string) {
	a = strings.TrimSpace(a)
	loc := addrCoreLoc(a)
	if loc == nil {
		return // 番地のない地名は登録しない
	}
	if m.addrSet == nil {
		m.addrSet = map[string]bool{}
	}
	if c := addrCanon(a); c != "" {
		m.addrSet[c] = true
	}
	if c := addrCanon(a[loc[0]:loc[1]]); c != "" {
		m.addrSet[c] = true
	}
}

// addrRegistered: 登録済みの住所（またはその番地まで）と同じか
func (m *masker) addrRegistered(s string) bool {
	return m.addrSet[addrCanon(s)]
}

// ---- 記号 ----

// addrCode: 住所のかたまりの記号。key は書き方をそろえた形、exact は書類の文字そのもの（元に戻すときの文字）
func (cb *codeBook) addrCode(key, exact string) string {
	bk := "住|" + key
	c, ok := cb.keyToCode[bk]
	if !ok {
		return cb.get(bk, exact, addrKind)
	}
	if cb.keyOrig[bk] == exact {
		return c
	}
	vk := "住書|" + key + "|" + exact
	if c2, ok := cb.keyToCode[vk]; ok {
		return c2
	}
	for n := 2; ; n++ {
		code := strings.TrimSuffix(c, "】") + fmt.Sprintf("の書き方%d】", n)
		if _, used := cb.codeToOrig[code]; !used {
			cb.add(vk, exact, code)
			cb.dirty = true
			return code
		}
	}
}

// addrContCode: かたまりの2つ目以降（改行・区切りのあと、前の段落・セルの続き）の記号
func (cb *codeBook) addrContCode(first, exact string) string {
	k := "住続|" + first + "|" + exact
	if c, ok := cb.keyToCode[k]; ok {
		return c
	}
	for n := 1; ; n++ {
		suf := "の続き】"
		if n > 1 {
			suf = fmt.Sprintf("の続き%d】", n)
		}
		code := strings.TrimSuffix(first, "】") + suf
		if _, used := cb.codeToOrig[code]; !used {
			cb.add(k, exact, code)
			cb.dirty = true
			return code
		}
	}
}

// ---- Word・Excel の文字の区切り（run）----
// processXML が、いま調べているグループの文字と、要素ごとの境目を置く（住所のかたまりを境目で分けるため）
var (
	curGroupText   string
	curGroupBounds []int
	curPartNo      int // processXML を呼ぶたびに1つ増える（前の段落・セルが同じ部品の中のすぐ前か、を見るため）
	curGroupSeq    int // 部品の中のグループの順番
)

// groupPos: いま調べている文字の位置（部品の番号・グループの順番）
func groupPos() [2]int { return [2]int{curPartNo, curGroupSeq} }

// adjacentGroup: prev が同じ部品のすぐ前のグループか
func adjacentGroup(prev, cur [2]int) bool {
	return prev[0] != 0 && prev[0] == cur[0] && prev[1]+1 == cur[1]
}

func groupBoundsFor(s string) map[int]bool {
	if s != curGroupText || len(curGroupBounds) == 0 {
		return nil
	}
	out := map[int]bool{}
	for _, b := range curGroupBounds {
		out[b] = true
	}
	return out
}

func isSepRune(r rune) bool { return r == '\n' || r == '\r' || r == '｜' }

// reXEsc: Excel の文字の中の改行の書き方（_x000a_・_x000d_。openpyxl などが書く）
var reXEsc = regexp.MustCompile(`^_x000[aAdD]_`)
var reXEscAll = regexp.MustCompile(`_x000[aAdD]_`)

// sepLen: s[i:] の頭が改行・区切りなら、その長さ（バイト）。そうでなければ 0
func sepLen(s string, i int) int {
	if i >= len(s) {
		return 0
	}
	r, sz := utf8.DecodeRuneInString(s[i:])
	if isSepRune(r) {
		return sz
	}
	if r == '_' && reXEsc.MatchString(s[i:]) {
		return 7
	}
	return 0
}
func isBlankRune(r rune) bool {
	return r == ' ' || r == '　' || r == '\t'
}

// addrSplit: かたまり s[st:en] を、改行・区切り（記号の外に残す）と要素の境目で分けた区間
func addrSplit(s string, st, en int) [][2]int {
	bounds := groupBoundsFor(s)
	var segs [][2]int
	ps := st
	for i := st; i < en; {
		_, sz := utf8.DecodeRuneInString(s[i:])
		if sl := sepLen(s, i); sl > 0 {
			sz = sl
			a := i
			for a > ps {
				pr, psz := utf8.DecodeLastRuneInString(s[:a])
				if !isBlankRune(pr) {
					break
				}
				a -= psz
			}
			b := i + sz
			for b < en {
				nr, nsz := utf8.DecodeRuneInString(s[b:])
				if sl := sepLen(s, b); sl > 0 {
					nsz = sl
				} else if !isBlankRune(nr) {
					break
				}
				b += nsz
			}
			if a > ps {
				segs = append(segs, [2]int{ps, a})
			}
			ps, i = b, b
			continue
		}
		if bounds[i] && i > ps {
			segs = append(segs, [2]int{ps, i})
			ps = i
		}
		i += sz
	}
	if ps < en {
		segs = append(segs, [2]int{ps, en})
	}
	out := segs[:0]
	for _, sg := range segs {
		if strings.TrimFunc(s[sg[0]:sg[1]], func(r rune) bool { return isBlankRune(r) }) != "" {
			out = append(out, sg)
		}
	}
	return out
}

type addrLump struct {
	st, en int
	pieces []match
}

// addrPieces: かたまりを記号にする（1つ目は【住所NNN】、2つ目からは【住所NNNの続き】）
func (m *masker) addrPieces(s string, st, en int, first string) []match {
	var out []match
	for _, sg := range addrSplit(s, st, en) {
		exact := s[sg[0]:sg[1]]
		var code string
		if first == "" {
			// 同じ住所（〒・都道府県の有無・空白の違い）は同じ記号の仲間に（書き方の違いは【住所001の書き方2】。元に戻すと、それぞれの書き方に戻る）
			key := addrCanon(s[st:en])
			if key == "" {
				key = addrNorm(s[st:en])
			}
			code = m.cb.addrCode(key, exact)
			first = code
		} else {
			code = m.cb.addrContCode(first, exact)
		}
		out = append(out, match{start: sg[0], end: sg[1], repl: code, kind: "住所", orig: exact})
	}
	return out
}

// placeStart: 住所の照合を始めてよい位置か（かたまりの頭、または市区町村・都道府県の字のすぐ後ろ）
func placeStart(s string, i, a int) bool {
	if i == a {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return strings.ContainsRune("市区町村郡都道府県", r)
}

// addrCores: 番地までの住所らしき部分（市区町村のある形・ない形）
func addrCores(s string) [][2]int {
	var cores [][2]int
	for _, ix := range addrFindAll(s) {
		cores = append(cores, [2]int{skipAddrHead(s, ix[0], ix[1]), ix[1]})
	}
	for _, ix := range reAddrNC.FindAllStringIndex(s, -1) {
		if !overlapsAny(cores, ix[0], ix[1]) {
			cores = append(cores, [2]int{ix[0], ix[1]})
		}
	}
	sort.Slice(cores, func(a, b int) bool { return cores[a][0] < cores[b][0] })
	return cores
}

// addrRegLumps: 登録済みの住所のかたまり
func (m *masker) addrRegLumps(s string) []addrLump {
	if len(m.addrSet) == 0 || !reHasDigit.MatchString(s) {
		return nil
	}
	var out []addrLump
	last := 0
	for _, c := range addrCores(s) {
		if c[0] < last || strings.Contains(s[c[0]:c[1]], "【") {
			continue
		}
		_, tend := m.tailAfter(s, c[1], true)
		st, en := -1, -1
		for i := c[0]; i < c[1]; {
			if placeStart(s, i, c[0]) {
				if tend > c[1] && m.addrRegistered(s[i:tend]) {
					st, en = i, tend
					break
				}
				if m.addrRegistered(s[i:c[1]]) {
					st, en = i, c[1]
					break
				}
			}
			_, sz := utf8.DecodeRuneInString(s[i:])
			i += sz
		}
		if st < 0 {
			continue
		}
		if st == c[0] {
			if loc := reZipBefore.FindStringIndex(s[:st]); loc != nil && !digitBefore(s, loc[0]) {
				st = loc[0]
			}
		}
		out = append(out, addrLump{st: st, en: en, pieces: m.addrPieces(s, st, en, "")})
		last = en
	}
	return out
}

// addrRegMatches: 登録済みの住所の置き換え（findMatchesHdr から）
func (m *masker) addrRegMatches(s string) []match {
	var out []match
	for _, l := range m.addrRegLumps(s) {
		out = append(out, l.pieces...)
	}
	return out
}

func digitBefore(s string, i int) bool {
	if i == 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return isDigitRune(r)
}

func trimRightSep(s string) string {
	for {
		t := strings.TrimRightFunc(s, func(r rune) bool { return isBlankRune(r) || isSepRune(r) })
		if len(t) >= 7 && reXEsc.MatchString(t[len(t)-7:]) {
			t = t[:len(t)-7]
		}
		if t == s {
			return t
		}
		s = t
	}
}

// trimLeftSep: 頭の空白・改行・区切りを除く
func trimLeftSep(s string) string {
	for i := 0; i < len(s); {
		if sl := sepLen(s, i); sl > 0 {
			i += sl
			continue
		}
		r, sz := utf8.DecodeRuneInString(s[i:])
		if !isBlankRune(r) {
			return s[i:]
		}
		i += sz
	}
	return ""
}

// addrContinue: 前の段落・セル・行（prev）が登録済みの住所で終わり、この文字（s）の頭に、
// その住所と合わせて登録済みになる建物名・部屋番号が続くとき、その部分を【…の続き】にする
func (m *masker) addrContinue(prev, s string, ms []match) []match {
	if len(m.addrSet) == 0 || prev == "" || s == "" {
		return ms
	}
	p := trimRightSep(prev)
	lumps := m.addrRegLumps(p)
	if len(lumps) == 0 || lumps[len(lumps)-1].en != len(p) {
		return ms
	}
	l := lumps[len(lumps)-1]
	raw, tend := m.tailAfterB(s, 0, true, true)
	if strings.TrimSpace(raw) == "" || !m.addrRegistered(p[l.st:l.en]+" "+s[:tend]) {
		return ms
	}
	ts := skipSep(s, 0)
	if len(ms) > 0 && ms[0].start < tend {
		return ms
	}
	var first string
	if len(l.pieces) > 0 {
		first = l.pieces[0].repl
	}
	if first == "" {
		return ms
	}
	pieces := m.addrPieces(s, ts, tend, first)
	return append(pieces, ms...)
}

// addrContSuspect: 前の段落・セル・行が住所（記号になった住所を含む）で終わり、この文字の頭に建物名・部屋番号らしき字が
// 残っているとき、保留にする（段落の続き・隣のセル）。登録する語は、前の住所と合わせたもの
func (m *masker) addrContSuspect(prevMasked, cur string) []suspect {
	if prevMasked == "" || cur == "" {
		return nil
	}
	p := trimRightSep(prevMasked)
	if p == "" {
		return nil
	}
	endsAddr := false
	if toks := ourTokens(p, m.cb); len(toks) > 0 && toks[len(toks)-1][1] == len(p) {
		t := toks[len(toks)-1]
		tok := p[t[0]:t[1]]
		orig := m.cb.codeToOrig[tok]
		cm := reCodeNum.FindStringSubmatch(tok)
		if reHasDigit.MatchString(orig) && reAddrOrig.MatchString(orig) && (cm == nil || !notAddrCodeKinds[cm[1]]) || strings.HasPrefix(tok, "【"+addrKind) {
			endsAddr = true
		}
	}
	if !endsAddr {
		view, _ := m.placeView(p)
		for _, c := range addrCores(view) {
			if c[1] == len(view) && !strings.Contains(view[c[0]:c[1]], "【") {
				endsAddr = true
			}
		}
	}
	if !endsAddr {
		return nil
	}
	raw, tend := m.tailAfterB(cur, 0, true, true)
	tail := trimLeftSep(raw)
	if tail == "" || m.exclude[tail] || m.exclude[normName(tail)] {
		return nil
	}
	reg := m.regFor(p, " "+tail)
	ctx := []rune(p)
	if len(ctx) > 20 {
		ctx = ctx[len(ctx)-20:]
	}
	return []suspect{{word: tail, context: string(ctx) + "／" + cur[:tend], reason: addrTailReason, reg: reg}}
}

// regFor: 住所として登録する語（記号を元の字に戻した住所の頭から、続きの建物名まで）
func (m *masker) regFor(maskedPrefix, rawTail string) string {
	rp := restoreText(maskedPrefix, m.cb)
	start := -1
	for _, c := range addrCores(rp) {
		start = c[0]
	}
	if start < 0 {
		return ""
	}
	return cleanAddr(rp[start:] + rawTail)
}

// skipAddrHead: 住所のかたまりの前にくっついた見出し（「住所秦野市…」の「住所」）を外した始まり（改善23）
func skipAddrHead(s string, st, en int) int {
	for _, h := range []string{"現住所", "住所地", "所在地", "住所"} {
		if strings.HasPrefix(s[st:en], h) {
			i := st + len(h)
			for i < en {
				r, sz := utf8.DecodeRuneInString(s[i:])
				if r != ':' && r != '：' && r != ' ' && r != '　' {
					break
				}
				i += sz
			}
			if addrFind(s[i:en]) != nil || reAddrNC.MatchString(s[i:en]) {
				return i
			}
		}
	}
	return st
}
