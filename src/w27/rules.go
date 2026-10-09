package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// person entry from 氏名一覧
type person struct {
	kind    string // 区分
	name    string // 氏名 as written
	aliases []string
	reading string // 読み（氏名一覧の4列目。改善C No.40）
}

const D = `[0-9０-９]`
const H = `[-‐－−―ー]`

var (
	reTokenAny = regexp.MustCompile(`【[^【】]{1,40}】`)
	rePhoneHy  = regexp.MustCompile(`0` + D + `{1,4}` + `[-‐－−―ー(（]` + D + `{1,4}` + `[-‐－−―ー)）]` + D + `{3,4}`)
	reZip      = regexp.MustCompile(`〒\s*` + D + `{3}` + H + D + `{4}|` + D + `{3}` + H + D + `{4}`)
	rePhoneSp  = regexp.MustCompile(`0` + D + `{1,4}[ 　]` + D + `{2,4}[ 　]` + D + `{4}`)
	reLongNum  = regexp.MustCompile(D + `{8,}`)
	reOldDate  = regexp.MustCompile(`(?:明治|大正|昭和|[MTS]\.?)\s*` + D + `{1,2}\s*[年./／]\s*` + D + `{1,2}\s*[月./／]\s*` + D + `{1,2}\s*日?|(?:18` + D + `{2}|19[0-5]` + D + `|196[0-5])\s*[年/／.]\s*` + D + `{1,2}\s*[月/／.]\s*` + D + `{1,2}\s*日?`)
	reMail     = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	dateCore   = `(?:明治|大正|昭和|平成|令和|[MTSHR]\.?)?\s*` + D + `{1,4}\s*[年./／]\s*` + D + `{1,2}\s*[月./／]\s*` + D + `{1,2}\s*日?`
	reBirthPre = regexp.MustCompile(`生年月日\s*[：:]?\s*(` + dateCore + `)`)
	reBirthSuf = regexp.MustCompile(`(` + dateCore + `)\s*生`)
	reSuspect  = regexp.MustCompile(`([\p{Han}々〆ヶ]{1,4}|[\p{Katakana}ー]{2,8})[ 　]?(様|さん|氏|ちゃん|くん|先生|医師)|([\p{Hiragana}]{2,4})(ちゃん|くん|様)`)
)

var stopWords = map[string]bool{}

var compoundSkip = map[string]bool{"様式": true, "様子": true, "様相": true, "様態": true, "様々": true, "氏名": true, "様変": true}

// 敬称の字（様）を含む一般語（R8.9.27 改善4）。敬称の前後とあわせてこの語になっているときは、名前扱いしない
// （「関わっていない模様」「前回同様」「仕様」「王様」「多様」など）
// 前の漢字1字が実在の名字になりうる語（神様・若様・殿様 など）は入れない（差し戻し1回目）
var honorCommonWords = strings.Fields(`模様 同様 仕様 王様 多様 一様 異様 左様 然様 有様 態様 様子 様相 様態 様式 様々`)

// wordCoversHonor: s の中の敬称（s[hs:he]）が、語 w の一部として書かれているか。
// w は敬称の字だけでなく、前か後ろに1字以上をもつ語に限る（「さん」「様」そのものの除外では効かせない）
func wordCoversHonor(s string, hs, he int, w string) bool {
	hon := s[hs:he]
	if len(w) <= len(hon) {
		return false
	}
	for k := 0; k+len(hon) <= len(w); {
		i := strings.Index(w[k:], hon)
		if i < 0 {
			break
		}
		st := hs - (k + i)
		if st >= 0 && st+len(w) <= len(s) && s[st:st+len(w)] == w {
			return true
		}
		k += i + 1
	}
	return false
}

// honorInCommonWord: 敬称の字が一般語（模様・同様 など）や、除外に登録した語（「模様」など敬称の字を含む語）の一部か
func (m *masker) honorInCommonWord(s string, hs, he int) bool {
	for _, w := range honorCommonWords {
		if wordCoversHonor(s, hs, he, w) {
			return true
		}
	}
	for ex := range m.exclude {
		if utf8.RuneCountInString(ex) >= 2 && wordCoversHonor(s, hs, he, ex) {
			return true
		}
	}
	return false
}

func init() {
	for _, w := range strings.Fields(`本人 ご本人 利用者 家族 ご家族 担当者 担当 奥 奥さ 息子 娘 妻 夫 主人 ご主人 長男 長女 次男 次女 三男 三女 お父 お母 父 母 兄 姉 弟 妹 お兄 お姉 皆 皆さ 各 相談員 看護師 医師 主治医 先生 お客 孫 嫁 婿 お嫁 事業所 施設 病院 ケアマネ 職員 ヘルパー 訪問 管理者 相手 他 お子 子 ご子息 ご息女 お婆 お爺 おばあ おじい ばあ じい 叔父 叔母 伯父 伯母 親戚 隣人 近所 民生委員 包括 市 区 町 村 役所 お隣 奥様 皆様 各位 御中 殿方 同 上記 前記 当 貴 お嬢 ご両親 両親 ご夫婦 夫婦 お二人 ご兄弟 兄弟 姉妹 お孫 甥 姪 いとこ 友人 知人 大家 家主 お坊 住職 神父 牧師 係 様式 利用 お客様 お世話 みな みなさ おばあ おじい おかあ おとう おにい おねえ おくさ だんな ヘルパ ドクター ケアマネジャー 担当の 主治医の 主治 在宅 往診 専門 歯科 眼科 内科 外科 整形 皮膚科 精神科 泌尿器科 耳鼻科 婦人科 研修 指導 嘱託 訪問看護 薬`) {
		stopWords[w] = true
	}
}

type codeBook struct {
	// key -> code, code -> original
	keyToCode  map[string]string
	codeToOrig map[string]string
	keyOrig    map[string]string
	order      []string
	counters   map[string]int
	dirty      bool
	numeric    map[string]bool
	nameCodes  map[string]bool
}

func newCodeBook() *codeBook {
	return &codeBook{keyToCode: map[string]string{}, codeToOrig: map[string]string{}, keyOrig: map[string]string{}, counters: map[string]int{}, numeric: map[string]bool{}, nameCodes: map[string]bool{}}
}

var reCodeNum = regexp.MustCompile(`^【([^0-9０-９【】]+?)(\d{3,})(?:の呼び名\d+|の名|の姓|の読み\d+)?】$`)

func (cb *codeBook) add(key, orig, code string) {
	cb.keyToCode[key] = code
	if strings.HasPrefix(key, "数|") {
		cb.numeric[code] = true
	}
	for _, pre := range []string{"名|", "姓|", "下|", "別|", "読|"} {
		if strings.HasPrefix(key, pre) {
			cb.nameCodes[code] = true
		}
	}
	if strings.HasPrefix(key, "名|") || (strings.HasPrefix(key, "文|") && strings.Contains(strings.SplitN(key, "|", 3)[1], "名")) {
		cb.codeToOrig[code] = strings.NewReplacer(" ", "", "　", "").Replace(orig)
	} else {
		cb.codeToOrig[code] = orig
	}
	cb.keyOrig[key] = orig
	cb.order = append(cb.order, key)
	if m := reCodeNum.FindStringSubmatch(code); m != nil {
		var n int
		fmt.Sscanf(m[2], "%d", &n)
		if n > cb.counters[m[1]] {
			cb.counters[m[1]] = n
		}
	}
}

func (cb *codeBook) get(key, orig, prefix string) string {
	if c, ok := cb.keyToCode[key]; ok {
		return c
	}
	cb.counters[prefix]++
	code := fmt.Sprintf("【%s%03d】", prefix, cb.counters[prefix])
	cb.add(key, orig, code)
	cb.dirty = true
	return code
}

func (cb *codeBook) getAlias(key, orig, baseCode string, idx int) string {
	if c, ok := cb.keyToCode[key]; ok {
		return c
	}
	code := strings.TrimSuffix(baseCode, "】") + fmt.Sprintf("の呼び名%d】", idx)
	// 呼び名を外したあと（住所の登録へ移したときなど）に、前の呼び名の記号と重ならないよう、空いている番号を使う（改善6）
	for _, used := cb.codeToOrig[code]; used; _, used = cb.codeToOrig[code] {
		idx++
		code = strings.TrimSuffix(baseCode, "】") + fmt.Sprintf("の呼び名%d】", idx)
	}
	cb.add(key, orig, code)
	cb.dirty = true
	return code
}

func (cb *codeBook) getSuffix(key, orig, baseCode, suffix string) string {
	if c, ok := cb.keyToCode[key]; ok {
		return c
	}
	code := strings.TrimSuffix(baseCode, "】") + suffix + "】"
	cb.add(key, orig, code)
	cb.dirty = true
	return code
}

func sanitizePrefix(s string) string {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer("【", "", "】", "", " ", "", "　", "", ":", "", "\t", "").Replace(s)
	if s == "" {
		s = "人物"
	}
	return s
}

const honor = `(?:様|さま|さん|氏|殿|ちゃん|くん|君)`

// partEntry: 2文字以上ならそのまま、1文字なら敬称が付いたときだけ置き換える
func partEntry(word, code, kind string) nameEntry {
	q := regexp.QuoteMeta(word)
	if utf8.RuneCountInString(word) >= 2 {
		return nameEntry{re: regexp.MustCompile(q), code: code, kind: kind, orig: word, lit: word}
	}
	return nameEntry{re: regexp.MustCompile(`(` + q + `)[ 　]?` + honor), code: code, kind: kind, orig: word, lit: word, group: 1}
}

// nameMatcher holds compiled name patterns.
type nameEntry struct {
	re    *regexp.Regexp
	code  string
	kind  string
	orig  string
	lit   string
	group int // submatch group to replace (0 = whole match)
	pid   int // 氏名一覧の何人目か（1から。0は人に結び付かない語。改善C No.32）
	// kanaStart: かなの読み。すぐ前が同じ種類のかな（語の途中）なら置き換えない（改善C No.40）
	kanaStart bool
}

type masker struct {
	reMulti *regexp.Regexp // all names/aliases (2+ chars) in one pattern, longest first
	reOne   *regexp.Regexp // 1-char parts followed by an honorific
	reTwo   *regexp.Regexp // 2字のかなの読み（姓・名）＋敬称・宅・家（改善23 試験役 C1）
	lookup  map[string]nameEntry
	fuse    map[string]nameEntry // 伏せ字の呼び名（伏せ字の字を〇にそろえた鍵。改善22）
	names   []nameEntry
	exclude map[string]bool
	// 1文字の除外（R8.9.29）：名前のすぐ前後に残った字の確認にだけ使う。名前の見つけ方には使わない
	// （「子」などを除外にすると名前を見逃すため）
	excludeOne map[string]bool
	surnames   map[string]bool
	cb         *codeBook
	reLit      *regexp.Regexp
	litN       int
	addrSet    map[string]bool // 登録済みの住所（書き方をそろえた形。改善6）
	// 登録した生年月日の書き方の違い（読み取りの「Ｓ21.5.3」など。改善12 No.36。birth_alias.go）
	dateAliases []dateAlias

	// 改善C（rules_c.go）
	persons     []pinfo
	personSrc   []int                  // persons[i] が people の何番目か
	cands       map[string][]nameEntry // 同じ書き方の語の、すべての登録（下の名前が同じ別の人など。No.32）
	prefer      map[string]nameEntry   // この書類で優先する記号（No.32）
	drugDoc     bool                   // 薬情らしい書類（No.38）
	docHonor    map[string]bool        // この書類で「さん・様・氏・先生」が付いて出てくる漢字の語（職種のあとの名前の見分け。R8.10.1）
	oneSur      map[string]string      // 1文字の姓 → 姓の記号（No.24）
	oneGiven    map[string]string      // 姓|1文字の名 → 名の記号
	givenCodes  map[string]bool        // 名の記号
	surOfCode   map[string]string      // 姓の記号 → 姓
	readingSeen map[string]bool
	reNear      *regexp.Regexp // 登録名と1字違い（No.25）
}

// nameFlexPattern: スペースなしで登録した3字以上の漢字の名前（「田辺美沙子」）は、書類で字の間に空白1つが入っても見つける
// （「田辺 美沙子」。姓と名の区切りが分からないため、どの字の間でもよい。置き換えを増やすだけ。点検役 R8.10.1）
func nameFlexPattern(name string) string {
	rs := []rune(name)
	if len(rs) < 3 || strings.ContainsAny(name, " 　") {
		return spaceFlexPattern(name)
	}
	for _, r := range rs {
		if !unicode.Is(unicode.Han, r) && r != '々' && r != 'ヶ' {
			return spaceFlexPattern(name)
		}
	}
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = regexp.QuoteMeta(string(r))
	}
	return strings.Join(parts, `[ 　]?`)
}

func spaceFlexPattern(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == ' ' || r == '　' })
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return strings.Join(parts, `[ 　]*`)
}

func newMasker(people []person, exclude []string, cb *codeBook) *masker {
	m := &masker{exclude: map[string]bool{}, excludeOne: map[string]bool{}, surnames: map[string]bool{}, cb: cb}
	for _, e := range exclude {
		e = strings.TrimSpace(e)
		if utf8.RuneCountInString(e) == 1 {
			m.excludeOne[e] = true
			continue
		}
		m.exclude[e] = true
		if n := normName(e); n != e && utf8.RuneCountInString(n) >= 2 {
			m.exclude[n] = true // 「岡本 澄子」と空白入りで除外しても、空白なしの形でも引けるように（改善23 点検役）
		}
	}
	for srcIdx, p := range people {
		if strings.TrimSpace(p.kind) == addrKind {
			m.addAddr(p.name) // 住所は人に結び付けない（改善6）
			continue
		}
		prefix := rowPrefix(p.kind, p.name)
		base := m.cb.get("名|"+prefix+"|"+p.name, p.name, prefix)
		ek := prefix // 置き換えの種類（記録・③の最後の確かめで使う）
		if prefix != sanitizePrefix(p.kind) {
			ek = sanitizePrefix(p.kind) + "（生年月日）" // 「その他」の生年月日の行：③の最後の確かめで「その他」として数える
		}
		m.addDateAlias(p.name, base, ek) // 改善12 No.36：生年月日の形の登録語
		pat := spaceFlexPattern(p.name)
		pi := pinfo{prefix: prefix, name: p.name, base: base}
		pid := len(m.persons) + 1
		m.names = append(m.names, nameEntry{re: regexp.MustCompile(pat), code: base, kind: ek, orig: p.name, lit: p.name, pid: pid})
		if parts := strings.FieldsFunc(p.name, func(r rune) bool { return r == ' ' || r == '　' }); len(parts) >= 2 && namePartsOK(parts) {
			sur, given := parts[0], strings.Join(parts[1:], "")
			m.surnames[sur] = true
			scode := m.cb.get("姓|"+sur, sur, "姓")
			se := partEntry(sur, scode, "姓だけ")
			se.pid = pid
			m.names = append(m.names, se)
			gcode := m.cb.getSuffix("下|"+prefix+"|"+p.name, given, base, "の名")
			ge := partEntry(given, gcode, prefix+"（名だけ）")
			ge.pid = pid
			m.names = append(m.names, ge)
			pi.sur, pi.given, pi.scode, pi.gcode = sur, given, scode, gcode
		}
		for i, a := range p.aliases {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			code := m.cb.getAlias("別|"+prefix+"|"+p.name+"|"+a, a, base, i+1)
			m.addDateAlias(a, code, prefix+"（呼び名）") // 改善12 No.36：生年月日の形の呼び名
			pi.acodes = append(pi.acodes, code)
			m.names = append(m.names, nameEntry{re: regexp.MustCompile(spaceFlexPattern(a)), code: code, kind: prefix + "（呼び名）", orig: a, lit: a, pid: pid})
		}
		m.persons = append(m.persons, pi)
		m.personSrc = append(m.personSrc, srcIdx)
	}
	m.initC()
	m.initReadings(people)
	// longest first
	sort.SliceStable(m.names, func(a, b int) bool {
		return utf8.RuneCountInString(m.names[a].lit) > utf8.RuneCountInString(m.names[b].lit)
	})
	m.rebuild()
	return m
}

// rebuild compiles the combined name patterns from m.names.
func (m *masker) rebuild() {
	sort.SliceStable(m.names, func(a, b int) bool {
		return utf8.RuneCountInString(m.names[a].lit) > utf8.RuneCountInString(m.names[b].lit)
	})
	m.lookup = map[string]nameEntry{}
	m.cands = map[string][]nameEntry{}
	m.fuse = map[string]nameEntry{}
	var multi, one, two []string
	// 読み取りの誤り・異体字でも見つけられるよう、字形の違う書き方も同じ記号に対応させる
	var withVariants []nameEntry
	for _, ne := range m.names {
		withVariants = append(withVariants, ne)
		for _, v := range nameVariants(ne.lit) {
			nv := ne
			nv.lit = v
			withVariants = append(withVariants, nv)
		}
	}
	sort.SliceStable(withVariants, func(a, b int) bool {
		return utf8.RuneCountInString(withVariants[a].lit) > utf8.RuneCountInString(withVariants[b].lit)
	})
	for _, ne := range withVariants {
		k := normName(ne.lit)
		if cs := m.cands[k]; len(cs) == 0 || cs[len(cs)-1].code != ne.code {
			m.cands[k] = append(cs, ne)
		}
		if _, dup := m.lookup[k]; dup {
			continue
		}
		m.lookup[k] = ne
		// 伏せ字の呼び名（山〇太〇）は、〇・O・0・＊ などどれで書かれていても（まじっていても）同じ記号に（改善22）
		if ne.group == 0 && hasFuseAndHan(ne.lit) {
			if _, ok := m.fuse[fuseKey(ne.lit)]; !ok {
				m.fuse[fuseKey(ne.lit)] = ne
				multi = append(multi, fusePattern(ne.lit))
			}
		}
		if ne.group == 0 {
			multi = append(multi, nameFlexPattern(ne.lit))
		} else if ne.group == 2 {
			two = append(two, regexp.QuoteMeta(ne.lit))
		} else {
			one = append(one, regexp.QuoteMeta(ne.lit))
		}
	}
	if len(multi) > 0 {
		m.reMulti = regexp.MustCompile(`(?:` + strings.Join(multi, `|`) + `)`)
	}
	if len(one) > 0 {
		m.reOne = regexp.MustCompile(`(` + strings.Join(one, `|`) + `)[ 　]?` + honor)
	}
	m.reTwo = nil
	if len(two) > 0 {
		m.reTwo = regexp.MustCompile(`(` + strings.Join(two, `|`) + `)[ 　]?(?:` + honor + `|宅|家|邸)`)
	}
	m.buildNear()
}

var reOnlyDigitsSym = regexp.MustCompile(`^[0-9０-９\-‐－−ー（）()・.,、 　/／:：]*$`)

// addDynamic registers a value read from a registered cell so that the same
// text is replaced everywhere in the workbook (and checked afterwards).
func (m *masker) addDynamic(text, label string) {
	t := strings.TrimSpace(text)
	if utf8.RuneCountInString(normName(t)) < 2 || reOnlyDigitsSym.MatchString(t) || eraWords[t] || stopWords[t] || m.exclude[t] || isCommonValue(t) {
		return
	}
	// 氏名の欄以外（住所・電話など）は、数字を含む値だけをブック全体の対象にする
	if !strings.Contains(label, "名") && !reHasDigit.MatchString(t) {
		return
	}
	if _, ok := m.lookup[normName(t)]; ok {
		return
	}
	// 氏名一覧の語だけでできているなら、そちらの記号を使う
	masked := applyMatches(t, m.findMatches(t, false))
	if masked != t && strings.TrimSpace(reTokenAny.ReplaceAllString(masked, "")) == "" {
		return
	}
	code := m.cb.get("文|"+label+"|"+text, text, label)
	m.names = append(m.names, nameEntry{code: code, kind: "登録セルの値（" + label + "）", orig: t, lit: t})
	m.rebuild()
}

// entryFor: 書き方 k の登録。この書類で優先する記号があればそちら（改善C No.32）
func (m *masker) entryFor(k string) (nameEntry, bool) {
	if e, ok := m.prefer[k]; ok {
		return e, true
	}
	e, ok := m.lookup[k]
	if !ok && hasFuseAndHan(k) {
		e, ok = m.fuse[fuseKey(k)]
	}
	return e, ok
}

// kanaInWord: かなの読みが、同じ種類のかなの語の途中から始まっているか
func kanaInWord(s string, start int, lit string) bool {
	if start == 0 {
		return false
	}
	pr, _ := utf8.DecodeLastRuneInString(s[:start])
	fr, _ := utf8.DecodeRuneInString(lit)
	if isKataRune(fr) {
		return isKataRune(pr)
	}
	if isHiraRune(fr) {
		return isHiraRune(pr)
	}
	return false
}

func normName(s string) string { return strings.NewReplacer(" ", "", "　", "").Replace(s) }

// nameMatches finds listed names (whole names, parts, aliases).
func (m *masker) nameMatches(s string) []match {
	var out []match
	if m.reMulti != nil {
		for _, ix := range m.reMulti.FindAllStringIndex(s, -1) {
			if ne, ok := m.entryFor(normName(s[ix[0]:ix[1]])); ok {
				if ne.kanaStart && (kanaInWord(s, ix[0], ne.lit) || kanaRunsOn(s, ix[1])) {
					continue
				}
				if kanaOrgPlace(s, ix[0], ix[1], ne.lit) {
					continue // 「ステーションさくら草」「デイサービスひかり」「みどりが丘」：事業所名・地名の一部（改善23 試験役 D1）
				}
				if kataNameContinues(s, ix[1], ne.lit) {
					continue // 「トシエさん」：登録した「トシ」のあとにカタカナが続く＝別の方の名前かもしれない。記号にせず聞く（noHonorSuspects 6。改善27の2 試験役 U1）
				}
				out = append(out, match{start: ix[0], end: ix[1], repl: ne.code, kind: ne.kind, orig: s[ix[0]:ix[1]]})
			}
		}
	}
	if m.reTwo != nil {
		for _, ix := range m.reTwo.FindAllStringSubmatchIndex(s, -1) {
			if ne, ok := m.entryFor(s[ix[2]:ix[3]]); ok && !kanaInWord(s, ix[2], ne.lit) && !kataNameContinues(s, ix[3], ne.lit) {
				out = append(out, match{start: ix[2], end: ix[3], repl: ne.code, kind: ne.kind, orig: s[ix[2]:ix[3]]})
			}
		}
	}
	if m.reOne != nil {
		for _, ix := range m.reOne.FindAllStringSubmatchIndex(s, -1) {
			if ne, ok := m.entryFor(s[ix[2]:ix[3]]); ok {
				out = append(out, match{start: ix[2], end: ix[3], repl: ne.code, kind: ne.kind, orig: s[ix[2]:ix[3]]})
			}
		}
	}
	out = append(out, m.dateAliasMatches(s)...) // 改善12 No.36
	return out
}

func isDigitRune(r rune) bool { return (r >= '0' && r <= '9') || (r >= '０' && r <= '９') }

func boundaryOK(s string, start, end int) bool {
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(s[:start])
		if isDigitRune(r) {
			return false
		}
	}
	if end < len(s) {
		r, _ := utf8.DecodeRuneInString(s[end:])
		if isDigitRune(r) {
			return false
		}
	}
	return true
}

// findMatches returns non-overlapping masking matches for text.
func (cb *codeBook) isOurToken(tok string) bool {
	_, ok := cb.codeToOrig[tok]
	return ok
}

func ourTokens(s string, cb *codeBook) [][]int {
	var out [][]int
	for _, ix := range reTokenAny.FindAllStringIndex(s, -1) {
		if cb.isOurToken(s[ix[0]:ix[1]]) {
			out = append(out, ix)
		}
	}
	return out
}

func (m *masker) findMatches(s string, withPatterns bool) []match {
	return m.findMatchesHdr(s, withPatterns, false)
}

// 年齢（ヘッダー・フッターの中だけ。改善5）：「（R8.9.6時点86歳）」の 86歳
var reAge = regexp.MustCompile(D + `{1,3}\s*[歳才]`)

// findMatchesHdr: hdr=true はヘッダー・フッターの中の文字（年齢も記号にする）
func (m *masker) findMatchesHdr(s string, withPatterns, hdr bool) []match {
	var cands []match
	blocked := ourTokens(s, m.cb)
	if withPatterns {
		// 登録済みの住所のかたまり（改善6）。同じ位置から始まる郵便番号・名前より先に置く
		cands = append(cands, m.addrRegMatches(s)...)
	}
	cands = append(cands, m.nameMatches(s)...)
	if withPatterns {
		addPat := func(re *regexp.Regexp, kind string, group int, needBoundary bool) {
			for _, ix := range re.FindAllStringSubmatchIndex(s, -1) {
				a, b := ix[2*group], ix[2*group+1]
				if a < 0 {
					continue
				}
				orig := strings.TrimSpace(s[a:b])
				// trim leading/trailing spaces from range
				for a < b && (s[a] == ' ') {
					a++
				}
				for b > a && (s[b-1] == ' ') {
					b--
				}
				if needBoundary && !boundaryOK(s, a, b) {
					continue
				}
				cands = append(cands, match{start: a, end: b, repl: "", kind: kind, orig: orig})
			}
		}
		addPat(reBirthPre, "生年月日", 1, false)
		addPat(reBirthSuf, "生年月日", 1, false)
		addPat(reMail, "メール", 0, false)
		addPat(rePhoneHy, "電話", 0, true)
		addPat(rePhoneSp, "電話", 0, true)
		addPat(reOldDate, "古い日付", 0, true)
		addPat(reZip, "郵便番号", 0, true)
		addPat(reLongNum, "番号", 0, true)
		if hdr {
			addPat(reAge, "年齢", 0, true)
		} else {
			// 本文の年齢（改善C No.25）：「( 52歳)」「（52才）」「年齢：52歳」
			addPat(reAgeParen, "年齢", 1, true)
			addPat(reAgeLabel, "年齢", 1, true)
		}
	}
	sort.SliceStable(cands, func(a, b int) bool {
		if cands[a].start != cands[b].start {
			return cands[a].start < cands[b].start
		}
		return (cands[a].end - cands[a].start) > (cands[b].end - cands[b].start)
	})
	var out []match
	last := 0
	for _, c := range cands {
		if c.start < last || c.end <= c.start {
			continue
		}
		inTok := false
		for _, t := range blocked {
			if c.start < t[1] && c.end > t[0] {
				inTok = true
				break
			}
		}
		if inTok {
			continue
		}
		if c.repl == "" {
			c.repl = m.cb.get("型|"+c.kind+"|"+c.orig, c.orig, c.kind)
		}
		out = append(out, c)
		last = c.end
	}
	return m.oneCharNameFix(s, out, blocked) // 残った1文字の姓・名（改善C No.24）
}

type suspect struct {
	word    string
	context string
	reason  string
	reg     string // 住所として登録するときの語（改善6。空なら word）
	tok     string // すぐ前後にある名前の記号（だれの名前の一部かを示すため。改善22）
}

// 氏名欄：「氏名」「利用者名」などの見出しのすぐ後ろに、登録されていない漢字の名前らしき文字が残っていないか
const nameLabels = `(?:利用者氏名|利用者名|被保険者氏名|患者氏名|患者名|本人氏名|契約者氏名|申請者氏名|対象者氏名|お名前|御名前|氏\s*名)`

var (
	reLabelName  = regexp.MustCompile(nameLabels + `[ 　\t]*[:：]?[ 　\t]*([\p{Han}々ヶ]{2,5}(?:[ 　][\p{Han}々ヶ]{1,4})?)`)
	reLabelOnly  = regexp.MustCompile(`^[ 　\t]*` + nameLabels + `[ 　\t]*[:：]?[ 　\t]*$`)
	reNameLike   = regexp.MustCompile(`^[\p{Han}々ヶ]{2,5}(?:[ 　][\p{Han}々ヶ]{1,4})?$`)
	labelNotName = map[string]bool{}
)

func init() {
	for _, w := range strings.Fields(`欄 記入 記入欄 記載 署名 押印 自署 生年月日 年齢 性別 住所 続柄 電話 印 等 及び 又は 不詳 未記入 記入例 変更 届出 確認 本人 利用者 家族 代理人 代筆 記名 捺印 フリガナ 振仮名 漢字 楷書 目標 課題 計画 支援 意向 備考 経過 評価 方針 援助 目的 担当 事業 総合 長期 短期 生活 介護 医療 看護 相談 家庭 環境 健康 状態 状況 要望 希望 同意 説明 交付`) {
		labelNotName[w] = true
	}
}

const notNameEnd = `者間日月年名欄書表所容況別等数先時件類課係科部室局院館会議法項目付定額率度型式性力業品物地区市町村県都府道等欄票簿証紙帳録届号番額様殿宛印内外中上下前後期週回標題画援向考過価針的当載告項`

func (m *masker) nameFieldSuspect(w, ctx string) []suspect {
	w = strings.TrimSpace(w)
	nw := normName(w)
	if nw == "" || labelNotName[nw] || stopWords[nw] || isCommonValue(nw) || m.exclude[w] || m.exclude[nw] {
		return nil
	}
	for k := range labelNotName {
		if utf8.RuneCountInString(k) >= 2 && strings.HasPrefix(nw, k) {
			return nil
		}
	}
	// 書類の見出し・用語で終わる語（「会議欠席者」「適用期間」など）は名前とみなさない
	if r, _ := utf8.DecodeLastRuneInString(nw); strings.ContainsRune(notNameEnd, r) {
		return nil
	}
	for _, w := range personNouns {
		if strings.HasSuffix(nw, w) {
			return nil
		}
	}
	return []suspect{{word: w, context: ctx, reason: "氏名欄に、氏名一覧に登録されていない名前らしき文字があります"}}
}

// findLabelNameSuspects: 同じ文の中で「氏名　村田陽子」のように書かれている場合
func (m *masker) findLabelNameSuspects(s string) []suspect {
	var out []suspect
	for _, ix := range reLabelName.FindAllStringSubmatchIndex(s, -1) {
		w := s[ix[2]:ix[3]]
		out = append(out, m.nameFieldSuspect(w, contextAround(s, ix[2], ix[3]))...)
	}
	return out
}

// labelCellSuspect: 見出しだけの欄（前）の次の欄（今）が名前らしいとき（Excel・Wordの表）
func (m *masker) labelCellSuspect(prev, cur string) []suspect {
	if !reLabelOnly.MatchString(prev) {
		return nil
	}
	c := strings.TrimSpace(cur)
	if !reNameLike.MatchString(c) {
		return nil
	}
	return m.nameFieldSuspect(c, strings.TrimSpace(prev)+"［"+c+"］")
}

// findSuspects looks for name-like words left in already-masked text.
func (m *masker) findSuspects(s string) []suspect {
	var out []suspect
	out = append(out, m.findLabelNameSuspects(s)...)
	out = append(out, m.addrSuspects(s)...)
	toks := ourTokens(s, m.cb)
	for _, ix := range reSuspect.FindAllStringSubmatchIndex(s, -1) {
		wi, hi := 2, 4
		if ix[2] < 0 {
			wi, hi = 6, 8
		}
		w := s[ix[wi]:ix[wi+1]]
		hon := s[ix[hi]:ix[hi+1]]
		if ix[1] < len(s) {
			r, _ := utf8.DecodeRuneInString(s[ix[1]:])
			if compoundSkip[hon+string(r)] {
				continue // 様式・様子・氏名 など語の一部
			}
			if hon == "氏" && unicode.Is(unicode.Han, r) && !strings.ContainsRune("宅方邸宛家", r) {
				continue
			}
		}
		if m.honorInCommonWord(s, ix[hi], ix[hi+1]) {
			continue // 模様・同様・仕様・王様 など、敬称の字を含む一般語（除外に書いた語も）
		}
		overl := false
		for _, t := range toks {
			if ix[0] < t[1] && ix[1] > t[0] {
				overl = true
			}
		}
		if endsWithPersonNoun(w, s[:ix[wi]]) {
			continue // 「入浴本人様」「訪問時家族様」など：人を指す一般語で終わる語は名前ではない
		}
		if hon == "氏" && strings.ContainsAny(w, "町村区市郡県都府道丁") {
			continue // 地名
		}
		if overl || stopWords[w] || m.exclude[w] || m.excludeOne[w] || m.endsWithExcluded(s[:ix[wi]]+w) {
			continue
		}
		if notPersonBox(w) {
			continue // 「各事業所様」「長女〇〇様」など（改善23）
		}
		// ignore if the word is a suffix of a stop word context like "お母" handled; skip single hiragana-only common
		reason := "敬称の付いた名前らしき語"
		if m.surnames[w] {
			reason = "氏名一覧の方の姓と同じ（姓だけで書かれている可能性）"
		}
		// 表示用：前に続く漢字（最大6文字）も含めて見せる（「中千鶴子さん」→「田中千鶴子さん」）
		ds := ix[0]
		for k := 0; k < 6 && ds > 0; k++ {
			r, size := utf8.DecodeLastRuneInString(s[:ds])
			if !unicode.Is(unicode.Han, r) && r != '々' {
				break
			}
			ds -= size
		}
		if wd := strings.TrimSpace(s[ds:ix[hi]]); jobSuffix(wd) != "" && utf8.RuneCountInString(strings.TrimSuffix(wd, jobSuffix(wd))) >= 2 {
			continue // 「佐藤看護師様」：職種の前の名前（2字以上）は「佐藤」として別の決まり（rules_ocr）で聞く。「林看護師様」の1字の名字は今までどおりここで（改善27 試験役 R2・点検役 2）
		}
		if addresseeTitle(s[ds:ix[1]]) {
			continue // 「病棟看護師様」「主任ケアマネジャー様」：あて名の職種だけ（改善27）
		}
		out = append(out, suspect{word: s[ds:ix[1]], context: contextAround(s, ds, ix[1]), reason: reason})
	}
	// 読み取りで化けた名前・担当者名などの見逃し対策（rules_ocr.go）：保留を増やすだけで、置き換えはしない
	out = m.ocrSuspects(s, out)
	// 改善C（rules_c.go）：見出しのすぐ後ろの名前・〇〇子さん・1字違いの名前・欄の中身・「ー」区切りの番地。薬情の語は外す
	out = m.cSuspects(s, out)
	// 敬称のない名前（改善23）：ほかの決まりで出ていない所だけ足す（ほかの決まりの理由を優先）
	return m.dropExcluded(m.drugFilter(m.mergeSus(out, m.noHonorSuspects(s))))
}

func contextAround(s string, a, b int) string {
	ra := []rune(s[:a])
	rb := []rune(s[b:])
	pre := ra
	if len(pre) > 15 {
		pre = pre[len(pre)-15:]
	}
	post := rb
	if len(post) > 15 {
		post = post[:15]
	}
	return string(pre) + "［" + s[a:b] + "］" + string(post)
}

// restore
func restoreMatches(s string, cb *codeBook) ([]match, []string) {
	var out []match
	var unknown []string
	for _, ix := range reTokenAny.FindAllStringIndex(s, -1) {
		tok := s[ix[0]:ix[1]]
		if orig, ok := cb.codeToOrig[tok]; ok {
			out = append(out, match{start: ix[0], end: ix[1], repl: orig, kind: "戻す", orig: tok})
		} else if reCodeNum.MatchString(tok) {
			unknown = append(unknown, tok)
		}
	}
	return out, unknown
}

func xmlAttrEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;").Replace(s)
}

// rawMask replaces listed names and e-mail addresses anywhere in raw XML
// (sheet names, formulas, attributes, link targets …).
var (
	reRelTarget   = regexp.MustCompile(`Target="([^"]*)"`)
	reFormulaText = regexp.MustCompile(`<(?:f|definedName|formula1|formula2|formula|xm:f)(?:\s[^>]*)?>([^<]*)</`)
	reQuotedLit   = regexp.MustCompile(`(?:"|&quot;)((?:[^"&]|&amp;|&lt;|&gt;|&apos;|&#[0-9]+;)*?)(?:"|&quot;)`)
)

func (m *masker) rawMask(part, x string) (string, int) {
	var cands []match
	cands = append(cands, m.nameMatches(x)...)
	if strings.HasPrefix(part, "xl/worksheets/") || part == "xl/workbook.xml" {
		// string literals inside formulas: phones, numbers, dates …
		for _, ix := range reFormulaText.FindAllStringSubmatchIndex(x, -1) {
			inner := x[ix[2]:ix[3]]
			for _, lx := range reQuotedLit.FindAllStringSubmatchIndex(inner, -1) {
				lit := inner[lx[2]:lx[3]]
				for _, mt := range m.findMatches(lit, true) {
					mt.start += ix[2] + lx[2]
					mt.end += ix[2] + lx[2]
					cands = append(cands, mt)
				}
			}
		}
	}
	if strings.HasSuffix(part, ".rels") {
		// link targets (tel:, mailto:, file paths …): numbers and phones too
		for _, ix := range reRelTarget.FindAllStringSubmatchIndex(x, -1) {
			v := x[ix[2]:ix[3]]
			for _, mt := range m.findMatches(v, true) {
				mt.start += ix[2]
				mt.end += ix[2]
				cands = append(cands, mt)
			}
		}
	}
	for _, ix := range reMail.FindAllStringIndex(x, -1) {
		o := x[ix[0]:ix[1]]
		cands = append(cands, match{start: ix[0], end: ix[1], kind: "メール", orig: o})
	}
	if len(cands) == 0 {
		return x, 0
	}
	blocked := ourTokens(x, m.cb)
	sort.SliceStable(cands, func(a, b int) bool {
		if cands[a].start != cands[b].start {
			return cands[a].start < cands[b].start
		}
		return cands[a].end-cands[a].start > cands[b].end-cands[b].start
	})
	var out []match
	last := 0
	for _, c := range cands {
		if c.start < last {
			continue
		}
		skip := false
		for _, t := range blocked {
			if c.start < t[1] && c.end > t[0] {
				skip = true
			}
		}
		if skip {
			continue
		}
		if c.repl == "" {
			c.repl = m.cb.get("型|"+c.kind+"|"+c.orig, c.orig, c.kind)
		}
		out = append(out, c)
		last = c.end
	}
	return applyMatches(x, out), len(out)
}

// leftoverCheck returns listed names / known personal strings still present.
func (m *masker) leftoverCheck(part, text string) []string {
	var found []string
	seen := map[string]bool{}
	add := func(w string) {
		if !seen[w] {
			seen[w] = true
			found = append(found, part+"：「"+w+"」")
		}
	}
	for _, x := range m.nameMatches(text) {
		add(x.orig)
	}
	for _, l := range m.addrRegLumps(text) { // 登録済みの住所（改善6）
		add(text[l.st:l.en])
	}
	for _, o := range reMail.FindAllString(text, -1) {
		add(o)
	}
	if m.litN != len(m.cb.keyOrig) {
		m.litN = len(m.cb.keyOrig)
		m.reLit = nil
		var lits []string
		for k, o := range m.cb.keyOrig {
			if strings.HasPrefix(k, "型|") && utf8.RuneCountInString(o) >= 6 {
				lits = append(lits, regexp.QuoteMeta(o))
			}
		}
		sort.Slice(lits, func(a, b int) bool { return len(lits[a]) > len(lits[b]) })
		if len(lits) > 0 {
			m.reLit = regexp.MustCompile(strings.Join(lits, "|"))
		}
	}
	if m.reLit != nil {
		for _, o := range m.reLit.FindAllString(text, -1) {
			add(o)
		}
	}
	return found
}

// rawRestore puts originals back for our tokens anywhere in raw XML.
var reEncTok = regexp.MustCompile(`&#12304;(?:[^&<>"]|&#[0-9]+;){1,80}?&#12305;`)

func rawRestore(x string, cb *codeBook) (string, int) {
	n := 0
	x = reEncTok.ReplaceAllStringFunc(x, func(e string) string {
		if o, ok := cb.codeToOrig[decodeEntities(e)]; ok {
			n++
			return xmlAttrEscape(o)
		}
		return e
	})
	out := reTokenAny.ReplaceAllStringFunc(x, func(tok string) string {
		if o, ok := cb.codeToOrig[tok]; ok {
			n++
			return xmlAttrEscape(o)
		}
		return tok
	})
	return out, n
}

var reBirthOnly = regexp.MustCompile(`^\s*(` + dateCore + `)`)

// birthNext masks a date at the start of a text that follows a 「生年月日」 label (table cells).
func (m *masker) birthNext(prev, s string) []match {
	p := strings.Trim(prev, " 　:：\t")
	if !strings.HasSuffix(p, "生年月日") {
		return nil
	}
	ix := reBirthOnly.FindStringSubmatchIndex(s)
	if ix == nil {
		return nil
	}
	a, b := ix[2], ix[3]
	orig := strings.TrimSpace(s[a:b])
	return []match{{start: a, end: b, kind: "生年月日", orig: orig, repl: m.cb.get("型|生年月日|"+orig, orig, "生年月日")}}
}

var commonValues = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`なし 無し 無 特になし 特に無し 該当なし 該当無し 同上 同左 同右 上記 上記に同じ 不明 未定 未記入 未確認 未 空欄 なし（本人のみ） 本人 本人のみ 独居 施設 入院中 別紙 別紙参照 省略 ー － - ― なし。 なし、 ☐ ☑ 有 有り あり`) {
		commonValues[w] = true
	}
}

func isCommonValue(t string) bool {
	return commonValues[strings.Trim(t, " 　。、")]
}

// 人を指す一般語（2文字以上）。語の末尾がこれで、前が動作の語か何もないときは名前とみなさない。
// 「夫」「父」「母」など1文字の続柄は、和夫・正夫のような名前と区別できないので入れない。
var personNouns = strings.Fields(`本人 利用者 家族 長男 長女 次男 次女 三男 三女 主人 息子 叔父 叔母 伯父 伯母 両親 夫婦 兄弟 姉妹 父親 母親 親戚 親族 隣人 担当者 職員 看護師 医師 主治医 相談員 管理者 家主 大家 相手 患者 入居者 施設長 皆様 奥様 ご主人 旦那 後見人 保佐人 補助人 代理人 民生委員 契約者 申請者 対象者 介護者 支援者 家政婦`)

// 一般語の前に付いてもよい、動作・場面の語（「入浴本人様」「訪問時家族様」など）
var actionWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`入浴 排泄 排せつ 通院 訪問 訪問時 送迎 食事 介助 入院 退院 来所 来訪 受診 移動 移乗 更衣 整容 服薬 面談 電話 連絡 同行 付添 付添い 外出 買物 買い物 調理 掃除 洗濯 清拭 口腔 口腔ケア 歩行 起居 就寝 起床 散歩 通所 宿泊 利用 相談 説明 確認 同席 同居 別居 独居 当日 翌日 前日 当時 現在 今回 前回 次回 毎回 退所 入所 帰宅 在宅 自宅 施設 病院 担当 主 副 各 元 前 新 旧 他 同`) {
		actionWords[w] = true
	}
}

func endsWithPersonNoun(w, before string) bool {
	for _, n := range personNouns {
		if strings.HasSuffix(w, n) {
			rest := strings.TrimSuffix(w, n)
			if rest == "" {
				return true
			}
			full := before + rest // 語の前の文字も含めて、動作の語で終わっているか
			for aw := range actionWords {
				if strings.HasSuffix(full, aw) {
					return true
				}
			}
			return false // 「田中本人様」「山田家族様」の田中・山田は名前かもしれない
		}
	}
	// 「〜者」「〜員」「〜師」（介助者・相談員・看護師など）：3文字以上の語のときだけ一般語とみなす
	// （2文字の「薬師」「武者」などは実在する姓のため）
	if utf8.RuneCountInString(w) >= 3 && (strings.HasSuffix(w, "者") || strings.HasSuffix(w, "員") || strings.HasSuffix(w, "師")) {
		rest := w
		for _, suf := range []string{"者", "員", "師"} {
			rest = strings.TrimSuffix(rest, suf)
		}
		return true
	}
	return false
}

// 取り違えやすい字（読み取りの誤り・異体字）。左の字を含む名前には、右の字に替えた書き方も登録する。
var variantPairs = map[rune][]rune{
	'一': {'ー', '－', '‐', '—'},
	'藤': {'籐'}, '籐': {'藤'},
	'徳': {'德'}, '德': {'徳'},
	'高': {'髙'}, '髙': {'高'},
	'崎': {'﨑', '碕'}, '﨑': {'崎'},
	'辺': {'邊', '邉'}, '邊': {'辺', '邉'}, '邉': {'辺', '邊'},
	'斉': {'齊', '斎', '齋'}, '斎': {'齋', '斉', '齊'}, '齋': {'斎', '斉'}, '齊': {'斉', '斎'},
	'沢': {'澤'}, '澤': {'沢'},
	'浜': {'濱'}, '濱': {'浜'},
	'島': {'嶋', '嶌'}, '嶋': {'島'},
	'広': {'廣'}, '廣': {'広'},
	'国': {'國'}, '國': {'国'},
	'栄': {'榮'}, '榮': {'栄'},
	'恵': {'惠'}, '惠': {'恵'},
	'桜': {'櫻'}, '櫻': {'桜'},
	'竜': {'龍'}, '龍': {'竜'},
	'瀬': {'瀨'}, '瀨': {'瀬'},
	'条': {'條'}, '條': {'条'},
	'寿': {'壽'}, '壽': {'寿'},
	'実': {'實'}, '實': {'実'},
	'真': {'眞'}, '眞': {'真'},
	'二': {'ニ'}, '口': {'ロ'}, '力': {'カ'}, '工': {'エ'}, '夕': {'タ'}, '八': {'ハ'},
}

// nameVariants returns other spellings of s (up to 2 characters changed, at most 24).
func nameVariants(s string) []string {
	seen := map[string]bool{s: true}
	cur := []string{s}
	for depth := 0; depth < 2; depth++ {
		var next []string
		for _, w := range cur {
			rs := []rune(w)
			for i, r := range rs {
				for _, alt := range variantPairs[r] {
					nr := append([]rune(nil), rs...)
					nr[i] = alt
					v := string(nr)
					if !seen[v] {
						seen[v] = true
						next = append(next, v)
					}
				}
			}
		}
		cur = next
	}
	var out []string
	for v := range seen {
		if v != s {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

// endsWithExcluded: 除外の語（2文字以上）で終わるなら、前に何が付いていても名前扱いしない
// （「居宅介護支援佐藤様」の佐藤、「相談員鈴木さん」の鈴木 など）。
func (m *masker) endsWithExcluded(w string) bool {
	for ex := range m.exclude {
		if utf8.RuneCountInString(ex) >= 2 && strings.HasSuffix(w, ex) {
			return true
		}
	}
	return false
}

// namePartsOK: 空白で分けた字が、どれも名前の字（漢字・かな・英字）だけか。
// 「39 /10/ 17」のような数字・記号の行（その他に入れた日付など）を、姓「39」と名に分けない（改善27：
// 分けると、どの書類の「39」も名字の記号になり、Excelの中の数字まで置き換わって開けなくなった）
func namePartsOK(parts []string) bool {
	for _, x := range parts {
		if strings.ContainsAny(x, "123456789１２３４５６７８９/／") { // 0 は伏せ字（山0 太0）にも使われるので見ない
			return false
		}
		letter := false
		for _, r := range x {
			if unicode.IsLetter(r) {
				letter = true
				break
			}
		}
		if !letter {
			return false
		}
	}
	return true
}

// rowPrefix: 氏名一覧の行の記号の頭。「その他」の行で生年月日らしい形（昭和・大正・明治か元号なしの年／月／日。
// 「/」「.」「年月」区切り。月1〜12・日1〜31）は【生年月日NNN】にする（②の生年月日の箱から入れた行。改善27・点検役 5）。
// 令和・平成の日付、「-」区切り（番地の形）は生年月日とみなさない
func rowPrefix(kind, name string) string {
	if kind == "その他" && birthLikeRow(strings.TrimSpace(name)) {
		return "生年月日"
	}
	return sanitizePrefix(kind)
}

var reBirthRow = regexp.MustCompile(`^(明治|大正|昭和|[明大昭MTS])?[ 　]*([0-9０-９]{1,4})[ 　]*(?:年|[/／.．])[ 　]*([0-9０-９]{1,2})[ 　]*(?:月|[/／.．])[ 　]*([0-9０-９]{1,2})(?:[ 　]*日)?$`)

func birthLikeRow(w string) bool {
	m := reBirthRow.FindStringSubmatch(w)
	if m == nil {
		return false
	}
	num := func(x string) int {
		n := 0
		for _, r := range x {
			if r >= '０' && r <= '９' {
				r = r - '０' + '0'
			}
			n = n*10 + int(r-'0')
		}
		return n
	}
	y, mo, d := num(m[2]), num(m[3]), num(m[4])
	if mo < 1 || mo > 12 || d < 1 || d > 31 {
		return false
	}
	if m[1] == "" && len([]rune(m[2])) == 4 && y > 1995 {
		return false // 西暦の最近の日付は生年月日ではない
	}
	return true
}

// isKataOnly: カタカナ（と「ー」）だけの語
func isKataOnly(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		if !(unicode.Is(unicode.Katakana, r) || r == 'ー') {
			return false
		}
	}
	return true
}

// kataNameContinues: カタカナの名前（トシ）のすぐ後ろに、カタカナの字（エ）が続くか（「トシエ」は別の方の名前かもしれない）
func kataNameContinues(s string, end int, lit string) bool {
	if end >= len(s) || !isKataOnly(normName(lit)) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[end:])
	return unicode.Is(unicode.Katakana, r) && r != 'ー' && r != '・'
}
