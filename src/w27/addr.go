package main

// 住所の要確認（改善5・No.15／No.16、差し戻し（追加）1回目）。どれも「保留（要確認）」を増やすだけで、置き換えの規則はゆるめない。
//
//   - 番地のあとに半角・全角スペースを挟んで続く建物名・部屋番号（「桜町1-2-3 サンプルハイツB-201号」
//     「〇〇町2丁目5番地　コーポ山田 102」、番地のすぐ後ろなら目印のない「山田 102」も）を、住所の一部として候補語に含める
//   - 市区町村名のない形（「桜町1-2-3 サンプルハイツB-201号」）も、建物名・部屋番号が続けば保留にする
//   - 住所を「呼び名」で記号にしたあとに建物名・部屋番号が残っていたら保留にする
//   - 丁目・番地・番＋号の形の番地（「〇〇町2丁目5番地」「中央3丁目9番51号」）も住所とみなす（建物名がなくても）
//   - 候補語は都道府県名から始まる形で出す（「川県大和市…」のように頭が欠けない）
//   - 住所の頭の市区町村名が氏名一覧の姓と同じ字で記号になっていても（「【姓001】市上郷1-2-3」＝大和市上郷1-2-3）、
//     元の字に戻して住所の形として見て、住所全体を保留にする（本文中の氏名は今までどおり記号）
//
// 除外の登録は、今の候補語でも、前の版の候補語（頭の欠けた形も含む）でも効く（前の版で除外が効いていたものは今も効く）。

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	addrD    = `[0-9０-９]`
	addrH    = `[-－ー‐−]`
	addrPref = `(?:北海道|東京都|大阪府|京都府|[\p{Han}]{2,3}県)`
	// 番地の形：1-2-3／2丁目5番地・3丁目9番51号・3丁目9-51／12番地・12番地の3／9番51号
	addrNum1 = addrD + `+(?:[ 　]?[-－ー‐−の][ 　]?` + addrD + `+)(?:[-－ー‐−の]` + addrD + `+){0,2}` // FAXの読み取りで最初の区切りの前後に空白が入る形（「4　ー4ー2ー105」）も。2つ目からは空白なし（「3-9-51 ー 106号室」の部屋番号まで番地に入れない。改善27・点検役 3）
	addrNum2 = `(?:` + addrD + `+|[一二三四五六七八九十]+)丁目\s*` + addrD + `+(?:\s*番地?)?(?:\s*(?:の|` + addrH + `)?\s*` + addrD + `+)?(?:\s*号)?`
	addrNum3 = addrD + `+\s*番地(?:\s*の?\s*` + addrD + `+)?`
	addrNum4 = addrD + `+\s*番\s*` + addrD + `+\s*号`
	addrNum  = `(?:` + addrNum2 + `|` + addrNum4 + `|` + addrNum3 + `|` + addrNum1 + `)`
	// 市区町村名：漢字（とヶ・ケ）だけの名前
	addrMuniHan = `[\p{Han}ヶケ]{1,5}[市区町村郡]`
	// 都道府県名のすぐ後ろなら、かなを含む名前も市区町村とみなす（一覧にない名前・架空の名前も）。
	// 都道府県名のない「のみの市」のような語は、ここには当たらない
	addrMuniPrefKana = `[\p{Han}\p{Hiragana}\p{Katakana}ーヶケノ]{1,8}?[市区町村郡]`
	// 町名・字名
	addrTown = `[\p{Han}\p{Katakana}ヶケノの]{0,8}`
)

// kanaMunicipalities: ひらがな・カタカナを含む実在の市区町村名（都道府県名がなくても市区町村とみなす）。
// 「つくばみらい市」が「つくば市」より先に当たるよう、長い名前から並べる（addrKanaMuni で並べ替える）
var kanaMunicipalities = strings.Fields(`
	つがる市 むつ市 おいらせ町 にかほ市 いわき市
	ひたちなか市 つくば市 かすみがうら市 つくばみらい市 さくら市 みどり市 みなかみ町
	さいたま市 ふじみ野市 ときがわ町 いすみ市 あきる野市
	かほく市 あわら市 おおい町 南アルプス市 伊豆の国市
	みよし市 あま市 いなべ市 たつの市 南あわじ市 かつらぎ町 みなべ町
	さぬき市 東かがわ市 まんのう町 つるぎ町 東みよし町 いの町
	うきは市 みやま市 みやこ町 みやき町 あさぎり町 えびの市
	いちき串木野市 南さつま市 さつま町 うるま市
	ニセコ町 せたな町 むかわ町 えりも町 新ひだか町 上ノ国町
`)

var addrKanaMuni = func() string {
	names := append([]string(nil), kanaMunicipalities...)
	sort.SliceStable(names, func(i, j int) bool { return utf8.RuneCountInString(names[i]) > utf8.RuneCountInString(names[j]) })
	return `(?:` + strings.Join(names, `|`) + `)`
}()

var (
	// 前の版（改善4まで）の住所の形。除外の照合にだけ使う（前の版の候補語で登録された除外を効かせるため）
	reAddrOld = regexp.MustCompile(`[\p{Han}ヶケ]{1,5}[市区町村郡][\p{Han}\p{Katakana}ヶケノの]{0,8}[0-9０-９]+(?:[-－ー‐−の][0-9０-９]+){1,3}`)
	// 今の住所の形（都道府県から・丁目などの番地も）。
	// 市区町村名は、①都道府県＋かなを含む名前、②（都道府県）＋かなを含む実在の名前、③（都道府県）＋漢字の名前 の順に見る
	reAddr = regexp.MustCompile(`(?:` + addrPref + addrMuniPrefKana + `|(?:` + addrPref + `)?(?:` + addrKanaMuni + `|` + addrMuniHan + `))` + addrTown + addrNum)
	// 市区町村名のない形（建物名・部屋番号が続くときだけ保留）
	reAddrNC   = regexp.MustCompile(`[\p{Han}ヶケ]{2,8}` + addrNum)
	reHanOnly  = regexp.MustCompile(`^[\p{Han}ヶケ々]{1,6}$`)
	reRoomOnly = regexp.MustCompile(`^[A-Za-zＡ-Ｚａ-ｚ]?[-－‐]?[0-9０-９]{1,4}(?:号室|号|室)?$`)
	reRoomTail = regexp.MustCompile(`[A-Za-zＡ-Ｚａ-ｚ'’]?[-－‐]?[0-9０-９]{1,4}(?:号室|号|室)?$`)
	reAddrOrig = regexp.MustCompile(`[市区町村郡都道府県丁番]|[0-9０-９]+[-－ー‐−][0-9０-９]+[-－ー‐−][0-9０-９]+`)
)

// 建物名の目印
var buildingWords = strings.Fields(`ハイツ マンション コーポ レジデンス ハウス アパート メゾン パレス コート ヒルズ ビル ヴィラ ハイム テラス タワー ガーデン パーク フラット ドミール カーサ シャトー グラン ロイヤル プラザ ステージ ホームズ 荘 寮 団地 住宅 棟 館 苑`)

const addrReason = "番地まで書かれた住所らしき文字。利用者・家族の住所なら「住所として置き換える」（氏名一覧の区分「住所」に登録）、事業所などの住所なら「除外」に登録してください"
const addrTailReason = "番地のあとに続く建物名・部屋番号らしき文字（住所の続き）。利用者・家族の住所なら、前の住所と合わせて「住所として置き換える」、事業所などなら「除外」に登録してください"

// 記号の種類のうち、住所の続きとみなさないもの（電話・日付など）
var notAddrCodeKinds = map[string]bool{"電話": true, "郵便番号": true, "番号": true, "メール": true, "生年月日": true, "古い日付": true, "年齢": true}

func hasBuildingWord(s string) bool {
	for _, w := range buildingWords {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func isLatinRune(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= 'Ａ' && r <= 'Ｚ') || (r >= 'ａ' && r <= 'ｚ')
}

// roomLike: カタカナ・英字で始まり、部屋番号らしき数字で終わる語（「コーポ'99」「メゾン102」）。ひらがなを含むものは入れない
func roomLike(c string) bool {
	r, _ := utf8.DecodeRuneInString(c)
	if !(unicode.Is(unicode.Katakana, r) || isLatinRune(r)) {
		return false
	}
	for _, x := range c {
		if unicode.Is(unicode.Hiragana, x) {
			return false
		}
	}
	loc := reRoomTail.FindStringIndex(c)
	return loc != nil && utf8.RuneCountInString(c[:loc[0]]) >= 2
}

// roomOnlyStrong: 部屋番号だけの語で、号・室が付くか英字が付くもの（「201号室」「B-201」）
func roomOnlyStrong(c string) bool {
	if !reRoomOnly.MatchString(c) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(c)
	return isLatinRune(r) || strings.HasSuffix(c, "号") || strings.HasSuffix(c, "室")
}

func isAddrStop(r rune) bool {
	return r == ' ' || r == '　' || r == '\t' || r == '\n' || r == '\r' || strings.ContainsRune("。、，,（）()「」『』【】｜|:：;；!！?？", r)
}

// skipSep: 空白に加えて、改行・ヘッダーの区切り（｜）を2つまで飛ばす（改善6）
func skipSep(s string, i int) int {
	nl := 0
	for i < len(s) {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if sl := sepLen(s, i); sl > 0 {
			if r != '\n' || i == 0 || s[i-1] != '\r' {
				nl++
			}
			if nl > 2 {
				break
			}
			sz = sl
		} else if r != ' ' && r != '　' && r != '\t' {
			break
		}
		i += sz
	}
	return i
}

func skipSpaces(s string, i int) int {
	for i < len(s) {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if r != ' ' && r != '　' {
			break
		}
		i += sz
	}
	return i
}

// isPlainBuilding: 目印のない建物名らしき語（漢字・カタカナ2〜10字。ひらがな・数字なし）
func isPlainBuilding(c string) bool {
	n := utf8.RuneCountInString(c)
	if n < 2 || n > 10 {
		return false
	}
	for _, r := range c {
		if !(unicode.Is(unicode.Han, r) || unicode.Is(unicode.Katakana, r) || r == 'ー' || r == '々' || r == 'ヶ') {
			return false
		}
	}
	return true
}

// roomNumberish: 部屋番号だけの語（「102」「B-201」「201号室」）。数字だけのときは3〜4けた
func roomNumberish(c string) bool {
	if !reRoomOnly.MatchString(c) {
		return false
	}
	if roomOnlyStrong(c) {
		return true
	}
	d := 0
	for _, r := range c {
		if isDigitRune(r) {
			d++
		}
	}
	return d >= 3
}

// placeView: 市区町村・都道府県の字のすぐ前にある記号（地名と同じ字の姓など）を元の字に戻した文と、戻した所（view の中の位置）。
// 住所を調べるためだけに使う。戻した所を含む住所は、建物名・部屋番号が続くときだけ保留にする（それ以外は前の版と同じ）
func (m *masker) placeView(s string) (string, [][2]int) {
	toks := ourTokens(s, m.cb)
	if len(toks) == 0 {
		return s, nil
	}
	var b strings.Builder
	var spans [][2]int
	pos := 0
	for _, t := range toks {
		orig := m.cb.codeToOrig[s[t[0]:t[1]]]
		if t[1] < len(s) && startsPlace(s[t[1]:]) && reHanOnly.MatchString(orig) {
			b.WriteString(s[pos:t[0]])
			st := b.Len()
			b.WriteString(orig)
			spans = append(spans, [2]int{st, b.Len()})
			pos = t[1]
		}
	}
	b.WriteString(s[pos:])
	return b.String(), spans
}

// expandAll: 記号をすべて元の字に戻した文（建物名・部屋番号の候補語を、登録できる元の字で出すため）
func (m *masker) expandAll(s string) string {
	return restoreText(s, m.cb)
}

// chunkAtTok: chunkAt と同じだが、途中の記号（建物名の中の姓と同じ字など）は1字として読み進める
func (m *masker) chunkAtTok(s string, i int) int {
	n := 0
	for i < len(s) && n < 24 {
		if strings.HasPrefix(s[i:], "【") {
			if loc := reTokenAny.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 && m.cb.isOurToken(s[i:i+loc[1]]) {
				i += loc[1]
				n++
				continue
			}
		}
		r, sz := utf8.DecodeRuneInString(s[i:])
		if isAddrStop(r) || sepLen(s, i) > 0 {
			break
		}
		i += sz
		n++
	}
	return i
}

// cutParticle: 建物名のあとに続く助詞など（「メゾン大和305号室です」の「です」）を切る。
// 先頭のひらがな（「ひばり荘」）はそのまま
func cutParticle(c string) (string, bool) {
	seenOther := false
	for i, r := range c {
		if unicode.Is(unicode.Hiragana, r) {
			if seenOther {
				return c[:i], true
			}
			continue
		}
		seenOther = true
	}
	return c, false
}

// tailAfter: s[pos:] に続く建物名・部屋番号。返すのは、元の字に戻した続きの文字（前の空白を含む）と、s の中での終わりの位置。
// 続きがすでに記号だけ（呼び名に登録済み）なら、なし
func (m *masker) tailAfter(s string, pos int, plainName bool) (string, int) {
	return m.tailAfterB(s, pos, plainName, false)
}

// tailAfterB: boundary=true は、s の頭が前の段落・セル・行の続き（境目を空白とみなす）。
// 改善6：番地のあとの改行（セル内改行・ヘッダーの改行・Word の改行）やヘッダーの書式記号（｜）も越えて、建物名・部屋番号を読む
func (m *masker) tailAfterB(s string, pos int, plainName, boundary bool) (string, int) {
	i := pos
	if strings.HasPrefix(s[i:], "号") {
		i += len("号")
	}
	st := skipSep(s, i)
	hadSpace := st > i || boundary
	e1 := m.chunkAtTok(s, st)
	c1m := s[st:e1]
	if c1m == "" || strings.TrimSpace(reTokenAny.ReplaceAllStringFunc(c1m, func(t string) string {
		if m.cb.isOurToken(t) {
			return ""
		}
		return t
	})) == "" {
		return "", pos
	}
	c1, cut := cutParticle(m.expandAll(c1m))
	if c1 == "" {
		return "", pos
	}
	ok := hasBuildingWord(c1)
	if !ok && hadSpace {
		ok = roomLike(c1) || roomOnlyStrong(c1)
	}
	// 目印のない建物名＋部屋番号（「山田 102」）：番地のすぐ後ろ（市区町村のある住所・住所の記号のあと）のときだけ（差し戻し追加1）
	if !ok && hadSpace && plainName && !cut && isPlainBuilding(c1) {
		if j := skipSpaces(s, e1); j > e1 {
			e2 := m.chunkAtTok(s, j)
			if c2 := s[j:e2]; !strings.Contains(c2, "【") && roomNumberish(c2) {
				return s[pos:st] + c1 + s[e1:j] + c2, e2
			}
		}
	}
	if !ok {
		return "", pos
	}
	word := s[pos:st] + c1
	end := e1
	if !cut {
		if j := skipSpaces(s, e1); j > e1 {
			e2 := m.chunkAtTok(s, j)
			if c2 := m.expandAll(s[j:e2]); reRoomOnly.MatchString(c2) && !strings.Contains(s[j:e2], "【") {
				word += s[e1:j] + c2
				end = e2
			}
		}
	}
	return word, end
}

// addrExcluded: 除外に登録された住所か（今の候補語・前の版の候補語のどちらでも）
func (m *masker) addrExcluded(words ...string) bool {
	for _, w := range words {
		if w == "" {
			continue
		}
		if m.exclude[w] || m.exclude[strings.TrimSpace(w)] {
			return true
		}
		if old := reAddrOld.FindString(w); old != "" && m.exclude[old] {
			return true
		}
	}
	return false
}

func overlapsAny(spans [][2]int, a, b int) bool {
	for _, sp := range spans {
		if a < sp[1] && b > sp[0] {
			return true
		}
	}
	return false
}

// addrSuspects: 置き換えたあとの文に残る住所らしき文字
func (m *masker) addrSuspects(s string) []suspect {
	var out []suspect
	view, _ := m.placeView(s)
	var covered [][2]int
	for _, ix := range reAddr.FindAllStringIndex(view, -1) {
		core := view[ix[0]:ix[1]]
		if strings.Contains(core, "【") {
			continue
		}
		tail, tend := m.tailAfter(view, ix[1], true)
		covered = append(covered, [2]int{ix[0], tend})
		w := core + tail
		if m.addrExcluded(core, w) || m.isFormWord(core) || m.isFormWord(w) {
			continue // 様式に印刷されている事業所の所在地（1表。改善27の2）も止めない
		}
		out = append(out, suspect{word: w, context: contextAround(view[:ix[0]]+w+view[tend:], ix[0], ix[0]+len(w)), reason: addrReason, reg: cleanAddr(w)})
	}
	// 市区町村名のない形：建物名・部屋番号が続くときだけ
	for _, ix := range reAddrNC.FindAllStringIndex(view, -1) {
		if overlapsAny(covered, ix[0], ix[1]) {
			continue
		}
		core := view[ix[0]:ix[1]]
		if strings.Contains(core, "【") {
			continue
		}
		tail, tend := m.tailAfter(view, ix[1], false)
		if tail == "" {
			continue
		}
		w := core + tail
		if m.addrExcluded(core, w) {
			continue
		}
		out = append(out, suspect{word: w, context: contextAround(view[:ix[0]]+w+view[tend:], ix[0], ix[0]+len(w)), reason: addrReason, reg: cleanAddr(w)})
	}
	// 住所の記号（呼び名・登録セルの住所）のあとに残る建物名・部屋番号
	for _, t := range ourTokens(s, m.cb) {
		tok := s[t[0]:t[1]]
		orig := m.cb.codeToOrig[tok]
		if !reHasDigit.MatchString(orig) || !reAddrOrig.MatchString(orig) {
			continue
		}
		if cm := reCodeNum.FindStringSubmatch(tok); cm != nil && notAddrCodeKinds[cm[1]] {
			continue
		}
		raw, tend := m.tailAfter(s, t[1], true)
		tail := strings.TrimLeft(strings.TrimPrefix(raw, "号"), " 　")
		if tail == "" || m.exclude[tail] || m.exclude[normName(tail)] {
			continue
		}
		ctx := s[:t[1]] + raw
		ctxAll := ctx + s[tend:]
		out = append(out, suspect{word: tail, context: contextAround(ctxAll, t[0], len(ctx)), reason: addrTailReason, reg: m.regFor(s[:t[1]], raw)})
	}
	return out
}
