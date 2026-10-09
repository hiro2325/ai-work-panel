package main

// 伏せ字の名前を楽にする（改善22・R8.10.5 注文）
//   介護の書類は、事業所が「山〇太〇」「山O太O」のように名前の一部を隠してFAXで届くことが多い。
//   - ②の橙の箱で、だれの伏せ字かを「候補の方のボタン1回」で選べるようにする（候補は氏名一覧の字と合う方）。
//   - 選ぶと、その方の「呼び名」に伏せ字の書き方を足す（次からは自動で記号になる）。
//   - 〇・○・◯・●・O・0・＊・× などは、どれで書かれていても同じ伏せ字として置き換える。
//   - 名前の記号のすぐ前後に字が残ったとき（「長【…の名】」）も、その方の呼び名に足すボタンを出す。

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 伏せ字に使われる字（読み取りで入れ替わることがあるものは同じに扱う）
const fuseChars = "〇○◯●OＯ0０＊*×✕?？□■"

func isFuse(r rune) bool { return strings.ContainsRune(fuseChars, r) }

func hasFuseAndHan(w string) bool {
	f, h := false, false
	for _, r := range w {
		if isFuse(r) {
			f = true
		} else if unicode.Is(unicode.Han, r) || r == '々' {
			h = true
		}
	}
	return f && h
}

// fusePattern: 伏せ字の字の所を、どの伏せ字の字でも合う形にした正規表現（姓と名の間などの空白は、あってもなくても）
func fusePattern(lit string) string {
	cls := "[" + regexp.QuoteMeta(fuseChars) + "]"
	var parts []string
	for _, r := range strings.ReplaceAll(strings.TrimSpace(lit), "　", " ") {
		switch {
		case r == ' ':
			continue
		case isFuse(r):
			parts = append(parts, cls)
		default:
			parts = append(parts, regexp.QuoteMeta(string(r)))
		}
	}
	return strings.Join(parts, `[ 　]?`)
}

// fuseKey: 伏せ字の字を「〇」にそろえた鍵（同じ伏せ字の箱を1つにまとめる）
func fuseKey(w string) string {
	return strings.Map(func(r rune) rune {
		if isFuse(r) {
			return '〇'
		}
		return r
	}, normName(w))
}

// fuseFits: 伏せ字の語 w が、名前 t と同じ長さで、伏せ字でない字がすべて同じか（伏せ字でない漢字が1字以上）
func fuseFits(w, t string) bool {
	a, b := []rune(normName(w)), []rune(normName(t))
	if len(a) != len(b) || len(a) < 2 {
		return false
	}
	same := 0
	for i := range a {
		if isFuse(a[i]) {
			continue
		}
		if a[i] != b[i] {
			return false
		}
		same++
	}
	return same > 0 && same < len(a)
}

// fuseOwners: 伏せ字の語に合う方（利用者・家族）。氏名全体が合う方を先に
func fuseOwners(w string, fam []nameRow) []nameRow {
	type hit struct {
		r    nameRow
		full bool
	}
	var hs []hit
	for _, r := range fam {
		full := fuseFits(w, r.Name)
		ok := full
		if !ok {
			if f := strings.FieldsFunc(r.Name, func(c rune) bool { return c == ' ' || c == '　' }); len(f) == 2 {
				ok = fuseFits(w, f[0]) || fuseFits(w, f[1])
			}
		}
		if !ok {
			for _, a := range aliasList(r.Alias) {
				if fuseFits(w, a) {
					ok = true
					break
				}
			}
		}
		if !ok && utf8.RuneCountInString(normName(w)) >= 3 {
			// 前か後ろが切れて読めた伏せ字（「三0延」＝三武延江の前3字）も候補に
			nr := []rune(normName(r.Name))
			k := utf8.RuneCountInString(normName(w))
			if len(nr) > k && (fuseFits(w, string(nr[:k])) || fuseFits(w, string(nr[len(nr)-k:]))) {
				ok = true
			}
		}
		if ok {
			hs = append(hs, hit{r, full})
		}
	}
	sort.SliceStable(hs, func(i, j int) bool { return hs[i].full && !hs[j].full })
	out := []nameRow{}
	for _, h := range hs {
		out = append(out, h.r)
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// tokenOwners: 記号（【利用者001の名】【姓003】など）の元の字を持つ方（利用者・家族）。
// 同じ名・同じ姓の方が何人かいると、記号は先に登録した方のものになるので、記号の番号ではなく元の字で探す。
// 足す書き方（fix）と字の重なりが多い方を先に。
func tokenOwners(tok, fix string, cb *codeBook, fam []nameRow) []nameRow {
	orig := normName(cb.codeToOrig[tok])
	out := []nameRow{}
	if orig == "" {
		return out
	}
	type hit struct {
		r     nameRow
		score int
	}
	var hs []hit
	for _, r := range fam {
		ok := normName(r.Name) == orig
		if f := strings.FieldsFunc(r.Name, func(c rune) bool { return c == ' ' || c == '　' }); len(f) == 2 && (f[0] == orig || f[1] == orig) {
			ok = true
		}
		for _, a := range aliasList(r.Alias) {
			if normName(a) == orig {
				ok = true
			}
		}
		if !ok {
			continue
		}
		sc := 0
		for _, c := range normName(r.Name) {
			if strings.ContainsRune(fix, c) {
				sc++
			}
		}
		hs = append(hs, hit{r, sc})
	}
	sort.SliceStable(hs, func(i, j int) bool { return hs[i].score > hs[j].score })
	// 足す書き方が、氏名全体と伏せ字・化けた字を除いて合う方が1人だけなら、その方だけ（「三武延0」＝三武 延江。三武 和子は出さない）
	var full []nameRow
	for _, h := range hs {
		if fuseFits(fix, h.r.Name) || normName(fix) == normName(h.r.Name) {
			full = append(full, h.r)
		}
	}
	if len(full) == 1 {
		return full
	}
	for _, h := range hs {
		out = append(out, h.r)
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// unmaskWith: 語の中の記号を、元の字に戻す（画面に出す・呼び名に足すため。この画面の中だけ）
func unmaskWith(w string, cb *codeBook) string {
	ms, _ := restoreMatches(w, cb)
	return applyMatches(w, ms)
}

// fuseFix: 呼び名に足す書き方（敬称・空白の端を除く）。2字以上・60字まで・区切りの字なし
func fuseFix(w string) string {
	w = strings.TrimSpace(reHonorTail.ReplaceAllString(strings.TrimSpace(w), ""))
	if utf8.RuneCountInString(normName(w)) < 2 || utf8.RuneCountInString(w) > 60 || strings.ContainsAny(w, "\t\r\n【】/／、,，") {
		return ""
	}
	return w
}

// fuseInfo: ②の橙の箱に、だれの名前か（候補）と、呼び名に足す書き方を付ける
func fuseInfo(sj *susJSON, su suspect, cb *codeBook, fam []nameRow) {
	if sj.Known != "" {
		return
	}
	if sj.Place != "" {
		// 事業所名・地名の形の中のかなの名前：人の名前だったときに足す先（名・呼び名にその字がある方）
		sj.Fix = fuseFix(sj.Word)
		for _, r := range fam {
			hit := false
			if f := strings.FieldsFunc(r.Name, func(c rune) bool { return c == ' ' || c == '　' }); len(f) == 2 && f[1] == sj.Place {
				hit = true
			}
			for _, a := range aliasList(r.Alias) {
				for _, p := range strings.FieldsFunc(a, func(c rune) bool { return c == ' ' || c == '　' }) {
					if p == sj.Place || hiraToKata(p) == hiraToKata(sj.Place) {
						hit = true
					}
				}
			}
			if hit && len(sj.Owners) < 8 {
				sj.Owners = append(sj.Owners, r)
			}
		}
		return
	}
	if su.tok != "" {
		sj.Part = true
		sj.Fix = fuseFix(unmaskWith(sj.Word, cb))
		if sj.Fix != "" {
			sj.Owners = tokenOwners(su.tok, sj.Fix, cb, fam)
		}
		return
	}
	if sj.Kind == "name" && sj.Garbled {
		w := sj.Name
		if sj.Orig != "" {
			w = sj.Orig // 書類に書かれているとおりの字で（スペースを入れる前）
		}
		sj.Fix = fuseFix(w)
		if sj.Fix != "" {
			sj.Owners = fuseOwners(sj.Fix, fam)
			if len(sj.Owners) == 0 {
				sj.Owners = nearOwners(sj.Fix, fam) // 1字だけ違う方（読み取りで別の字に化けた）
			}
		}
	}
}

// nearOwners: 字の数が同じで、1字だけ違う方（利用者・家族。氏名・呼び名）
func nearOwners(w string, fam []nameRow) []nameRow {
	a := []rune(fuseKey(w))
	near := func(t string) bool {
		b := []rune(normName(t))
		if len(a) != len(b) || len(a) < 3 {
			return false
		}
		d := 0
		for i := range a {
			if a[i] != b[i] && !isFuse(a[i]) {
				d++
			}
		}
		return d <= 1
	}
	out := []nameRow{}
	for _, r := range fam {
		ok := near(r.Name)
		for _, al := range aliasList(r.Alias) {
			ok = ok || near(al)
		}
		if ok {
			out = append(out, r)
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// kanaRunsOn: かなの読みのすぐ後ろが、ひらがなの語の続き（「まことに」「いしいし」「うえだけ」）か。
// 後ろが敬称・助詞（は・が・と・の・も・へ・や・より・から・で）なら名前の終わりとみなす（改善23 試験役 C2）
func kanaRunsOn(s string, end int) bool {
	if end >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[end:])
	if !unicode.Is(unicode.Hiragana, r) {
		return false
	}
	rest := s[end:]
	// 名前のあとに「に」が付く形（「まことに連絡」）は漏らさないため記号にする。ただし決まった言い回し（まことに申し訳ない）は外す
	if strings.HasSuffix(s[:end], "まこと") && strings.HasPrefix(rest, "に") {
		return true
	}
	for _, p := range []string{"さん", "さま", "ちゃん", "くん", "どの", "は", "が", "と", "の", "も", "へ", "や", "より", "から", "で", "たち", "ら", "に", "を", "ね", "よ", "か", "な", "だけ", "って", "まで", "しか", "ばかり", "なら", "こそ", "さえ", "くらい", "ぐらい", "など", "だ", "です", "でし", "じゃ"} {
		if strings.HasPrefix(rest, p) {
			return false
		}
	}
	return true
}

// kanaOrgPlace: かなだけの名前（さくら・ひかり・みどり）が、事業所名・地名の一部として書かれているか。
//   ・すぐ前がカタカナ（ステーション・デイサービス・ホーム など）
//   ・すぐ後ろが、事業所・地名によく付く字や語（草・荘・苑・園・台・丘・の家・の里・が丘・が咲 …）
// 名前として書かれた形（妻さくら・さくら様・さくらさん・さくらは）は、これまでどおり記号にする。
func kanaOrgPlace(s string, a, b int, lit string) bool {
	if !isKanaWord(lit) {
		return false
	}
	for _, h := range []string{"さん", "さま", "様", "ちゃん", "くん", "君", "殿", "氏"} {
		if strings.HasPrefix(s[b:], h) {
			return false // 敬称が付けば名前
		}
	}
	if a > 0 {
		if r, _ := utf8.DecodeLastRuneInString(s[:a]); unicode.Is(unicode.Katakana, r) || r == 'ー' {
			for _, j := range []string{"ヘルパー", "ケアマネ", "ケアマネジャー", "ナース", "ドクター", "スタッフ", "セラピスト", "リハ", "サ責", "ワーカー"} {
				if strings.HasSuffix(s[:a], j) {
					return false // 「ヘルパーさくら」は人（これまでどおり記号に）
				}
			}
			return true
		}
	}
	rest := s[b:]
	for _, w := range []string{"が丘", "が咲", "が差", "が射", "が光", "ヶ丘", "ケ丘", "通り", "公園", "ホーム", "ハウス", "クリニック", "薬局", "保育", "幼稚園", "学園"} {
		if strings.HasPrefix(rest, w) {
			return true
		}
	}
	if r, _ := utf8.DecodeRuneInString(rest); r != utf8.RuneError && strings.ContainsRune("草荘苑園台丘坂谷里郷森館堂店院社寮橋町村区市野原台寺", r) {
		return true
	}
	return false
}

var reBirthTail = regexp.MustCompile(`[ 　]*(女性|男性|女|男|生まれ|生)$`)

// birthInfo: 生年月日の欄の崩れた日付（「昭和31年10月日 女性」）。足す書き方は性別などを外した日付、候補はこのファイルの利用者（改善26）。
// 年・月・日のうち2つ以上が書かれた形だけを呼び名に足せるようにする（「昭和31年」だけだと、ほかの方の文の同じ字まで記号になるため。点検役）。
// それ以外（「確認中」など）は「生年月日ではない（通す）」だけを出す（試験役 L2）
func birthInfo(sj *susJSON, fam []nameRow, users []string) {
	w := strings.TrimSpace(reBirthTail.ReplaceAllString(strings.TrimSpace(sj.Word), ""))
	if w == "" || utf8.RuneCountInString(w) > 40 || strings.ContainsAny(w, "\t\r\n【】、,，") { // 「/」入りの日付も（改善27。足す先は氏名一覧の「その他」）
		return
	}
	sj.Birth, sj.Ex = true, w
	n := 0
	for _, c := range []string{"年", "月", "日"} {
		if strings.Contains(w, c) {
			n++
		}
	}
	if birthFull(w) != w && (n < 2 && !reDotDate.MatchString(w) || !strings.ContainsAny(w, "0123456789０１２３４５６７８９") || !reEraOrYear.MatchString(w)) {
		if strings.ContainsAny(w, "0123456789０１２３４５６７８９") {
			sj.Part = true // 日付の一部（年だけ・元号のない年月など）：呼び名には足さない（ほかの方の文の同じ字まで記号になるため）
		}
		return
	}
	sj.Fix = w
	in := map[string]bool{}
	for _, u := range users {
		in[u] = true
	}
	for _, r := range fam {
		if r.Kind == "利用者" && in[r.Code] && len(sj.Owners) < 8 {
			sj.Owners = append(sj.Owners, r)
		}
	}
}

var reDotDate = regexp.MustCompile(`[0-9０-９]{1,4}\s*[./／．]\s*[0-9０-９]{1,2}\s*[./／．]`)

// nameTok: 名前の記号か。呼び名に足した生年月日など、元の字に数字のある記号は名前ではないので、
// すぐ前後の字（「生年月日」「年月日」「女」）を名前の一部かと聞かない（改善26 試験役 L1）
func (m *masker) nameTok(tok string) bool {
	if !m.cb.nameCodes[tok] {
		return false
	}
	o := m.cb.codeToOrig[tok]
	// 生年月日の形（数字と 年・月 か 31.10. の形）だけを名前でないとみなす。伏せ字を 0 と読んだ名前（原0フ0）は名前のまま（点検役）
	return !(strings.ContainsAny(o, "0123456789０１２３４５６７８９") && (strings.ContainsAny(o, "年月") || reDotDate.MatchString(o)))
}

// 元号（漢字・頭文字）か西暦4けたのある日付だけを、呼び名に足せる生年月日とする（「31年10月」だけだと、ほかの方の「平成31年10月」に当たる。点検役）
var reEraOrYear = regexp.MustCompile(`明治|大正|昭和|平成|令和|明|大|昭|平|令|[MTSHRmtshr][ 　]*[0-9０-９]|[0-9０-９]{4}`)

// readingInfo: フリガナの欄の名前の読み（「サキサチコ」）。このファイルの利用者を候補にし、ボタン1回でその方の呼び名に足す（改善27）
func readingInfo(sj *susJSON, fam []nameRow, users []string) {
	w := strings.TrimSpace(sj.Word)
	if w == "" || utf8.RuneCountInString(w) > 30 || strings.ContainsAny(w, "\t\r\n【】/／、,，") {
		return
	}
	sj.Read, sj.Fix, sj.Ex = true, w, w
	in := map[string]bool{}
	for _, u := range users {
		in[u] = true
	}
	for _, r := range fam {
		if r.Kind == "利用者" && in[r.Code] && len(sj.Owners) < 8 {
			sj.Owners = append(sj.Owners, r)
		}
	}
}
