package main

// 置き換えの見逃し対策（R8.9.27）：記号にはせず「保留（要確認・名前）」にするだけの規則。
//  E1 読み取りで字が化けた名前＋敬称（例：海0一0様、海?一郎さん）
//  E2 担当者・顧客名などのラベルの後ろに続く、氏名一覧にない名前らしき語（例：利用者担当者:相模良夫）
//  E3 日付（＋曜日）の直後の名前らしき語＋業務語（例：R08 / 09 / 03 (木)座間貴　訪問）
// どれも置き換え後の文に対して調べるので、氏名一覧・除外に登録済みの語は出てこない。

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 読み取りで化けやすい字（漢字の名前に混ざっていたら「化けた名前」とみなす）
func isGarble(r rune) bool {
	switch {
	case r >= '0' && r <= '9', r >= '０' && r <= '９':
		return true
	case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= 'Ａ' && r <= 'Ｚ', r >= 'ａ' && r <= 'ｚ':
		return true
	}
	return strings.ContainsRune("〇○◯□■＊*?？ー－", r)
}

func isNameHan(r rune) bool {
	return (unicode.Is(unicode.Han, r) || r == '々' || r == 'ヶ') && r != '〇'
}

// 名前の中にあってよい字（漢字＋化けやすい字）
func isNameOrGarble(r rune) bool { return isNameHan(r) || isGarble(r) || r == '一' }

// hasGarble: 漢字（一・〇 以外）と化けやすい字（一 は除く）が混ざっているか
func hasGarble(w string) bool {
	han, gar := false, false
	for _, r := range w {
		if isGarble(r) {
			gar = true
		} else if isNameHan(r) && r != '一' {
			han = true
		}
	}
	return han && gar
}

// 化けた名前の説明（CLIの確認リストにも出る）
const garbleNote = "読み取りで字が化けた名前かもしれません。正しい字に直してから氏名一覧に登録し、化けた書き方はその方の「呼び名」に書き足してください"

// isAddrLike: 住所の形か（字を1つ含むかではなく並びで見る。西村・中村・町田・市川 などの姓は住所にしない）
//
//	（記号のすぐ後ろが 市区町村郡都道府県 で始まるとき（「【姓001】市勝瀬1」＝大和市勝瀬1）は、呼ぶ側で住所とみなす）
//	・都道府県名＋都道府県、「〜市〜町／村」「〜区〜町」の並び
//	・番地の形（1-2-3、3丁目、9番地、5号）
//	・市区郡都道府県 で終わる語、3字以上で 町・村 で終わる語（大和町・清川村）
var (
	reAddrNum  = regexp.MustCompile(`[0-9０-９一二三四五六七八九十]+\s*(?:丁目|番地|番|号)|[0-9０-９]+\s*[-－ー‐−の]\s*[0-9０-９]+`)
	reAddrSeq  = regexp.MustCompile(`[\p{Han}ヶケ]{1,4}[市区郡][\p{Han}ヶケ]{1,5}[町村]|[\p{Han}]{2,3}[都道府県][\p{Han}ヶケ]{1,4}[市区町村郡]`)
	reAddrPref = regexp.MustCompile(`(?:北海道|東京都|大阪府|京都府|神奈川県|[\p{Han}]{2,3}県)`)
)

// startsPlace: 記号（地名と同じ字の姓など）のすぐ後ろに続く語が、市区町村などで始まるか
func startsPlace(w string) bool {
	r, _ := utf8.DecodeRuneInString(w)
	return strings.ContainsRune("市区町村郡都道府県", r)
}

func isAddrLike(w string) bool {
	nw := normName(w)
	if nw == "" {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(nw)
	if reAddrNum.MatchString(nw) || reAddrSeq.MatchString(nw) || reAddrPref.MatchString(nw) {
		return true
	}
	if strings.ContainsRune("市区郡都道府県", last) {
		return true
	}
	if strings.ContainsRune("町村", last) && utf8.RuneCountInString(nw) >= 3 {
		return true
	}
	return false
}

// notNameEnd から地名の字（区市町村県都府道・地）を除き、職名の字を足したもの
var ocrNotNameEnd = strings.Map(func(r rune) rune {
	if strings.ContainsRune("区市町村県都府道地", r) {
		return -1
	}
	return r
}, notNameEnd) + "員師長課係科部社"

var counterAfterDigit = "名人件回歳才階号番日月年時分秒円個枚本台部室週度割倍点冊通箇ヶ条巻"

// 数字＋助数詞（「2名様」「3階」など）を含む語は名前とみなさない
func hasCounter(w string) bool {
	rs := []rune(w)
	for i := 0; i+1 < len(rs); i++ {
		if (rs[i] >= '0' && rs[i] <= '9' || rs[i] >= '０' && rs[i] <= '９') && strings.ContainsRune(counterAfterDigit, rs[i+1]) {
			return true
		}
	}
	return false
}

var reHonorAny = regexp.MustCompile(`(様|さま|さん|氏|殿|先生|医師|ちゃん|くん|君)`)

// garbleHonorSuspects: E1 化けた字の混ざった名前＋敬称
func (m *masker) garbleHonorSuspects(s string) []suspect {
	var out []suspect
	toks := ourTokens(s, m.cb)
	for _, ix := range reHonorAny.FindAllStringIndex(s, -1) {
		hon := s[ix[0]:ix[1]]
		if ix[1] < len(s) {
			r, _ := utf8.DecodeRuneInString(s[ix[1]:])
			if compoundSkip[hon+string(r)] {
				continue
			}
			if hon == "氏" && unicode.Is(unicode.Han, r) && !strings.ContainsRune("宅方邸宛家", r) {
				continue
			}
		}
		if m.honorInCommonWord(s, ix[0], ix[1]) {
			continue // 模様・同様 など（改善4）
		}
		// 敬称の前（空白1つまで）に続く、漢字・化けやすい字の並び（最大8字）
		end := ix[0]
		if end > 0 {
			if r, sz := utf8.DecodeLastRuneInString(s[:end]); r == ' ' || r == '　' {
				end -= sz
			}
		}
		st := end
		for n := 0; n < 8 && st > 0; n++ {
			r, sz := utf8.DecodeLastRuneInString(s[:st])
			if !isNameOrGarble(r) && !isKataRune(r) { // カタカナの名（原〇フ〇）も続けて読む（改善23）
				break
			}
			st -= sz
		}
		// 前に付いたカタカナ（「ホーム原〇」の「ホーム」）は外す。名前は漢字・化けやすい字から始まる
		for st < end {
			r, sz := utf8.DecodeRuneInString(s[st:end])
			if !isKataRune(r) || isGarble(r) && r != 'ー' {
				break
			}
			st += sz
		}
		w := s[st:end]
		// 「山田ケアマネジャー様」：職種の語の前の名前を、欠けずに聞く（8字の上限で「田ケアマネジャー」と1字欠けていた。改善27 試験役 Q2）
		if jw := jobSuffix(w); jw != "" {
			ne := end - len(jw)
			ns := ne
			for n := 0; n < 5 && ns > 0; n++ {
				r, sz := utf8.DecodeLastRuneInString(s[:ns])
				if !isNameHan(r) && r != '々' {
					break
				}
				ns -= sz
			}
			for hw := range jobHeadWords { // 「佐藤主任ケアマネジャー」→「佐藤」
				if t := s[ns:ne]; strings.HasSuffix(t, hw) && utf8.RuneCountInString(t) > utf8.RuneCountInString(hw)+1 {
					ne -= len(hw)
					break
				}
			}
			if nm := s[ns:ne]; utf8.RuneCountInString(nm) >= 2 {
				if !m.inTok(s, ns, ne) && !m.exclude[nm] && !stopWords[nm] && !notPersonBox(nm) && !isCommonWord(nm) && !ocrNotName[nm] && !jobHeadWords[nm] {
					out = append(out, suspect{word: nm, context: contextAround(s, ns, ix[1]), reason: "氏名欄（職種の前）に、氏名一覧に登録されていない名前らしき文字があります"})
				}
				continue
			}
			// 1字の名字（「森ケアマネジャー様」）は、今までどおり下の決まりで語ごと聞く（点検役）
		}
		if utf8.RuneCountInString(w) < 2 {
			continue
		}
		afterTok := false
		tokStart := st
		for _, t := range toks {
			if t[1] == st {
				afterTok = true // 「【姓001】一0様」：名簿の姓の後ろに化けた名が残っている
				tokStart = t[0]
			}
		}
		first, _ := utf8.DecodeRuneInString(w)
		if !afterTok && !(isNameHan(first) && first != '一') && !strings.ContainsRune("〇○◯□■＊*?？", first) {
			continue // 「2名様」「A様」など：漢字（または化けやすい記号）で始まらない
		}
		if afterTok {
			if !strings.ContainsFunc(w, isGarble) {
				continue
			}
		} else if !hasGarble(w) {
			continue
		}
		if hasCounter(w) || isAddrLike(w) || (afterTok && startsPlace(w)) || m.exclude[w] || m.exclude[normName(w)] || m.endsWithExcluded(s[:end]) {
			continue
		}
		if !afterTok && (notPersonBox(w) || addresseeTitle(w)) { // あて名の職種だけ（改善27。notPersonBox に入れると「森ケアマネジャー様」の「ケアマネジャー」だけが見られて名前を見逃す：点検役）
			continue // 「各事業所様」「長女〇〇様」など（改善23）
		}
		out = append(out, suspect{word: s[tokStart:ix[1]], context: contextAround(s, tokStart, ix[1]), reason: "敬称の付いた名前らしき語（" + garbleNote + "）"})
	}
	return out
}

// E2 のラベル（長いものから）
var ocrLabels = []string{
	"利用者担当者", "ご利用者名", "ご利用者様名", "利用者氏名", "担当者氏名", "担当者名", "担当職員", "担当ケアマネ",
	"顧客氏名", "顧客名", "お客様名", "利用者名", "ご利用者", "お客様", "面会者", "記入者", "記録者", "報告者", "作成者",
	"訪問者", "対応者", "受付者", "相談者", "連絡者", "確認者", "実施者", "説明者", "依頼者", "申込者",
	"担当者", "担当", "氏名", "名前",
	"看護師", // 改善C No.40 ③（病院の看護師名）
}

// E2・E3 で名前とみなさない一般語
var ocrNotName = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`会議 担当者会議 ケアマネ 事業所 様式 欄 同上 記入 未定 なし 無し 本人 家族 ご主人様 ご主人 奥様 長男 長女 次男 次女 主人 妻 夫 息子 娘
		不在 未記入 空欄 全員 各位 管理者 担当者 相談員 職員 介護支援専門員 専門員 看護師 主治医 医師 包括 地域包括 市役所 役所 区役所 市 町 村
		定期 臨時 緊急 初回 再度 再 自宅 居宅 事務所 病院 施設 通所 訪問 電話 来所 連絡 点検 納品 搬入 回収 面談 面接 同行 同席 合同 予定 本日 当日 翌日 前日
		午前 午後 夕方 早朝 夜間 定時 定例 月例 通常 実施 使用 利用 状況 状態 確認 説明 契約 解約 請求 集金 設置 交換 修理 調整 相談 受診 通院 入院 退院
		株式会社 合同会社 有限会社 合資会社 合名会社 社会福祉法人 医療法人 社団法人 財団法人 一般社団法人 特定非営利活動法人 法人 会社 協会 組合 協同組合 本社 支社 支店 営業所
		担当 以下 以上 同じ 不明 別紙 参照 記載 署名 押印 印 変更 追加 削除 未 済 有 無 可 否 他 等 及び 又は 様 殿 御中`) {
		ocrNotName[w] = true
	}
}

// nameAfterLabel: s[i:] の先頭から、名前らしき字の並び（漢字・化けやすい字、途中に空白1つまで）を取り出す
func nameRunAt(s string, i int) (int, int) {
	st := i
	j := i
	spaceUsed := false
	n := 0
	for j < len(s) {
		r, sz := utf8.DecodeRuneInString(s[j:])
		if isNameOrGarble(r) {
			j += sz
			n++
			continue
		}
		if (r == ' ' || r == '　') && !spaceUsed && n >= 1 && n <= 4 && j+sz < len(s) {
			if r2, _ := utf8.DecodeRuneInString(s[j+sz:]); isNameHan(r2) {
				spaceUsed = true
				j += sz
				continue
			}
		}
		break
	}
	return st, j
}

func (m *masker) ocrNameOK(w, s string, a, b int) bool { return m.ocrNameOKEnd(w, s, a, b, false) }

// ocrNameOKEnd: skipEnd のときは「見出し・用語の字で終わる語」の判定をしない
// （見出し語のすぐ後ろに区切りなしで続く名前。「主な担当者座間」の「間」など。改善C No.26）
func (m *masker) ocrNameOKEnd(w, s string, a, b int, skipEnd bool) bool {
	nw := normName(w)
	c := utf8.RuneCountInString(nw)
	if c < 2 || c > 6 {
		return false
	}
	hasHan := false
	for _, r := range nw {
		if isNameHan(r) && r != '一' {
			hasHan = true
		}
	}
	if !hasHan || hasCounter(nw) {
		return false
	}
	for e := range m.exclude { // 除外は空白の違い（半角・全角・なし）を問わない
		if normName(e) == nw {
			return false
		}
	}
	if ocrNotName[nw] || labelNotName[nw] || stopWords[nw] || isCommonValue(nw) || m.exclude[w] || m.exclude[nw] || m.endsWithExcluded(s[:b]) {
		return false
	}
	for k := range ocrNotName {
		if utf8.RuneCountInString(k) >= 2 && (strings.HasPrefix(nw, k) || strings.HasSuffix(nw, k)) {
			return false
		}
	}
	for k := range labelNotName {
		if utf8.RuneCountInString(k) >= 2 && strings.HasPrefix(nw, k) {
			return false
		}
	}
	// 見出し・用語で終わる語は名前ではない（ただし 村・町・市・区・田 などで終わる姓は名前として扱い、住所は形で判定する）
	if r, _ := utf8.DecodeLastRuneInString(nw); !skipEnd && strings.ContainsRune(ocrNotNameEnd, r) {
		return false
	}
	if isAddrLike(nw) {
		return false
	}
	for _, p := range personNouns {
		if strings.HasSuffix(nw, p) {
			return false
		}
	}
	return true
}

// isPersonLabel: 区切りの前の見出しが「人を指す見出し」か
//
//	・一覧の見出し（担当者・顧客名・記入者 など）で終わる
//	・〇〇者（送信者・配達者・同行者 …）、〇〇者名・氏名・客名・員名
//	・担当 を含む（営業担当・担当営業・担当看護師・ご担当）
func isPersonLabel(l string) bool {
	l = strings.TrimLeft(l, "ご御")
	if l == "" {
		return false
	}
	for _, x := range ocrLabels {
		if strings.HasSuffix(l, x) {
			return true
		}
	}
	if strings.Contains(l, "担当") {
		return true
	}

	n := utf8.RuneCountInString(l)
	if strings.HasSuffix(l, "者") && n >= 2 {
		// 管理者・代表者・事業者 なども対象（自事業所の職員はフルネームで「除外」に登録する決まり。
		// よその事業所のFAXの「管理者：〇〇」は保留にして人に聞く）。会社名・法人名は名前側の一般語で除く
		return true
	}
	for _, x := range []string{"者名", "氏名", "客名", "員名", "様名", "人名"} {
		if strings.HasSuffix(l, x) {
			return true
		}
	}
	return false
}

func isSep(r rune) bool { return r == ':' || r == '：' || r == ' ' || r == '　' || r == '\t' }

// labelNameSuspectsOCR: E2 人を指す見出し＋区切り＋名前らしき語
func (m *masker) labelNameSuspectsOCR(s string) []suspect {
	var out []suspect
	for i := 0; i < len(s); {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if !isSep(r) || i == 0 {
			i += sz
			continue
		}
		// 区切りの直前の見出し（漢字・ご・御、最大8字）
		pr, _ := utf8.DecodeLastRuneInString(s[:i])
		if !(unicode.Is(unicode.Han, pr) || pr == 'ご' || pr == '御') {
			i += sz
			continue
		}
		ls := i
		for n := 0; n < 8 && ls > 0; n++ {
			q, qs := utf8.DecodeLastRuneInString(s[:ls])
			if !(unicode.Is(unicode.Han, q) || q == 'ご' || q == '御' || q == '々') {
				break
			}
			ls -= qs
		}
		label := s[ls:i]
		j := i
		sep := 0
		for j < len(s) {
			q, qsz := utf8.DecodeRuneInString(s[j:])
			if !isSep(q) {
				break
			}
			j += qsz
			sep++
		}
		next := j
		job := false
		if !isPersonLabel(label) {
			// 職種名（生活相談員・理学療法士・介護福祉士 など）のあとの名前（「理学療法士：相良」。現場試験 S3・R8.10.1）
			// 今までの見出しに当たらないときだけ。止めすぎないよう、名前の候補は下でもう一度ふるう
			job = isJobLabel(label)
		}
		if sep > 4 || !(job || isPersonLabel(label)) {
			i = next
			continue
		}
		a, b := nameRunAt(s, j)
		i = next
		if b <= a {
			continue
		}
		// 名前のあとがかな・カナで続くなら、長い語の一部（名前ではない）
		if b < len(s) {
			q, _ := utf8.DecodeRuneInString(s[b:])
			if unicode.In(q, unicode.Hiragana, unicode.Katakana) && !strings.ContainsRune("様さ", q) && q != 'ー' {
				continue
			}
		}
		w := strings.TrimSpace(s[a:b])
		if !m.ocrNameOK(w, s, a, b) {
			continue
		}
		if job && !jobNameOK(w) {
			continue
		}
		// 職種の見出し（看護師・医師 など）のあとの、介護・看護の作業・予定の語は名前ではない（「看護師 交代」「医師 指示」。点検役 R8.10.1）
		if (job || isJobLabel(label)) && careTaskWord(w) {
			continue
		}
		reason := "氏名欄（担当者・顧客名などの見出し）の後ろに、氏名一覧に登録されていない名前らしき文字があります"
		if hasGarble(w) {
			reason += "（" + garbleNote + "）"
		}
		out = append(out, suspect{word: w, context: contextAround(s, a, b), reason: reason})
	}
	return out
}

var (
	reOCRDate = regexp.MustCompile(`(?:(?:R|Ｒ|令和|H|Ｈ|平成)\s*[0-9０-９]{1,2}|(?:19|20)[0-9]{2}|[0-9０-９]{1,2})\s*[/／.．年]\s*[0-9０-９]{1,2}\s*[/／.．月]\s*[0-9０-９]{1,2}\s*日?` +
		`(?:\s*[（(]\s*[月火水木金土日]\s*(?:曜日?)?\s*[)）])?(?:\s*[0-9０-９]{1,2}\s*[:：時]\s*(?:[0-9０-９]{1,2}\s*分?)?(?:\s*[~〜～－-]\s*[0-9０-９]{1,2}\s*[:：時]\s*(?:[0-9０-９]{1,2}\s*分?)?)?)?[ 　\t]*`)
	ocrWorkWords = []string{"訪問", "電話", "来所", "連絡", "点検", "納品", "搬入", "回収", "面談", "面接", "来訪", "往診", "同行", "対応", "設置", "交換", "修理", "説明", "契約", "受付", "確認"}
)

// dateNameSuspects: E3 日付（＋曜日・時刻）の直後の名前らしき語＋業務語
func (m *masker) dateNameSuspects(s string) []suspect {
	var out []suspect
	for _, ix := range reOCRDate.FindAllStringIndex(s, -1) {
		if ix[0] > 0 {
			if r, _ := utf8.DecodeLastRuneInString(s[:ix[0]]); r >= '0' && r <= '9' || r >= '０' && r <= '９' {
				continue
			}
		}
		i := ix[1]
		// 名前（2〜4字）＋空白0〜2＋業務語
		rest := s[i:]
		var best string
		n := 0
		for k := 0; k < len(rest) && n < 4; {
			r, sz := utf8.DecodeRuneInString(rest[k:])
			if !isNameOrGarble(r) {
				break
			}
			k += sz
			n++
			if n < 2 {
				continue
			}
			t := strings.TrimLeft(rest[k:], " 　\t")
			if len(rest[k:])-len(t) > 2*len("　") {
				continue
			}
			for _, ww := range ocrWorkWords {
				if strings.HasPrefix(t, ww) {
					best = rest[:k]
					break
				}
			}
			if best != "" {
				break
			}
		}
		if best == "" {
			continue
		}
		a, b := i, i+len(best)
		if !m.ocrNameOK(best, s, a, b) {
			continue
		}
		reason := "日付のすぐ後ろに、氏名一覧に登録されていない名前らしき語があります（担当者の名前の可能性）"
		if hasGarble(best) {
			reason += "（" + garbleNote + "）"
		}
		out = append(out, suspect{word: best, context: contextAround(s, a, b), reason: reason})
	}
	return out
}

// tokenSideSuspects: 名簿の名前の記号のすぐ前・すぐ後ろに、化けた字の混ざった名前の残りがあるとき
// （例：「大〇一郎様」→「大〇【利用者001の名】様」の「大〇」、「大和一0」→「【姓001】一0」の「一0」）
func (m *masker) tokenSideSuspects(s string) []suspect {
	var out []suspect
	for _, t := range ourTokens(s, m.cb) {
		if !m.nameTok(s[t[0]:t[1]]) {
			continue
		}
		// 前
		st := t[0]
		for n := 0; n < 4 && st > 0; n++ {
			r, sz := utf8.DecodeLastRuneInString(s[:st])
			if !isNameOrGarble(r) {
				break
			}
			st -= sz
		}
		if w := s[st:t[0]]; hasGarble(w) && !hasCounter(w) && !isAddrLike(w) && !m.exclude[w] {
			out = append(out, suspect{word: w + s[t[0]:t[1]], context: contextAround(s, st, t[1]), tok: s[t[0]:t[1]], reason: "名前の一部が化けた字で残っている語（" + garbleNote + "）"})
			continue
		}
		// 後ろ
		en := t[1]
		for n := 0; n < 4 && en < len(s); n++ {
			r, sz := utf8.DecodeRuneInString(s[en:])
			if !isNameOrGarble(r) || strings.ContainsRune("様殿氏君", r) {
				break
			}
			en += sz
		}
		w := s[t[1]:en]
		if w == "" || !strings.ContainsFunc(w, isGarble) || hasCounter(w) || isAddrLike(w) || startsPlace(w) || m.exclude[w] {
			continue
		}
		if first, _ := utf8.DecodeRuneInString(w); !isNameHan(first) && first != '一' && !strings.ContainsRune("〇○◯□■＊*?？", first) {
			continue
		}
		out = append(out, suspect{word: s[t[0]:t[1]] + w, context: contextAround(s, t[0], en), tok: s[t[0]:t[1]], reason: "名前の一部が化けた字で残っている語（" + garbleNote + "）"})
	}
	return out
}

// unmask: 氏名一覧に書く候補として見せるため、語の中の記号を元の文字に戻す（この画面・確認リストの中だけ）
func (m *masker) unmask(w string) string {
	ms, _ := restoreMatches(w, m.cb)
	return applyMatches(w, ms)
}

// ocrSuspects: 3つの規則の要確認を have に加える。
// 化けた名前（E1）が、これまでの「敬称の付いた名前らしき語」（例：一郎さん）と重なるときは、
// 件数を増やさずに、化けた字を含む語と説明に差し替える（氏名一覧に書く候補が正しく出るように）。
func (m *masker) ocrSuspects(s string, have []suspect) []suspect {
	key := func(w string) string { return normName(reHonorTail.ReplaceAllString(w, "")) }
	overlaps := func(a, b string) bool {
		return a != "" && b != "" && (strings.Contains(a, b) || strings.Contains(b, a))
	}
	for _, g := range [][]suspect{m.garbleHonorSuspects(s), m.tokenSideSuspects(s), m.labelNameSuspectsOCR(s), m.dateNameSuspects(s)} {
		for _, x := range g {
			x.word = m.unmask(x.word)
			k := key(x.word)
			dup := false
			for i, h := range have {
				if !overlaps(key(h.word), k) && !overlaps(normName(h.word), normName(x.word)) {
					continue
				}
				dup = true
				if strings.HasPrefix(x.reason, "敬称") && strings.HasPrefix(h.reason, "敬称") && !strings.Contains(h.reason, "化けた") &&
					strings.HasSuffix(normName(x.word), normName(h.word)) && hasGarble(x.word) {
					have[i] = x
				} else if hasGarble(x.word) && strings.Contains(k, key(h.word)) && len(k) > len(key(h.word)) {
					have[i] = x // 「利用者名 原〇フ〇様」：見出しのあとの「原〇」より、伏せ字を1語で読んだ「原〇フ〇」を出す（改善26 試験役 L3）
				}
				break
			}
			if !dup {
				have = append(have, x)
			}
		}
	}
	return have
}

// isAllHan: 漢字だけの語か
func isAllHan(w string) bool {
	for _, r := range w {
		if !unicode.Is(unicode.Han, r) {
			return false
		}
	}
	return w != ""
}

// isJobLabel: 職種名（漢字だけの職種の語）で終わる見出しか
func isJobLabel(l string) bool {
	for _, x := range commonJobWords {
		if utf8.RuneCountInString(x) >= 2 && isAllHan(x) && strings.HasSuffix(l, x) {
			return true
		}
	}
	return false
}

// 職種名のあとに来ても名前とみなさない語（勤務の形・資格など）
var jobNotName = strings.Fields(`御侍史 侍史 御机下 机下 御中 御待史 先生 資格 常駐 対応 勤務 配置 兼務 不足 募集 在籍 派遣 常勤 非常勤 専従 専任 兼任 交代 交替 着任 退職 休暇 研修 指導 同席 同行 訪問 確認 報告 連絡 相談 説明 記録 記入 署名 氏名 不在 未定 予定 変更 追加 担当 業務 以外 以上 全員 各位 複数 数名`)

func jobNameOK(w string) bool {
	nw := normName(w)
	for _, x := range jobNotName {
		if strings.Contains(nw, x) {
			return false
		}
	}
	for _, x := range no7NotNameParts {
		if strings.Contains(nw, x) {
			return false
		}
	}
	return true
}

// jobSuffix: 語の終わりの職種の語（3字以上。ケアマネジャー・看護師など）
func jobSuffix(w string) string {
	best := ""
	for _, j := range commonJobWords {
		if utf8.RuneCountInString(j) >= 3 && strings.HasSuffix(w, j) && len(j) > len(best) {
			best = j
		}
	}
	return best
}

// 職種の前に付く、名前ではない語（主任ケアマネジャー・前任看護師 など）
var jobHeadWords = map[string]bool{"主任": true, "担当": true, "前任": true, "後任": true, "新任": true, "専任": true, "常勤": true, "非常勤": true, "訪問": true, "病棟": true, "外来": true, "在宅": true, "居宅": true, "施設": true, "病院": true, "当院": true, "貴院": true, "当所": true, "当事業所": true, "各": true, "所属": true}
