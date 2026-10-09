package main

// 氏名一覧に登録した生年月日（呼び名など）を、読み取り（PDF・FAX）の書き方の違いがあっても見つける（改善12 No.36）
//
// 困っていたこと：呼び名に「昭和21年5月3日」と登録しても、読み取りの文が
// 「Ｓ21.5.3」「昭和２１年　５月　３日」（全角の空白）「昭21年5月3日」「昭和21-5-3」などのときは
// 登録した書き方と字が違うため見つからず、日付の形の決まり（reOldDate）にも当てはまらずに残っていた。
// 直し方：登録した語が生年月日の形なら、同じ年・月・日の書き方の違い（元号の略し方・全角半角・空白・区切り・西暦）を
// まとめて見つける。見つけたところは、登録した呼び名と同じ記号にする（置き換えを増やすだけで、減らさない）。

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type dateAlias struct {
	re   *regexp.Regexp
	code string
	kind string
}

var (
	reAliasEra  = regexp.MustCompile(`^(明治|大正|昭和|平成|令和|明|大|昭|平|令|[MTSHR])\.?(\d{1,2})[年./\-／](\d{1,2})[月./\-／](\d{1,2})日?(?:生|生まれ)?$`)
	reAliasWest = regexp.MustCompile(`^(\d{4})[年./\-／](\d{1,2})[月./\-／](\d{1,2})日?(?:生|生まれ)?$`)
)

var eraBase = map[string]int{"明治": 1867, "大正": 1911, "昭和": 1925, "平成": 1988, "令和": 2018}
var eraAbbr = map[string]string{"明": "明治", "大": "大正", "昭": "昭和", "平": "平成", "令": "令和", "M": "明治", "T": "大正", "S": "昭和", "H": "平成", "R": "令和"}
var eraLetter = map[string]string{"明治": "MＭｍm", "大正": "TＴｔt", "昭和": "SＳｓs", "平成": "HＨｈh", "令和": "RＲｒr"}

// halfDigitsLetters: 全角の数字・英字を半角にし、空白を取る（登録した語の形を調べるためだけに使う）
func halfDigitsLetters(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '０' && r <= '９':
			b.WriteRune(r - '０' + '0')
		case r >= 'Ａ' && r <= 'Ｚ':
			b.WriteRune(r - 'Ａ' + 'A')
		case r >= 'ａ' && r <= 'ｚ':
			b.WriteRune(r - 'ａ' + 'A')
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r == ' ' || r == '　':
		case r == '．':
			b.WriteRune('.')
		case r == '－' || r == '‐' || r == '−' || r == 'ー':
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// numPat: 数字 n を、全角・半角・前の0あり・字の間の空白ありで見つける形
func numPat(n int) string {
	var b strings.Builder
	b.WriteString(`[0０]?`)
	for i, c := range strconv.Itoa(n) {
		if i > 0 {
			b.WriteString(`[ 　]?`)
		}
		b.WriteString(fmt.Sprintf(`[%c%c]`, c, c-'0'+'０'))
	}
	return b.String()
}

// parseDateAlias: 登録した語が生年月日の形なら、元号・年（元号の年）・西暦・月・日を返す
func parseDateAlias(a string) (era string, ey, wy, mo, d int, ok bool) {
	n := halfDigitsLetters(a)
	if m := reAliasEra.FindStringSubmatch(n); m != nil {
		era = m[1]
		if full, x := eraAbbr[era]; x {
			era = full
		}
		ey, _ = strconv.Atoi(m[2])
		mo, _ = strconv.Atoi(m[3])
		d, _ = strconv.Atoi(m[4])
		wy = eraBase[era] + ey
	} else if m := reAliasWest.FindStringSubmatch(n); m != nil {
		wy, _ = strconv.Atoi(m[1])
		mo, _ = strconv.Atoi(m[2])
		d, _ = strconv.Atoi(m[3])
		for _, e := range []string{"令和", "平成", "昭和", "大正", "明治"} {
			if wy > eraBase[e] {
				era, ey = e, wy-eraBase[e]
				break
			}
		}
	} else {
		return "", 0, 0, 0, 0, false
	}
	if mo < 1 || mo > 12 || d < 1 || d > 31 || wy < 1868 || ey < 1 {
		return "", 0, 0, 0, 0, false
	}
	return era, ey, wy, mo, d, true
}

// dateAliasPattern: 同じ年月日の書き方の違いをまとめて見つける正規表現
func dateAliasPattern(era string, ey, wy, mo, d int) string {
	sp := `[ 　]*`
	eraAlt := []string{regexp.QuoteMeta(era), string([]rune(era)[0])}
	for _, r := range eraLetter[era] {
		eraAlt = append(eraAlt, regexp.QuoteMeta(string(r)))
	}
	// 元号（略した形も）＋年、または西暦の年。元号は読み取りで落ちることがあるので、なくてもよい
	year := `(?:(?:` + strings.Join(eraAlt, "|") + `)` + sp + `[.．]?` + sp + numPat(ey) + `|` + numPat(wy) + `|` + numPat(ey) + `)`
	sepY := sp + `[年.．/／\-－‐・]` + sp
	sepM := sp + `[月.．/／\-－‐・]` + sp
	return year + sepY + numPat(mo) + sepM + numPat(d) + `(?:` + sp + `日)?`
}

// addDateAlias: 生年月日の形の登録語（呼び名・氏名）を、書き方の違いでも見つけられるようにする
func (m *masker) addDateAlias(word, code, kind string) {
	era, ey, wy, mo, d, ok := parseDateAlias(word)
	if !ok {
		return
	}
	re, err := regexp.Compile(dateAliasPattern(era, ey, wy, mo, d))
	if err != nil {
		return
	}
	m.dateAliases = append(m.dateAliases, dateAlias{re: re, code: code, kind: kind})
}

// dateAliasMatches: 登録した生年月日の書き方の違い（前後が数字のものは別の数字なので外す）
func (m *masker) dateAliasMatches(s string) []match {
	var out []match
	for _, da := range m.dateAliases {
		for _, ix := range da.re.FindAllStringIndex(s, -1) {
			a, b := ix[0], ix[1]
			for a < b && (s[a] == ' ') {
				a++
			}
			if !boundaryOK(s, a, b) {
				continue
			}
			out = append(out, match{start: a, end: b, repl: da.code, kind: da.kind, orig: s[a:b]})
		}
	}
	return out
}
