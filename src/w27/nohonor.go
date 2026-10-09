package main

// 敬称のない名前の見逃し対策（改善23・R8.10.5 試験役ベテランケアマネの指摘 A1）
//   これまでは「様・さん」などの敬称が付いた名前だけを確かめていたため、次の名前が素通りしていた：
//     ・表の欄・行の中に、姓と名をスペースで区切って書いた名前（「民生委員 | 岡本 澄子 | 欠席」）
//     ・続柄・関係のすぐ後ろの名前（「妻 花子（同居）」「長男 次郎（市外）」「隣人の大川ミツ」）
//     ・登録した姓の記号のすぐ後ろに残った下の名前（「【その他074】 健太」＝前田 健太）
//   どれも、記号にはせず「保留（要確認・名前）」にして人に聞く。誤って止めすぎないよう、
//   下の名前は「名前によくある終わりの字」か、カタカナ・ひらがな2〜3字のときだけにする。

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 下の名前によくある終わりの字（漢字の名）。一般の語の終わりにもよく出る字（介・助・明・信・正・治・実・進・人・作 など）は入れない
const givenEndChars = "子郎夫男雄美江代枝恵香奈乃世也太司彦樹輝斗翔史志之里加佳絵穂菜希紀吉蔵松衛造朗哉弘博宏浩茂清勝隆豊誠秀昭義徳敏孝剛健優"

// 続柄・関係の語（このすぐ後ろの語は名前のことが多い）
var relHeadWords = strings.Fields(`妻 夫 長男 長女 次男 次女 三男 三女 四男 四女 息子 娘 孫 嫁 婿 兄 姉 弟 妹 父 母 義母 義父 義兄 義姉 義弟 義妹 甥 姪 叔母 叔父 伯母 伯父 祖母 祖父 実母 実父 内縁の妻 内縁の夫 隣人 知人 友人 民生委員 キーパーソン 主介護者 後見人`)

var (
	reHan   = `[\p{Han}々ヶ]`
	reKata  = `[\p{Katakana}ー]`
	reHira  = `[\p{Hiragana}]`
	reGiven = `(?:` + reHan + `{1,3}|` + reKata + `{2,3}|` + reHira + `{2,3})`
	// 名前の書き方：姓 名（空白あり）・漢字の姓＋カタカナの名・漢字だけ・カナだけ（漢字のすぐ後ろのひらがなは名にしない：「腰痛あり」）
	reNameAny = `(?:` + reHan + `{1,3}[ 　]` + reGiven + `|` + reHan + `{1,3}` + reKata + `{2,3}|` + reHan + `{1,3}|` + reKata + `{2,3}|` + reHira + `{2,3})`
	// 行・欄の中の「姓 名」（前後は行の端・区切り・空白）
	reSpacedFull = regexp.MustCompile(`(?:^|[\s|｜,，、・（(：:／/])(` + reHan + `{1,3})[ 　](` + reGiven + `)(?:$|[\s|｜,，、・（()）：:／/。])`)
	// 続柄のあとの名前
	reRelName = func() *regexp.Regexp {
		w := append([]string{}, relHeadWords...)
		for i := range w {
			w[i] = regexp.QuoteMeta(w[i])
		}
		return regexp.MustCompile(`(` + strings.Join(w, "|") + `)[ 　:：の]?(` + reNameAny + `)(?:$|[^\p{Han}\p{Katakana}\p{Hiragana}ー々])`)
	}()
	// 名前の記号のすぐ後ろ（空白1つまで）に残った下の名前
	reTokGiven = regexp.MustCompile(`(【[^【】]+】)[ 　]?(` + reGiven + `)(?:$|[^\p{Han}\p{Katakana}\p{Hiragana}ー々])`)
)

func isKanaOnly(w string) bool {
	for _, r := range w {
		if !(unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hiragana, r) || r == 'ー') {
			return false
		}
	}
	return w != ""
}

// givenLike: 下の名前らしいか（漢字：よくある終わりの字。カナ：2〜3字で一般の語でない）
func givenLike(g string, kanaOK bool) bool {
	g = normName(g)
	n := utf8.RuneCountInString(g)
	if n == 0 || n > 3 {
		return false
	}
	if isKanaOnly(g) {
		return kanaOK && n >= 2 && !strings.HasPrefix(g, "ー")
	}
	last, _ := utf8.DecodeLastRuneInString(g)
	return strings.ContainsRune(givenEndChars, last)
}

// notNameWord: 名前でない語（一般の語・職種・続柄・事業所・住所・除外）
func (m *masker) notNameWord(w string) bool { return m.notNameWordEx(w, true) }

func (m *masker) notNameWordEx(w string, useEx bool) bool {
	n := normName(w)
	if useEx && (m.exclude[n] || m.exclude[w] || m.excludeOne[n] || m.endsWithExcluded(n)) {
		return true
	}
	if n == "" || stopWords[n] || isCommonWord(n) || relationWord(n) || isNeighborWord(n) || isOrgWord(n) || isAddrLike(n) || hasCounter(n) {
		return true
	}
	if kataCommon[n] || kanjiNumWord(n) || honorOnly[n] {
		return true
	}
	for _, j := range commonJobWords {
		if n == j || strings.HasSuffix(n, j) {
			return true
		}
	}
	for _, r := range relHeadWords {
		if n == r {
			return true
		}
	}
	return false
}

// noHonorSuspects: 敬称のない名前らしき語（記号にはしない。保留にして人に聞く）
func (m *masker) noHonorSuspects(s string) []suspect {
	var out []suspect
	add := func(a, b int, why string) {
		w := s[a:b]
		if strings.Contains(w, "【") && !strings.HasPrefix(w, "【") {
			return
		}
		out = append(out, suspect{word: w, context: contextAround(s, a, b), reason: "敬称のない名前らしき語（" + why + "）"})
	}
	// 1) 行・欄の中の「姓 名」（合わなかったときは、名の前の空白から探し直す：「病院 中村 花子」）
	for pos := 0; pos < len(s); {
		ix := reSpacedFull.FindStringSubmatchIndex(s[pos:])
		if ix == nil {
			break
		}
		for i := range ix {
			if ix[i] >= 0 {
				ix[i] += pos
			}
		}
		sur, giv := s[ix[2]:ix[3]], s[ix[4]:ix[5]]
		// 姓だけを除外にしていても（自分の事業所の「前田」）、同じ姓の別の方「前田 健太」は止める（点検役）
		if m.notNameWordEx(sur, false) || m.notNameWord(giv) || m.notNameWord(sur+" "+giv) || !givenLike(giv, true) {
			pos = ix[3] // 名の前の空白から
			continue
		}
		pos = ix[5]
		if _, ok := m.lookup[normName(sur+giv)]; ok {
			continue
		}
		add(ix[2], ix[5], "姓と名の形")
	}
	// 2) 続柄・関係のすぐ後ろ
	for _, ix := range reRelName.FindAllStringSubmatchIndex(s, -1) {
		nm := s[ix[4]:ix[5]]
		st := ix[4]
		parts := strings.FieldsFunc(nm, func(r rune) bool { return r == ' ' || r == '　' })
		// 「長男の妻 由美」「長女の子 翔太」：前の語も続柄なら、その後ろだけを名前に
		if len(parts) == 2 && (isRelHead(parts[0]) || parts[0] == "子") {
			st = ix[4] + strings.Index(nm, parts[1])
			nm, parts = parts[1], parts[1:]
		}
		giv := nm
		if len(parts) == 2 {
			giv = parts[1]
		} else if rs := []rune(nm); len(parts) == 1 && len(rs) >= 3 && isKanaOnly(string(rs[len(rs)-2:])) {
			giv = string(rs[len(rs)-2:]) // 「大川ミツ」
		}
		if m.notNameWord(nm) || m.notNameWord(giv) || !givenLike(giv, true) {
			continue
		}
		// 「長女 宅」「妻 と」などは上で外れる。漢字だけの1字の名は止めない（「母 方」など）
		if utf8.RuneCountInString(normName(nm)) < 2 {
			continue
		}
		add(st, ix[5], "続柄・関係のすぐ後ろ")
	}
	// 3) 名前の記号のすぐ後ろに残った下の名前（「前田」だけ登録して「前田 健太」）
	for _, ix := range reTokGiven.FindAllStringSubmatchIndex(s, -1) {
		tok, giv := s[ix[2]:ix[3]], s[ix[4]:ix[5]]
		if !m.nameTok(tok) || !(strings.HasPrefix(tok, "【姓") || strings.HasPrefix(tok, "【その他") || strings.HasPrefix(tok, "【家族") || strings.HasPrefix(tok, "【利用者")) {
			continue
		}
		if strings.Contains(tok, "の名") || strings.Contains(tok, "呼び名") || strings.Contains(tok, "の読み") {
			continue // 下の名前・呼び名・読みの記号のあとは、名前の続きではない
		}
		if isHiraOnly(giv) || m.notNameWord(giv) || !givenLike(giv, true) {
			continue // ひらがなは「さんと」「です」などの続きのことが多い（点検役）
		}
		// 記号が姓だけのとき・下の名前のない人（「前田」だけ）のときに限る
		orig := m.cb.codeToOrig[tok]
		if strings.ContainsAny(orig, " 　") || utf8.RuneCountInString(normName(orig)) > 3 {
			continue
		}
		add(ix[2], ix[5], "登録した姓のすぐ後ろ")
	}
	// 6) 登録した方のカタカナの名前（トシ）のすぐ後ろにカタカナが続く所（トシエ）：記号にしていないので聞く（改善27の2 試験役 U1）
	for k, ne := range m.lookup {
		if !isKataOnly(ne.lit) || utf8.RuneCountInString(k) < 2 {
			continue
		}
		for off := 0; off < len(s); {
			i := strings.Index(s[off:], ne.lit)
			if i < 0 {
				break
			}
			a, b := off+i, off+i+len(ne.lit)
			off = b
			if !kataNameContinues(s, b, ne.lit) {
				continue
			}
			we := b
			for n := 0; n < 3 && we < len(s); n++ {
				r, sz := utf8.DecodeRuneInString(s[we:])
				if !(unicode.Is(unicode.Katakana, r) || r == 'ー') || r == '・' {
					break
				}
				we += sz
			}
			w := s[a:we]
			if m.exclude[w] || m.inTok(s, a, we) {
				continue
			}
			if _, ok := m.lookup[w]; ok {
				continue // 「トシエ」も登録済み（その方の記号になる）
			}
			out = append(out, suspect{word: w, context: contextAround(s, a, we), reason: "敬称のない名前らしき語（登録した方の名前「" + ne.lit + "」に字が続いた形。別の方の名前かもしれません）"})
		}
	}
	// 5) 登録した方のかなの名前と同じ字が、事業所名・地名の形で書かれている所（記号にはしていない。改善23 D1）：
	//    事業所名・地名なら「除外」1回で、次からは聞かない
	for k, ne := range m.lookup {
		if !isKanaWord(ne.lit) || utf8.RuneCountInString(k) < 2 {
			continue
		}
		for off := 0; off < len(s); {
			i := strings.Index(s[off:], ne.lit)
			if i < 0 {
				break
			}
			a, b := off+i, off+i+len(ne.lit)
			off = b
			if !kanaOrgPlace(s, a, b, ne.lit) {
				continue
			}
			if a > 0 && b < len(s) {
				pr, _ := utf8.DecodeLastRuneInString(s[:a])
				nr, _ := utf8.DecodeRuneInString(s[b:])
				if isKataRune(pr) && isKataRune(nr) && isKataRune([]rune(ne.lit)[0]) {
					continue // 「アセスメントシート」の「トシ」：カタカナの語の途中（前後ともカタカナ）は名前と聞かない（改善27の2 試験役 T2）
				}
			}
			ws, we := a, b
			for n := 0; n < 12 && ws > 0; n++ {
				r, sz := utf8.DecodeLastRuneInString(s[:ws])
				if !(unicode.Is(unicode.Katakana, r) || r == 'ー') {
					break
				}
				ws -= sz
			}
			for n := 0; n < 4 && we < len(s); n++ {
				r, sz := utf8.DecodeRuneInString(s[we:])
				if !(unicode.Is(unicode.Han, r) || unicode.Is(unicode.Katakana, r) || r == 'ー' || ((r == 'の' || r == 'が') && n == 0)) {
					break
				}
				we += sz
			}
			w := s[ws:we]
			skip := false
			for ex := range m.exclude {
				if strings.Contains(ex, ne.lit) && strings.Contains(s[max(0, ws-30):min(len(s), we+30)], ex) {
					skip = true
					break
				}
			}
			if skip || w == ne.lit {
				continue
			}
			out = append(out, suspect{word: w, context: contextAround(s, ws, we), reason: "敬称のない名前らしき語（登録した方の名前「" + ne.lit + "」と同じ字。事業所名・地名なら除外）"})
		}
	}
	// 4) ひらがなの名前＋さん・ちゃん・様（「長女のかずこさん」。試験役 B2）
	for _, ix := range reHiraHonor.FindAllStringSubmatchIndex(s, -1) {
		a, b := ix[2], ix[3]
		w := s[a:b]
		// 前に付いた助詞（の・と・が・は・も・に・を・や・へ）を外す
		for utf8.RuneCountInString(w) > 2 {
			r, sz := utf8.DecodeRuneInString(w)
			if !strings.ContainsRune("のとがはもにをやへ", r) {
				break
			}
			w, a = w[sz:], a+sz
		}
		n := utf8.RuneCountInString(w)
		last, _ := utf8.DecodeLastRuneInString(w)
		if n < 2 || n > 4 || !strings.ContainsRune("こみえよのかゆきりほせつ", last) || hiraNotName[w] || m.notNameWord(w) {
			continue
		}
		if a > 0 {
			if r, _ := utf8.DecodeLastRuneInString(s[:a]); unicode.Is(unicode.Hiragana, r) && !strings.ContainsRune("のとがはもにをやへ", r) {
				continue // ひらがなの文の途中
			}
		}
		if ix[1] < len(s) {
			if r, _ := utf8.DecodeRuneInString(s[ix[1]:]); compoundSkip[s[ix[3]:ix[1]]+string(r)] || (strings.HasSuffix(s[:ix[1]], "様") && strings.ContainsRune("子式相", r)) {
				continue // 「様子」「様式」など語の一部
			}
		}
		out = append(out, suspect{word: s[a:ix[1]], context: contextAround(s, a, ix[1]), reason: "敬称の付いた名前らしき語（ひらがなの名前）"})
	}
	return out
}

var reHiraHonor = regexp.MustCompile(`([\p{Hiragana}]{2,7})(?:さん|ちゃん|様|さま|くん)`)

// ひらがなで書く、名前ではない語（＋さん）
var hiraNotName = map[string]bool{"みな": true, "みなみ": true, "おかみ": true, "おくさ": true, "おじょ": true, "むすこ": true, "おむこ": true, "およめ": true, "おまわり": true, "あかちゃ": true, "おにいちゃ": true, "おねえちゃ": true, "いとこ": true, "おとこ": true, "おんなのこ": true, "まいご": true, "ようこそ": true, "すこ": true, "そこ": true, "ここ": true, "どこ": true, "あそこ": true, "だれか": true, "どなたか": true, "おいしゃ": true, "かんごし": true, "おとなり": true, "おむかい": true, "おうち": true, "ごきんじょ": true, "おつき": true, "ねえ": true, "にい": true, "ばあ": true, "じい": true, "かあ": true, "とう": true, "おねえ": true, "おにい": true, "おばあ": true, "おじい": true, "おかあ": true, "おとう": true, "ねえね": true}

// 介護の書類によく出るカタカナの語（名前ではない）
var kataCommon = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`ケア デイ ショート ベッド リハ リハビリ ヘルパー ケアマネ トイレ ポータブル サロン メール タクシー パート バス バイク カート ゴミ ゴミ出し スーパー コープ ケース メモ ノート ファイル テレビ ラジオ ゲーム カラオケ ペット イヌ ネコ ベスト パジャマ オムツ パッド シーツ マット ポット コップ ストロー スプーン ミキサー ゼリー トロミ プリン パン ジュース コーヒー タバコ ガス バイタル サイン ペース リズム レク ミス ナース ドクター カルテ メンバー チーム グループ ホーム センター ルーム ハウス ステイ サービス プラン チェック シール ラベル ボタン キー カギ ドア マスク`) {
		m[w] = true
	}
	return m
}()

// kanjiNumWord: 漢数字＋代・歳 など（「七十代」「八十歳」）
func kanjiNumWord(n string) bool {
	rs := []rune(n)
	if len(rs) < 2 || !strings.ContainsRune("代歳才年回人名階月日時分割度", rs[len(rs)-1]) {
		return false
	}
	for _, r := range rs[:len(rs)-1] {
		if !strings.ContainsRune("一二三四五六七八九十百千〇", r) {
			return false
		}
	}
	return true
}

// 敬称・人を指す語だけ（名前ではない）
var honorOnly = map[string]bool{"さん": true, "さま": true, "ちゃん": true, "くん": true, "君": true, "様": true, "殿": true, "氏": true, "先生": true, "たち": true, "達": true, "ら": true, "宅": true, "方": true, "さんち": true, "さん宅": true}

func isRelHead(w string) bool {
	for _, r := range relHeadWords {
		if w == r {
			return true
		}
	}
	return relationWord(w)
}

// dropExcluded: 語（敬称を外し、空白を外した形）が「除外」に登録済みの要確認は出さない。
// 除外したのに同じ所でまた止まって渡せなくなるのを防ぐ（改善23 点検役）
func (m *masker) dropExcluded(sus []suspect) []suspect {
	out := sus[:0]
	for _, x := range sus {
		w := normName(reHonorTail.ReplaceAllString(m.unmask(x.word), ""))
		if w != "" && !strings.Contains(w, "【") && (m.exclude[w] || m.excludeOne[w]) {
			continue
		}
		out = append(out, x)
	}
	return out
}

func isHiraOnly(w string) bool {
	for _, r := range w {
		if !unicode.Is(unicode.Hiragana, r) {
			return false
		}
	}
	return w != ""
}
