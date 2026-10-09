package main

// 改善C（R8.9.30）：置き換えの見逃し No.24・25・26・32・38・40
//
// どれも「見逃しを減らす」方向だけ。今までの置き換え・保留（要確認）の規則はゆるめない。
//   No.24 1文字の姓：名だけ置き換わって姓が残る形（「森　【利用者010の名】様」）。
//         名の記号のすぐ前（空白・タブ・ヘッダーの書式記号をはさんでも）にある、氏名一覧の1文字の姓も記号にする。
//         姓の記号のすぐ後ろの1文字の名（同じ方の名）も同じ。欄に姓だけ（「森」だけ）のときも記号にする
//   No.25 FAX読取：「ー」「－」区切りの番地（市区町村なし・英字入り）→ 保留／年齢「( 52歳)」「（52才）」「年齢 52歳」→ 記号
//         登録名と1文字だけ違う名前（読み取りの誤り）→ 保留／職員など登録済みの方の1文字の姓が、担当・看護師などの見出しのあとにあるとき → 記号
//   No.26 見出し語のすぐ後ろ（区切りなし）の名前 → 保留／「はな子さん」「ハナ子さん」→ 保留
//   No.32 下の名前が同じ別の人：その書類に出てくる利用者（とその家族）の記号を優先する
//   No.38 薬情：薬の名前・用法の語・1文字を、名前の候補（保留）に出さない（登録済みの名前の置き換えはそのまま）
//   No.40 紹介状：氏名一覧の読み（4列目）・書類の「フリガナ」欄・Excel のふりがなから読みを取り、読みも記号にする
//         「フリガナ」「氏名」「住所」「生年月日」の欄の中身は、読み取りが崩れていても保留／看護師・Ns のあとの名前 → 保留

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// pinfo: 氏名一覧の1人分（記号を選ぶ・1文字の姓を探すときに使う）
type pinfo struct {
	prefix, name, sur, given string
	base, scode, gcode       string
	acodes                   []string // 呼び名の記号
}

func isNameSep(r rune) bool {
	return r == ' ' || r == '　' || r == '\t' || r == ' ' || r == '｜'
}

// ---- 読み（No.40 ①）----

func kataToHira(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'ァ' && r <= 'ヶ' {
			return r - 0x60
		}
		return r
	}, s)
}

func hiraToKata(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'ぁ' && r <= 'ゖ' {
			return r + 0x60
		}
		return r
	}, s)
}

// isKanaWord: ひらがな・カタカナ（と空白・長音）だけの語
func isKanaWord(s string) bool {
	n := 0
	for _, r := range s {
		switch {
		case unicode.In(r, unicode.Hiragana, unicode.Katakana), r == 'ー':
			n++
		case r == ' ' || r == '　':
		default:
			return false
		}
	}
	return n > 0
}

func isKataRune(r rune) bool { return unicode.Is(unicode.Katakana, r) || r == 'ー' }
func isHiraRune(r rune) bool { return unicode.Is(unicode.Hiragana, r) }

// addReading: 読み（かな）を、その方の記号の「の読みN」として登録する。ひらがな・カタカナの両方の書き方を登録する。
// 部分の読み（姓だけ・名だけ）は3字以上のときだけ（短い読みはほかの語に紛れるため）
func (m *masker) addReading(pi int, reading string, part bool) {
	reading = strings.TrimSpace(reading)
	if !isKanaWord(reading) {
		return
	}
	n := utf8.RuneCountInString(normName(reading))
	if n < 2 {
		return
	}
	group := 0
	if part && n < 3 {
		group = 2 // 2字の読み（はた・はな）は、すぐ後ろに敬称・宅・家があるときだけ（「はたらく」「はたけ」を記号にしない。改善23 C1）
	}
	p := m.persons[pi]
	for _, v := range []string{reading, hiraToKata(reading), kataToHira(reading)} {
		k := normName(v)
		if m.readingSeen[k] {
			continue
		}
		if _, ok := m.lookup[k]; ok {
			continue // 呼び名などですでに登録済み
		}
		m.readingSeen[k] = true
		code := m.cb.getNumbered("読|"+p.prefix+"|"+p.name+"|"+v, v, p.base, "の読み")
		m.names = append(m.names, nameEntry{re: regexp.MustCompile(spaceFlexPattern(v)), code: code, kind: p.prefix + "（読み）", orig: v, lit: v, pid: pi + 1, kanaStart: true, group: group})
	}
}

// getNumbered: 基の記号に「の読み1」「の読み2」…を付けた記号（空いている番号）
func (cb *codeBook) getNumbered(key, orig, baseCode, suffix string) string {
	if c, ok := cb.keyToCode[key]; ok {
		return c
	}
	for i := 1; ; i++ {
		code := strings.TrimSuffix(baseCode, "】") + suffix + itoa(i) + "】"
		if _, used := cb.codeToOrig[code]; used {
			continue
		}
		cb.add(key, orig, code)
		cb.dirty = true
		return code
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// initC: newMasker の最後（rebuild の前）に呼ぶ
func (m *masker) initC() {
	m.oneSur = map[string]string{}
	m.oneGiven = map[string]string{}
	m.givenCodes = map[string]bool{}
	m.surOfCode = map[string]string{}
	m.readingSeen = map[string]bool{}
	for _, p := range m.persons {
		if p.sur == "" {
			continue
		}
		m.givenCodes[p.gcode] = true
		m.surOfCode[p.scode] = p.sur
		if utf8.RuneCountInString(p.sur) == 1 {
			// 1文字の姓の方の呼び名（下の名前だけの呼び名など）の記号の前に残った姓も同じ（「森　【利用者010の呼び名1】様」）
			for _, c := range p.acodes {
				m.givenCodes[c] = true
			}
		}
		if utf8.RuneCountInString(p.sur) == 1 {
			m.oneSur[p.sur] = p.scode
		}
		if utf8.RuneCountInString(p.given) == 1 {
			m.oneGiven[p.sur+"|"+p.given] = p.gcode
		}
	}
}

// initReadings: 氏名一覧の読み（4列目）と、かなの呼び名のひらがな・カタカナの書き方（No.40 ①）
func (m *masker) initReadings(people []person) {
	// 先に lookup を作る（呼び名との重なりを見るため）
	m.rebuild()
	for i := range m.persons {
		src := people[m.personSrc[i]]
		if r := strings.TrimSpace(src.reading); r != "" {
			m.addReading(i, r, false)
			if parts := strings.FieldsFunc(r, func(c rune) bool { return c == ' ' || c == '　' }); len(parts) >= 2 {
				m.addReading(i, parts[0], true)
				m.addReading(i, strings.Join(parts[1:], ""), true)
			}
		}
		for _, a := range src.aliases {
			a = strings.TrimSpace(a)
			if isKanaWord(a) && utf8.RuneCountInString(normName(a)) >= 2 {
				m.addReading(i, a, false)
				// 呼び名に「みたけ のぶえ」と姓・名を分けて書いた読みは、姓の読み・名の読みだけでも記号に（改善23 試験役 B1）
				if parts := strings.FieldsFunc(a, func(c rune) bool { return c == ' ' || c == '　' }); len(parts) == 2 {
					for _, pt := range parts {
						if utf8.RuneCountInString(pt) >= 2 {
							m.addReading(i, pt, true)
						}
					}
				}
			}
		}
	}
}

// ---- 書類ごとの見方（No.32・No.38・No.40）----

var reDrugTimes = regexp.MustCompile(`1日\s*[0-9０-９一二三四]\s*回|[0-9０-９]+\s*(?:mg|ｍｇ|μg|ｍｌ|mL|ml)`)

var drugIndicators = strings.Fields(`用法 用量 効能 効果 副作用 錠 カプセル 服用 食後 食前 薬剤師 処方 調剤 お薬 飲み方 頓服 内服 外用 顆粒 軟膏`)

// isDrugDoc: 薬情（お薬の説明書）らしい書類か。薬の語が4種類以上
func isDrugDoc(text string) bool {
	n := 0
	for _, w := range drugIndicators {
		if strings.Contains(text, w) {
			n++
		}
	}
	// 「1日○回」「○mg」のような用量の書き方があり、薬の語が4種類以上
	return reDrugTimes.MatchString(text) && n >= 4
}

// setDocContext: 置き換える前に、書類の文字全体（lines）を見て、その書類での記号の選び方などを決める。
// rph は Excel のふりがな（元の文字 → ふりがな）
func (m *masker) setDocContext(lines []string, rph [][2]string) {
	m.prefer = nil
	m.drugDoc = false
	text := strings.Join(lines, "\n")
	m.drugDoc = isDrugDoc(text)
	m.docHonor = docHonorWords(text)
	// 読み（No.40 ①）：Excel のふりがな
	added := false
	for _, pr := range rph {
		base := normName(pr[0])
		for i, p := range m.persons {
			if p.sur == "" || (p.prefix != "利用者" && p.prefix != "家族") {
				continue
			}
			if normName(p.name) == base {
				before := len(m.names)
				m.addReading(i, pr[1], false)
				added = added || len(m.names) != before
			}
		}
	}
	// 読み：書類の「フリガナ」欄（すぐ近くの行・欄に、登録済みの利用者・家族の氏名があるとき）
	for li, l := range lines {
		var kanas []string
		for _, ix := range reFuriLabel.FindAllStringSubmatchIndex(l, -1) {
			kanas = append(kanas, strings.TrimSpace(l[ix[2]:ix[3]]))
		}
		// 見出しだけの欄・行（「フリガナ」）の次の欄・行がかなだけ
		if reFuriOnly.MatchString(l) && li+1 < len(lines) && isKanaWord(strings.TrimSpace(lines[li+1])) {
			kanas = append(kanas, strings.TrimSpace(lines[li+1]))
		}
		for _, kana := range kanas {
			if utf8.RuneCountInString(normName(kana)) < 2 {
				continue
			}
			pi := m.nearestPerson(lines, li)
			if pi < 0 {
				continue
			}
			before := len(m.names)
			m.addReading(pi, kana, false)
			if parts := strings.FieldsFunc(kana, func(c rune) bool { return c == ' ' || c == '　' }); len(parts) >= 2 {
				m.addReading(pi, parts[0], true)
				m.addReading(pi, strings.Join(parts[1:], ""), true)
			}
			added = added || len(m.names) != before
		}
	}
	if added {
		m.rebuild()
	}
	// 下の名前が同じ別の人（No.32）：この書類に出てくる利用者と、その家族（同じ姓）の記号を優先する
	appear := map[int]bool{}
	for i, p := range m.persons {
		if p.sur == "" {
			continue
		}
		if regexp.MustCompile(spaceFlexPattern(p.name)).MatchString(text) {
			appear[i] = true
		}
	}
	related := func(i int) bool {
		if appear[i] {
			return true
		}
		p := m.persons[i]
		if p.prefix != "家族" {
			return false
		}
		for j := range appear {
			if m.persons[j].prefix == "利用者" && m.persons[j].sur == p.sur {
				return true
			}
		}
		return false
	}
	for k, cs := range m.cands {
		if len(cs) < 2 {
			continue
		}
		var pick []nameEntry
		codes := map[string]bool{}
		for _, c := range cs {
			if c.pid > 0 && related(c.pid-1) {
				if !codes[c.code] {
					codes[c.code] = true
					pick = append(pick, c)
				}
			}
		}
		if len(pick) == 1 && pick[0].code != m.lookup[k].code {
			if m.prefer == nil {
				m.prefer = map[string]nameEntry{}
			}
			pe := pick[0]
			base := m.lookup[k]
			pe.lit, pe.group = base.lit, base.group // 見つけ方（1文字＋敬称など）は今までの登録のまま
			m.prefer[k] = pe
		}
	}
	// 姓だけの書き方（改善23 試験役 D2）：この書類にフルネームで出てくる方のうち、その姓の方が1人だけなら、
	// 「前田様」も共通の【姓032】ではなく、その方の【その他072の姓】にする（AIに同じ方だと分かるように）。元に戻すと「前田」に戻る
	bySur := map[string][]int{}
	for i := range appear {
		if p := m.persons[i]; p.sur != "" {
			bySur[normName(p.sur)] = append(bySur[normName(p.sur)], i)
		}
	}
	for k, is := range bySur {
		if len(is) != 1 {
			continue
		}
		base, ok := m.lookup[k]
		if !ok || base.kind != "姓だけ" {
			continue
		}
		if _, done := m.prefer[k]; done {
			continue
		}
		p := m.persons[is[0]]
		if m.prefer == nil {
			m.prefer = map[string]nameEntry{}
		}
		pe := base
		pe.code = m.cb.getSuffix("下|"+p.prefix+"|"+p.name+"|姓", p.sur, p.base, "の姓")
		pe.kind = p.prefix + "（姓だけ）"
		pe.pid = is[0] + 1
		m.cb.nameCodes[pe.code] = true
		if m.surOfCode != nil {
			m.surOfCode[pe.code] = p.sur // 姓の記号として、すぐ後ろの別の名の確認にも使う
		}
		m.prefer[k] = pe
	}
}

// clearDocContext: 書類ごとの見方を消す（次の書類に持ち越さない）
func (m *masker) clearDocContext() {
	m.prefer = nil
	m.drugDoc = false
}

// nearestPerson: lines[li] の近く（前後3行）に氏名が書かれている利用者・家族（いちばん近い方）
func (m *masker) nearestPerson(lines []string, li int) int {
	best, bestD := -1, 99
	for d := 0; d <= 3; d++ {
		for _, j := range []int{li + d, li - d} {
			if j < 0 || j >= len(lines) {
				continue
			}
			for i, p := range m.persons {
				if p.sur == "" || (p.prefix != "利用者" && p.prefix != "家族") {
					continue
				}
				if regexp.MustCompile(spaceFlexPattern(p.name)).MatchString(lines[j]) && d < bestD {
					best, bestD = i, d
				}
			}
		}
		if best >= 0 {
			return best
		}
	}
	return best
}

// フリガナ欄：見出しのあとのかな
var reFuriOnly = regexp.MustCompile(`^[ 　\t]*(?:フリガナ|ふりがな|ﾌﾘｶﾞﾅ|カナ氏名|氏名カナ)[ 　\t]*[:：]?[ 　\t]*$`)

var reFuriLabel = regexp.MustCompile(`(?:フリガナ|ふりがな|ﾌﾘｶﾞﾅ|カナ氏名|氏名カナ)[ 　\t]*[:：]?[ 　\t]*([\p{Katakana}\p{Hiragana}ー]+(?:[ 　][\p{Katakana}\p{Hiragana}ー]+)?)`)

// ---- 1文字の姓・名（No.24・No.25）----

var (
	// 人を指す見出しのあとの1文字の姓（「担当　森」「看護師：森」）
	reOneAfterLabel = regexp.MustCompile(`(?:担当者?|記入者|記録者|作成者|報告者|対応者|看護師|Ns|NS|ＮＳ|Ｎｓ|ヘルパー|指導員|相談員|主治医|医師|ケアマネ|CM|ＣＭ|薬剤師|職員|係)[ 　\t]*[:：]?[ 　\t]*$`)
	// 1文字の姓のあとの肩書き（「森看護師」「森Ns」）
	reTitleAfter = regexp.MustCompile(`^[ 　]?(?:先生|看護師|Ns|NS|ＮＳ|Ｎｓ|ヘルパー|Dr|Ｄｒ|医師|CM|ＣＭ|薬剤師|相談員|指導員)`)
)

// oneCharNameFix: 置き換えの結果 out に、残った1文字の姓・名を足す（No.24・No.25）
func (m *masker) oneCharNameFix(s string, out []match, blocked [][]int) []match {
	if len(m.oneSur) == 0 && len(m.oneGiven) == 0 {
		return out
	}
	var add []match
	occupied := func(a, b int) bool {
		for _, x := range out {
			if a < x.end && b > x.start {
				return true
			}
		}
		for _, x := range add {
			if a < x.end && b > x.start {
				return true
			}
		}
		for _, t := range blocked {
			if a < t[1] && b > t[0] {
				return true
			}
		}
		return false
	}
	addSur := func(a, b int) {
		w := s[a:b]
		if code, ok := m.oneSur[w]; ok && !occupied(a, b) && !m.exclude[w] {
			add = append(add, match{start: a, end: b, repl: code, kind: "姓だけ", orig: w})
		}
	}
	for _, c := range out {
		if m.givenCodes[c.repl] {
			// 名の記号のすぐ前（空白などを3つまで）の1文字の姓
			i := c.start
			for n := 0; i > 0 && n < 3; n++ {
				r, sz := utf8.DecodeLastRuneInString(s[:i])
				if !isNameSep(r) {
					break
				}
				i -= sz
			}
			if i > 0 {
				_, sz := utf8.DecodeLastRuneInString(s[:i])
				addSur(i-sz, i)
			}
		}
		if sur, ok := m.surOfCode[c.repl]; ok {
			// 姓の記号のすぐ後ろの1文字の名（同じ姓の方の名）
			j := c.end
			for n := 0; j < len(s) && n < 3; n++ {
				r, sz := utf8.DecodeRuneInString(s[j:])
				if !isNameSep(r) {
					break
				}
				j += sz
			}
			if j < len(s) {
				r, sz := utf8.DecodeRuneInString(s[j:])
				nextOK := j+sz == len(s)
				if !nextOK {
					nr, _ := utf8.DecodeRuneInString(s[j+sz:])
					nextOK = !isNameHan(nr)
				}
				if code, ok := m.oneGiven[sur+"|"+string(r)]; ok && nextOK && !occupied(j, j+sz) {
					add = append(add, match{start: j, end: j + sz, repl: code, kind: "名だけ", orig: string(r)})
				}
			}
		}
	}
	// 欄・段落に姓だけ（「森」だけ）
	if t := strings.Trim(s, " 　\t ｜\r\n"); t != "" {
		if _, ok := m.oneSur[t]; ok {
			a := strings.Index(s, t)
			addSur(a, a+len(t))
		}
	}
	// 見出しのあと・肩書きの前の1文字の姓
	for w := range m.oneSur {
		for k := 0; k < len(s); {
			i := strings.Index(s[k:], w)
			if i < 0 {
				break
			}
			a := k + i
			b := a + len(w)
			k = b
			prevOK := a == 0
			if !prevOK {
				pr, _ := utf8.DecodeLastRuneInString(s[:a])
				prevOK = !isNameHan(pr)
			}
			nextOK := b == len(s)
			if !nextOK {
				nr, _ := utf8.DecodeRuneInString(s[b:])
				nextOK = !isNameHan(nr) && !unicode.In(nr, unicode.Hiragana, unicode.Katakana)
			}
			if reOneAfterLabel.MatchString(s[:a]) && nextOK {
				addSur(a, b)
				continue
			}
			if prevOK && reTitleAfter.MatchString(s[b:]) {
				addSur(a, b)
			}
		}
	}
	if len(add) == 0 {
		return out
	}
	out = append(out, add...)
	sort.SliceStable(out, func(a, b int) bool { return out[a].start < out[b].start })
	return out
}

// ---- 年齢（No.25）----

var (
	reAgeParen = regexp.MustCompile(`[（(][ 　]*(` + D + `{1,3}[ 　]*[歳才])[ 　]*[)）]`)
	reAgeLabel = regexp.MustCompile(`年齢[ 　]*[:：]?[ 　]*(` + D + `{1,3}(?:[ 　]*[歳才])?)`)
)

// ---- 住所：市区町村のない「ー」区切りの番地（No.25）----

var reAddrDash = regexp.MustCompile(`[\p{Han}ヶケ]{2,8}` + addrD + `+(?:` + addrH + `(?:` + addrD + `+|[A-Za-zＡ-Ｚａ-ｚ]{1,2})){2,4}`)

var addrDashLabels = strings.Fields(`現住所 住所地 住所 連絡先 所在地 居所 自宅 宛先 送付先 届け先 転居先 旧住所 新住所 旧 新`)

var addrDashNotHead = strings.Fields(`令和 平成 昭和 大正 明治 電話 携帯 番号 内線 品番 型番 型式 規格 第 年 月 日 号 条 項 版 回 期 表 図 番 頁`)

// dashAddrView: 番地（数字）のすぐ前の記号（地名と同じ字の姓など）を元の字に戻した文
func (m *masker) dashAddrView(s string) string {
	toks := ourTokens(s, m.cb)
	if len(toks) == 0 {
		return s
	}
	var b strings.Builder
	pos := 0
	for _, t := range toks {
		orig := m.cb.codeToOrig[s[t[0]:t[1]]]
		if t[1] < len(s) && reHanOnly.MatchString(orig) {
			if r, _ := utf8.DecodeRuneInString(s[t[1]:]); isDigitRune(r) || startsPlace(s[t[1]:]) {
				b.WriteString(s[pos:t[0]])
				b.WriteString(orig)
				pos = t[1]
			}
		}
	}
	b.WriteString(s[pos:])
	return b.String()
}

func (m *masker) dashAddrSuspects(s string, have []suspect) []suspect {
	view := m.dashAddrView(s)
	var out []suspect
	// 市区町村のある住所（今までの規則で見る）と重なるものは見ない
	var covered [][2]int
	for _, ix := range addrFindAll(view) {
		covered = append(covered, [2]int{ix[0], ix[1]})
	}
	for _, ix := range reAddrDash.FindAllStringIndex(view, -1) {
		w := view[ix[0]:ix[1]]
		if overlapsAny(covered, ix[0], ix[1]) {
			continue
		}
		if ix[0] > 0 {
			if r, _ := utf8.DecodeLastRuneInString(view[:ix[0]]); isDigitRune(r) {
				continue
			}
		}
		// 前に付いた見出し（「連絡先」「住所」など。読み取りで空白が消えたとき）は外す
		for _, lb := range addrDashLabels {
			if strings.HasPrefix(w, lb) && utf8.RuneCountInString(w) > utf8.RuneCountInString(lb)+2 {
				ix[0] += len(lb)
				w = w[len(lb):]
				break
			}
		}
		if n := utf8.RuneCountInString(strings.TrimRightFunc(w, func(r rune) bool { return !unicode.Is(unicode.Han, r) })); n < 2 {
			continue
		}
		head := strings.TrimRightFunc(w, func(r rune) bool { return !unicode.Is(unicode.Han, r) && r != 'ヶ' && r != 'ケ' })
		bad := false
		for _, x := range addrDashNotHead {
			if strings.HasSuffix(head, x) || strings.HasPrefix(head, x) && utf8.RuneCountInString(x) >= 2 {
				bad = true
			}
		}
		if bad || m.addrExcluded(w) || m.exclude[head] {
			continue
		}
		dup := false
		for _, h := range have {
			if strings.Contains(h.word, w) || strings.Contains(w, h.word) {
				dup = true
			}
		}
		if dup {
			continue
		}
		out = append(out, suspect{word: w, context: contextAround(view, ix[0], ix[1]), reason: "番地らしき文字（「ー」「－」などの区切り。市区町村名が読み取れていない住所の可能性）。" + addrReason, reg: cleanAddr(w)})
	}
	return out
}

// ---- 登録名と1文字違いの名前（No.25）----

// buildNear: 氏名一覧の氏名（3字以上）のうち1字だけ違う書き方を見つける形
func (m *masker) buildNear() {
	m.reNear = nil
	var alts []string
	seen := map[string]bool{}
	for _, p := range m.persons {
		if p.sur == "" {
			continue
		}
		rs := []rune(p.sur + p.given)
		if len(rs) < 3 {
			continue
		}
		ns := utf8.RuneCountInString(p.sur)
		for i := range rs {
			var b strings.Builder
			for k, r := range rs {
				if k == ns {
					b.WriteString(`[ 　\t\x{00a0}｜]*`)
				}
				if k == i {
					b.WriteString(`[^\s　\x{00a0}｜【】、。，．・,.（）()「」:：;；/／]`)
				} else {
					b.WriteString(regexp.QuoteMeta(string(r)))
				}
			}
			if a := b.String(); !seen[a] {
				seen[a] = true
				alts = append(alts, a)
			}
		}
	}
	if len(alts) > 0 {
		m.reNear = regexp.MustCompile(strings.Join(alts, "|"))
	}
}

// nearNameSuspects: 登録名と1字だけ違う名前（読み取りの誤り・字が化けた名前）→ 保留
func (m *masker) nearNameSuspects(s string) []suspect {
	if m.reNear == nil {
		return nil
	}
	view := restoreText(s, m.cb)
	var out []suspect
	for _, ix := range m.reNear.FindAllStringIndex(view, -1) {
		w := view[ix[0]:ix[1]]
		k := strings.Map(func(r rune) rune {
			if isNameSep(r) {
				return -1
			}
			return r
		}, w)
		if _, ok := m.lookup[k]; ok {
			continue // 登録済みの書き方そのもの
		}
		if m.exclude[w] || m.exclude[k] {
			continue
		}
		// 違う1字が漢字・化けやすい字・カタカナ（語の続きでないもの）のときだけ。
		// ひらがな（「相模より」の「よ」など助詞）は名前の化けとみなさない
		diff, ok := m.nearDiff(k)
		if !ok || !(isNameHan(diff) || isGarble(diff) || unicode.Is(unicode.Katakana, diff)) {
			continue
		}
		if strings.ContainsRune("様殿氏君宅方家邸", diff) {
			continue // 「佐木様」：名字（佐木 健さんの「佐木」）＋敬称。名の1字が化けたのではない（改善27 試験役 P1）
		}
		// 前後が漢字（長い語の一部。「綾瀬花子」の「瀬花子」・「大和一丁目」の「大和一丁」など）なら見ない。後ろの敬称はよい
		if ix[0] > 0 {
			if r, _ := utf8.DecodeLastRuneInString(view[:ix[0]]); isNameHan(r) || (unicode.Is(unicode.Katakana, diff) && isKataRune(r)) {
				continue
			}
		}
		if ix[1] < len(view) {
			r, _ := utf8.DecodeRuneInString(view[ix[1]:])
			if (isNameHan(r) && !strings.ContainsRune("様殿氏君宅方家邸", r)) || (unicode.Is(unicode.Katakana, diff) && isKataRune(r)) {
				continue
			}
		}
		out = append(out, suspect{word: w, context: contextAround(view, ix[0], ix[1]), reason: "氏名一覧の方と1字だけ違う名前（" + garbleNote + "）"})
	}
	return out
}

// nearDiff: k（空白なし）と1字だけ違う氏名一覧の氏名があれば、その違う字（k の側）
func (m *masker) nearDiff(k string) (rune, bool) {
	kr := []rune(k)
	for _, p := range m.persons {
		if p.sur == "" {
			continue
		}
		pr := []rune(p.sur + p.given)
		if len(pr) != len(kr) {
			continue
		}
		n, at := 0, -1
		for i := range pr {
			if pr[i] != kr[i] {
				n++
				at = i
			}
		}
		if n == 1 {
			return kr[at], true
		}
	}
	return 0, false
}

// inTok: s[a:b] が記号と重なるか
func (m *masker) inTok(s string, a, b int) bool {
	for _, t := range ourTokens(s, m.cb) {
		if a < t[1] && b > t[0] {
			return true
		}
	}
	return false
}

// ---- 見出し語のすぐ後ろの名前（No.26・No.40 ③）----

var reLabelNoSep = regexp.MustCompile(`(?:指導員名|主な担当者|担当看護師|担当者名|担当者|担当|記入者|記録者|作成者|報告者|氏名|看護師|指導員|相談員|主治医)`)

var labelNoSepNotName = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`会議 地域 区域 範囲 業務 部署 部門 期間 変更 交代 一覧 欄 表 名 者 様 様式 医 医師 医療 事業所 事業 窓口 課 係 科 部 室 制 数 外 内 分 別 時 日 月 年 看護師 看護 師 介護 支援 専門員 以外 不在 未定 決定 予定 確認 記入 署名 押印 印 連絡 相談 氏名 住所 性別 年齢 続柄 生年月日 電話 自宅 本人 家族 利用者 患者 職員 未 済 有 無 及び 等
		営業 事務 管理 管理者 窓口 受付 責任者 責任 代表 主任 補助 看護 薬剤 栄養 機能 訓練 送迎 入浴 調理 清掃 営業所 本部 支店 店舗 施設 病棟 外来 病院 医院 薬局 業者 企業 会社 事業者 部長 課長 係長 室長 所長 院長 理事 役員 社員 者名 名欄 替え 替 交替 引継 引継ぎ 申し送り 不詳 無記入 空欄`) {
		labelNoSepNotName[w] = true
	}
}

// 見出し語のすぐ後ろで、この字で始まる語は名前とみなさない（「担当時間」「担当人数」など）
const labelNoSepHead = "時人件区内期全各当本他主副新旧前後上下中"

func n2(s string) int { return utf8.RuneCountInString(s) }

func (m *masker) labelNoSepSuspects(s string) []suspect {
	var out []suspect
	for _, ix := range reLabelNoSep.FindAllStringIndex(s, -1) {
		if m.inTok(s, ix[0], ix[1]) {
			continue
		}
		a := ix[1]
		if a >= len(s) {
			continue
		}
		r, _ := utf8.DecodeRuneInString(s[a:])
		if !isNameHan(r) {
			continue // 区切りのあるものは今までの規則（E2）で見る
		}
		st, en := nameRunAt(s, a)
		w := strings.TrimSpace(s[st:en])
		nw := normName(w)
		if n := utf8.RuneCountInString(nw); n < 2 || n > 5 {
			continue
		}
		if labelNoSepNotName[nw] {
			continue
		}
		skip := false
		for k := range labelNoSepNotName {
			if utf8.RuneCountInString(k) >= 2 && (strings.HasPrefix(nw, k) || strings.HasSuffix(nw, k)) {
				skip = true
			}
		}
		// 長い語の一部（「担当地域の」など）：名前のあとにひらがなが続くときは、敬称以外は名前とみなさない
		if en < len(s) {
			q, _ := utf8.DecodeRuneInString(s[en:])
			if unicode.Is(unicode.Hiragana, q) && !strings.HasPrefix(s[en:], "さん") && !strings.HasPrefix(s[en:], "様") && !strings.HasPrefix(s[en:], "さま") {
				skip = true
			}
		}
		// 名前のあとが行の終わり・区切り・敬称のときは、終わりの字（間・部 など）で外さない（「主な担当者座間」）
		cleanEnd := en >= len(s)
		if !cleanEnd {
			q, _ := utf8.DecodeRuneInString(s[en:])
			cleanEnd = isNameSep(q) || strings.ContainsRune("、。，．,.（(【「：:／/", q) || strings.HasPrefix(s[en:], "さん") || strings.HasPrefix(s[en:], "様")
		}
		if !skip && !m.ocrNameOK(w, s, st, en) {
			// 終わりの字で外れる語は、行の終わり・区切りの前で、1字目が一般語の字でないときだけ候補にする
			fr, _ := utf8.DecodeRuneInString(nw)
			if !cleanEnd || n2(nw) > 4 || labelNoSepNotName[string(fr)] || strings.ContainsRune(labelNoSepHead, fr) {
				skip = true
			}
		}
		if skip || !m.ocrNameOKEnd(w, s, st, en, true) {
			continue
		}
		out = append(out, suspect{word: w, context: contextAround(s, st, en), reason: "氏名欄（担当者・氏名などの見出し）のすぐ後ろに、氏名一覧に登録されていない名前らしき文字があります"})
	}
	return out
}

var (
	reNsBefore = regexp.MustCompile(`(?:Ns|NS|ＮＳ|Ｎｓ)[ 　]*[:：]?[ 　]*`)
	reNsAfter  = regexp.MustCompile(`([\p{Han}々ヶ]{2,4})[ 　]?(?:Ns|NS|ＮＳ|Ｎｓ|看護師)(?:[^A-Za-zＡ-Ｚａ-ｚ]|$)`)
)

// nurseSuspects: 看護師・Ns の前後の名前（No.40 ③）
func (m *masker) nurseSuspects(s string) []suspect {
	var out []suspect
	for _, ix := range reNsBefore.FindAllStringIndex(s, -1) {
		if m.inTok(s, ix[0], ix[1]) {
			continue
		}
		st, en := nameRunAt(s, ix[1])
		if en <= st {
			continue
		}
		w := strings.TrimSpace(s[st:en])
		if !m.ocrNameOK(w, s, st, en) || careTaskWord(w) {
			continue
		}
		out = append(out, suspect{word: w, context: contextAround(s, st, en), reason: "氏名欄（看護師・Nsの見出し）の後ろに、氏名一覧に登録されていない名前らしき文字があります"})
	}
	for _, ix := range reNsAfter.FindAllStringSubmatchIndex(s, -1) {
		w := s[ix[2]:ix[3]]
		if ix[2] > 0 {
			if r, _ := utf8.DecodeLastRuneInString(s[:ix[2]]); isNameHan(r) {
				continue
			}
		}
		if endsWithPersonNoun(w, s[:ix[2]]) || strings.HasPrefix(w, "担当") || strings.HasPrefix(w, "訪問") || jobHeadWords[normName(w)] || !m.ocrNameOK(w, s, ix[2], ix[3]) {
			continue
		}
		out = append(out, suspect{word: w, context: contextAround(s, ix[2], ix[3]), reason: "氏名欄（看護師・Nsの前）に、氏名一覧に登録されていない名前らしき文字があります"})
	}
	return out
}

// ---- 「はな子さん」「ハナ子さん」（No.26）----

var reKanaKo = regexp.MustCompile(`([\p{Hiragana}\p{Katakana}ー]{1,4}子)[ 　]?(さん|様|さま|ちゃん)`)

func (m *masker) kanaKoSuspects(s string) []suspect {
	var out []suspect
	for _, ix := range reKanaKo.FindAllStringSubmatchIndex(s, -1) {
		w := s[ix[2]:ix[3]]
		kana := strings.TrimSuffix(w, "子")
		// 語の途中（前にかなが続く）なら、前の字も含める（最大4字）
		if ix[2] > 0 {
			if r, _ := utf8.DecodeLastRuneInString(s[:ix[2]]); unicode.In(r, unicode.Hiragana, unicode.Katakana) {
				continue
			}
		}
		if utf8.RuneCountInString(kana) < 2 || strings.HasPrefix(kana, "お") || strings.HasPrefix(kana, "ご") {
			continue
		}
		if m.exclude[w] || m.exclude[w+s[ix[4]:ix[5]]] {
			continue
		}
		out = append(out, suspect{word: s[ix[2]:ix[1]], context: contextAround(s, ix[2], ix[1]), reason: "敬称の付いた名前らしき語（ひらがな・カタカナ＋子。ご家族の下の名前の可能性）"})
	}
	return out
}

// ---- 欄の中身（No.40 ②）----

const fieldLabelPat = `(フリガナ|ふりがな|ﾌﾘｶﾞﾅ|(?:患者|利用者|被保険者|本人|契約者)?氏[ 　]?名|現?住[ 　]?所|生年月日)`

var (
	reFieldLabel     = regexp.MustCompile(fieldLabelPat + `[ 　\t]*[:：]?[ 　\t]*`)
	reFieldLabelOnly = regexp.MustCompile(`^[ 　\t]*` + fieldLabelPat + `[ 　\t]*[:：]?[ 　\t]*$`)
)

// 欄の中身の区切りとみなす語（次の欄の見出し）
var fieldStop = strings.Fields(`フリガナ ふりがな 氏名 住所 生年月日 性別 年齢 電話 TEL ＴＥＬ FAX ＦＡＸ 続柄 要介護 要支援 保険 番号 認定 主治医 病名 診断 既往 紹介 目的 職業 連絡先 緊急 備考 様 殿 さん 記入 欄 〒`)

// 欄の中身に入れずに飛ばす語（隣の欄の見出し。FAXの読み取りで住所のすぐ後ろに来る。改善30 No.68）
var reBrLabel = regexp.MustCompile(`【[ 　]*(?:` + fieldLabelPat + `|区分)[ 　]*[:：]?[ 　]*】`)

var fieldSkipWord = map[string]bool{"区分": true}

// 1字だけのときに区切りとみなす字（性別・元号の選択肢など）
const fieldStopOne = "男女明大昭平令歳才印様殿"

func fieldStopWord(c string) bool {
	if utf8.RuneCountInString(c) == 1 && strings.Contains(fieldStopOne, c) {
		return true
	}
	for _, w := range fieldStop {
		if strings.HasPrefix(c, w) {
			return true
		}
	}
	return false
}

// fieldContent: 見出しのあとの中身（次の見出し・区切りの語の前まで、空白で区切った2つまで）。記号・敬称・空白は除く
func (m *masker) fieldContent(rest string) string {
	var got []string
	for _, c := range strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == '　' || r == '\t' || r == '｜' || r == '\n' || r == '\r' }) {
		if loc := reFieldLabel.FindStringIndex(c); loc != nil && loc[0] == 0 {
			break
		}
		cc := reTokenAny.ReplaceAllStringFunc(c, func(t string) string {
			if m.cb.isOurToken(t) {
				return ""
			}
			return t
		})
		// 記号でない【】（FAXの読み取りで崩れてできたもの。例「【北菜奈】」）は、かっこを外して字として見る（改善30 No.68）。
		// かっこのままだと、止まった語を「除外」にも「住所」にも登録できず、取り消すしかなかった
		cc = reBrLabel.ReplaceAllString(cc, "") // 【氏名】【住所】【区分】のような【】で囲んだ見出しは消して、後ろの中身を見る
		cc = strings.NewReplacer("【", "", "】", "").Replace(cc)
		cc = strings.Trim(cc, "()（）[]［］「」、。,.：:・")
		if cc == "" {
			continue
		}
		if fieldSkipWord[cc] {
			continue // 隣の欄の見出し（「区分」など）は中身に入れない（改善30 No.68）
		}
		if fieldStopWord(cc) {
			break
		}
		got = append(got, cc)
		if len(got) >= 2 {
			break
		}
	}
	return strings.Join(got, " ")
}

func (m *masker) fieldSuspect(label, content, ctx string) []suspect {
	c := strings.TrimSpace(content)
	nc := normName(c)
	if nc == "" || isCommonValue(nc) || m.exclude[c] || m.exclude[nc] || eraWords[nc] || labelNotName[nc] || stopWords[nc] {
		return nil
	}
	if utf8.RuneCountInString(nc) < 2 && !isKanaWord(nc) {
		return nil
	}
	if reOnlyDigitsSym.MatchString(nc) && !strings.Contains(label, "生年月日") {
		return nil
	}
	if m.isFormWord(c) {
		return nil // 様式に印刷されている見出しの文字（改善16）
	}
	lb := normName(label)
	switch {
	case strings.Contains(lb, "住所"):
		if m.addrExcluded(c) { // 住所らしい形の条件は外した（点検役 R8.10.1：町名だけの住所が素通りするため。見出しは formwords で除く）
			return nil // 住所らしい形（数字・丁目・番地・号・都道府県・市区町村・郵便番号）がない普通の言葉は止めない（改善16）
		}
		return []suspect{{word: c, context: ctx, reason: "番地などが書かれる住所の欄の中身（読み取りが崩れていても住所の可能性）。" + addrReason, reg: cleanAddr(c)}}
	case strings.Contains(lb, "生年月日"):
		if b := normName(strings.TrimSpace(reBirthTail.ReplaceAllString(c, ""))); b == "" || m.exclude[b] {
			return nil // 「生年月日ではない（通す）」で除外にした日付・記号になった日付（改善26）
		}
		return []suspect{{word: c, context: ctx, reason: "生年月日の欄の中身が、記号に置き換えられずに残っています（読み取りが崩れた日付の可能性）。生年月日なら、だれの生年月日かのボタンを押してください"}}
	case strings.Contains(lb, "名"):
		return []suspect{{word: c, context: ctx, reason: "氏名欄の中身が、記号に置き換えられずに残っています（読み取りが崩れた名前の可能性）"}}
	default: // フリガナ
		return []suspect{{word: c, context: ctx, reason: "氏名欄（フリガナ）の中身が、記号に置き換えられずに残っています（名前の読みの可能性）。氏名一覧の方の読みなら、その方の「呼び名」に書き足してください"}}
	}
}

// fieldSuspects: 同じ文の中の「見出し＋中身」
func (m *masker) fieldSuspects(s string) []suspect {
	var out []suspect
	for _, ix := range reFieldLabel.FindAllStringSubmatchIndex(s, -1) {
		if m.inTok(s, ix[0], ix[1]) {
			continue // 記号（【住所001】など）の中の字
		}
		label := s[ix[2]:ix[3]]
		after := ix[1]
		// 見出しが長い語の一部（「氏名欄」「住所地」「住所ー」など）のときは見ない。
		// 区切り（空白・：）がないときは、中身が漢字・数字で始まるときだけ
		if ix[3] < len(s) {
			r, _ := utf8.DecodeRuneInString(s[ix[3]:])
			if r == '欄' || r == '地' || r == '等' || r == '変' {
				continue
			}
			if ix[1] == ix[3] && (r == ')' || r == '）') && ix[2] > 0 && strings.ContainsRune("(（", lastRune(s[:ix[2]])) {
				// 「(フリガナ)サキサチコ」：かっこで囲んだ見出しのすぐあとの中身（FAXの読み取り。改善27）
				after = ix[3] + utf8.RuneLen(r)
				r, _ = utf8.DecodeRuneInString(s[after:])
				if !isNameHan(r) && !isDigitRune(r) && !unicode.Is(unicode.Katakana, r) && !unicode.Is(unicode.Hiragana, r) {
					continue
				}
			} else if ix[1] == ix[3] && !isNameHan(r) && !isDigitRune(r) {
				continue
			}
		}
		content := m.fieldContent(s[after:])
		if strings.Contains(label, "生年月日") {
			if d := birthFull(s[after:]); d != "" {
				content = d // 「39 /10/ 17」のように空白で分かれた日付も、日まで1つの日付として見る（改善27）
			}
		}
		if content == "" {
			continue
		}
		out = append(out, m.fieldSuspect(label, content, contextAround(s, ix[2], ix[1]))...)
	}
	return out
}

// fieldCellSuspect: 見出しだけの欄（前）の次の欄（今）の中身（Excel・Wordの表、読み取りの次の行）
func (m *masker) fieldCellSuspect(prev, cur string) []suspect {
	lm := reFieldLabelOnly.FindStringSubmatch(prev)
	if lm == nil {
		return nil
	}
	if m.isFormWord(cur) {
		return nil // 隣の欄が様式に印刷されている文字そのもの（1表の早見表など。改善27の2）
	}
	content := m.fieldContent(cur)
	if strings.Contains(lm[1], "生年月日") {
		if d := birthFull(cur); d != "" {
			content = d
		}
	}
	if content == "" {
		return nil
	}
	return m.fieldSuspect(lm[1], content, strings.TrimSpace(prev)+"［"+strings.TrimSpace(cur)+"］")
}

// ---- 薬情（No.38）----

var drugTerms = strings.Fields(`錠 カプセル 顆粒 細粒 シロップ 軟膏 クリーム 貼付 テープ パップ 点眼 点鼻 吸入 坐剤 座薬 注射 内服 外用 頓服 用法 用量 効能 効果 副作用 服用 服薬 食前 食後 食間 毎食 就寝前 起床時 エキス 散剤 薬剤 薬局 処方 調剤 日分 包 mg ｍｇ μg mL ml`)

const drugChars = "朝昼夕晩毎食前後間就寝時錠包回日分粒散液剤用法量"

// isDrugWord: 薬の名前・用法の語（1字も含む）か
func isDrugWord(w string) bool {
	core := normName(reHonorTail.ReplaceAllString(reTokenAny.ReplaceAllString(w, ""), ""))
	if core == "" {
		return true
	}
	n := utf8.RuneCountInString(core)
	if n <= 1 {
		return true
	}
	for _, t := range drugTerms {
		if strings.Contains(core, t) {
			return true
		}
	}
	all := true
	kata := 0
	for _, r := range core {
		if !strings.ContainsRune(drugChars, r) {
			all = false
		}
		if isKataRune(r) {
			kata++
		}
	}
	if all {
		return true
	}
	// カタカナだけの語（薬の名前）。敬称の付いたもの（「ハナコ様」など）は名前かもしれないので外さない
	if kata == n && n >= 3 && !reHonorTail.MatchString(strings.TrimSpace(w)) {
		return true
	}
	return false
}

// drugFilter: 薬情のとき、薬の名前・用法の語・1字の候補を名前の保留から外す（住所・番号などの保留はそのまま）
func (m *masker) drugFilter(sus []suspect) []suspect {
	if !m.drugDoc {
		return sus
	}
	out := sus[:0]
	for _, x := range sus {
		if strings.HasPrefix(x.reason, "番地") || strings.HasPrefix(x.reason, "生年月日") {
			out = append(out, x)
			continue
		}
		if isDrugWord(x.word) {
			continue
		}
		out = append(out, x)
	}
	return out
}

// ---- まとめ ----

// cSuspects: 改善Cの保留を have に加える（重なるものは足さない）
func (m *masker) cSuspects(s string, have []suspect) []suspect {
	groups := [][]suspect{m.labelNoSepSuspects(s), m.nurseSuspects(s), m.kanaKoSuspects(s), m.nearNameSuspects(s), m.fieldSuspects(s)}
	groups = append(groups, m.dashAddrSuspects(s, have))
	groups = append(groups, m.no7Suspects(s)...) // 改善13 No.7
	for _, g := range groups {
		have = m.mergeSus(have, g)
	}
	return m.drugFilter(have)
}

func (m *masker) mergeSus(have, add []suspect) []suspect {
	key := func(w string) string { return normName(reHonorTail.ReplaceAllString(w, "")) }
	for _, x := range add {
		x.word = m.unmask(x.word)
		k := key(x.word)
		if k == "" {
			continue
		}
		dup := false
		for i, h := range have {
			hk := key(h.word)
			if hk == "" {
				continue
			}
			if strings.Contains(hk, k) {
				dup = true
				break
			}
			if strings.Contains(k, hk) {
				// 新しい方が長い（「新田3-9-51」と欄の中身「い新田3-9-51」）：長い方に置き換える
				have[i] = x
				dup = true
				break
			}
		}
		if !dup {
			have = append(have, x)
		}
	}
	return have
}

// reBirthFull: 生年月日の欄の頭の日付（年・月・日まで。区切りの前後の空白も含む。元号はあってもなくても）
var reBirthFull = regexp.MustCompile(`^[ 　\t]*((?:明治|大正|昭和|平成|令和|[明大昭平令MTSHR])?[ 　]*[0-9０-９]{1,4}[ 　]*(?:年|[/／.．-])[ 　]*[0-9０-９]{1,2}[ 　]*(?:月|[/／.．-])[ 　]*[0-9０-９]{1,2}(?:[ 　]*日)?)`)

func birthFull(rest string) string {
	if m := reBirthFull.FindStringSubmatch(rest); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}
