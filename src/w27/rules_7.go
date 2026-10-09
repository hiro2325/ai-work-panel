package main

// 改善13 No.7（R8.9.30・承認済み）：置き換えの見逃しの残り3つを「保留（要確認）」に出す。
// どれも保留を増やすだけ（きびしくする方向）。置き換え・今までの保留の規則はゆるめない。
//   ① 区切りのない見出しの直後の名前
//      ・「連絡先長男鈴木太郎」「家族長女〇〇」：連絡先・家族などの見出し（＋続柄）のすぐ後ろの名前
//      ・「担当者〇〇〇〇が訪問」：No.26 の見出し（担当者・記入者など）のすぐ後ろの名前に、助詞（が・は・と…）が続く形
//   ② 日付の後ろに名前だけで終わる行（「R8.9.1 〇〇〇〇」「9/15　〇〇〇〇」）
//   ③ 氏名一覧に登録済みの姓の記号のすぐ後ろに、登録されていない別の名が続く形（「【姓001】次郎」）
// 止めすぎない：名前らしき語は 3〜5字（③は下の名前 2〜3字）に限り、一般の語（担当者会議・記入者欄・訪問介護・
// 事業所名・病院名・薬の名前・模様・同様 など）や職種（「山田NS」）では止めない。

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 名前らしき語の中に入っていたら名前とみなさない語（書類によく出る業務・制度の語）
var no7NotNameParts = strings.Fields(`介護 看護 訪問 通所 通院 受診 入院 退院 入所 退所 会議 担当 予定 入浴 食事 排泄 更新 認定 申請 開始 終了 中止
	契約 支援 計画 評価 報告 連絡 電話 確認 面談 面接 調整 相談 調査 変更 利用 状況 状態 負担 軽減 同居 別居 不在 在宅 自宅 居宅
	病院 医院 薬局 施設 事業 会社 法人 番号 住所 携帯 同席 同行 対応 記入 記録 作成 送迎 往診 服薬 処方 検査 手術 転倒 骨折 体調
	様子 模様 同様 以外 以上 以下 全員 各位 一同 夫婦 家族 本人 構成 関係 欄 一覧 用紙 様式 先生 主任 管理 所長 部長 課長 係長 院長
	総合 地域 包括 市役所 役所 医療 福祉 保険 年金 手帳 検討 実施 継続 目標 課題 意向 希望 要望 援助 訓練 機能 生活 自立 介助
	住宅 改修 工事 修理 購入 貸与 一家 一族 親子 兄弟 姉妹 外泊 外出 帰省 行事 旅行 葬儀 法事 永眠 死亡 逝去 請求 発送 送付 提供 眼科 歯科 内科 外科 皮膚 耳鼻 精神 整形
	在住 勤務 来訪 来所 多忙 主婦 学生 大学 見学 指導 参加 了承 拒否 帰宅 同意 立会 仕事 県外 県内 市内 市外 都内 都外 近隣 遠方 同市 週末
	毎日 毎週 毎月 月一 共働 単身 赴任 会社員 自営 腰痛 難聴 持病 後見 書士 弁護 祝金 看多機 内視鏡 掃除 特養 老健 研修 運営 実地 衣替 夏祭 花火
	敬老 誕生 墓参 美容 買物 胃瘻 巡回 商店 団地 公園 学校 建設 電気 不動産 工務店 酒店 老人 公民館 会館 地区 自治会 整骨院 接骨院 店 館 園 校
	散歩 独歩 歩行 希望 志望 展望 願望 眺望 安静 冷静 平静 単純 湿潤 浸潤 結晶 液晶 演奏 完結 終結 締結 団結 連結 妥結 直結 凍結 帰結 結果 忍耐 神聖 静養
	錠 薬 散 液 軟膏 顆粒 注射 点滴 血圧 体温 脈拍 酸素 回復 悪化 改善 安定 変化 特記 事項 備考 共有 伝達 申し送り 引継`)

// 下の名前の終わりによく使う字（改善13 No.7 差し戻し：止めすぎないため、名前らしき語はこの字で終わるものに限る）
const no7GivenEnd = "凛紬湊葵薫潤歩純淳敦晶忍聖茜静望結蒼奏郎朗子江枝恵絵美代世夫男雄生治司吉蔵造平助介輔也哉彦太人仁和実輝樹斗香奈那乃菜花華保穂里理希紀志次三一二正明弘博清茂勇勝進誠豊隆秀義久光幸孝敏昭宏浩修衛門作松竹梅栄英衣依音織羽葉陽翔真優悠裕佑祐亮良涼蓮恭京功喜貴達徹哲健賢謙信伸晃智知聡瞳舞愛彩綾咲沙紗充満登昇稔巳馬翼航吾悟剛毅猛武朋友寛宗道範則典康靖泰安"

// no7GivenLike: 名前（または下の名前）の終わりの字が、下の名前によく使う字か
func no7GivenLike(w string) bool {
	r, _ := utf8.DecodeLastRuneInString(normName(w))
	return strings.ContainsRune(no7GivenEnd, r)
}

// no7NameOK: ①②で見つけた名前らしき語が、名前として出してよいか（止めすぎないための共通の確かめ）
func (m *masker) no7NameOK(w, s string, a, b int) bool { return m.no7NameOKMin(w, s, a, b, 3) }

// no7NameOKMin: 字数の下限を指定する（続柄の組み合わせ「長男の妻 由美」のあとは2字から）
func (m *masker) no7NameOKMin(w, s string, a, b, min int) bool {
	nw := normName(w)
	n := utf8.RuneCountInString(nw)
	if n < min || n > 5 {
		return false
	}
	for _, r := range nw {
		if !isNameHan(r) {
			return false
		}
	}
	// 頭の字（中・上・前・新・本・内・主 など）でふるうのは、最後の字が名前によく使う字でないときだけ
	// （「中村太郎」「前田太郎」などの姓を見逃さないため。点検役の再点検 R8.9.30）
	if fr, _ := utf8.DecodeRuneInString(nw); strings.ContainsRune(labelNoSepHead, fr) && !no7GivenLike(nw) {
		return false
	}
	for _, p := range no7NotNameParts {
		if strings.Contains(nw, p) {
			return false
		}
	}
	if !no7GivenLike(nw) || labelNoSepNotName[nw] || isCommonWord(nw) || hasCounter(nw) {
		return false
	}
	for k := range labelNoSepNotName {
		if utf8.RuneCountInString(k) >= 2 && (strings.HasPrefix(nw, k) || strings.HasSuffix(nw, k)) {
			return false
		}
	}
	if m.inTok(s, a, b) {
		return false
	}
	return m.ocrNameOK(w, s, a, b)
}

// no7CleanEnd: 名前のあとが「行の終わり・区切り・敬称」か。particle のときは助詞（が・は・と…）も認める
func no7CleanEnd(s string, en int, particle bool) bool {
	if en >= len(s) {
		return true
	}
	rest := s[en:]
	q, _ := utf8.DecodeRuneInString(rest)
	if isNameSep(q) || strings.ContainsRune("、。，．,.（(）)【「」：:／/・", q) {
		return true
	}
	for _, h := range []string{"さん", "様", "さま", "ちゃん", "くん", "氏", "殿"} {
		if strings.HasPrefix(rest, h) {
			return true
		}
	}
	if particle {
		for _, p := range []string{"が", "は", "と", "に", "へ", "の", "も", "より", "から"} {
			if strings.HasPrefix(rest, p) {
				return true
			}
		}
	}
	return false
}

// no7HonorOrParticle: 名前のすぐ後ろが 敬称（さん・様・氏）か「より・から・が・と・に」か
func no7HonorOrParticle(s string, en int) bool {
	rest := s[en:]
	rest = strings.TrimLeft(rest, " 　")
	for _, h := range []string{"さん", "様", "さま", "氏", "より", "から", "が", "と", "に"} {
		if strings.HasPrefix(rest, h) {
			return true
		}
	}
	return false
}

// ---- ① 区切りのない見出しの直後の名前 ----

const no7Relations = `(?:長男の妻|長男嫁|次男嫁|長男|長女|次男|次女|三男|三女|息子|娘|妻|夫|嫁|婿|孫|義母|義父|義姉|義妹|義兄|義弟|兄|姉|弟|妹|父|母|甥|姪|叔父|叔母|伯父|伯母)`

var (
	// 見出し（連絡先・家族など）＋続柄（なくてもよい）＋名前。見出しと続柄の間・続柄と名前の間の区切りはあってもなくてもよい
	reNo7RelLabel = regexp.MustCompile(`(?:緊急連絡先|連絡先|キーパーソン|ＫＰ|KP|主介護者|家族|続柄)[ 　\t]*[:：]?[ 　\t]*(` + no7Relations + `)?[ 　\t]*[:：]?[ 　\t]*`)
	// 続柄の組み合わせ（「長女の夫」「長男の妻」）＋名前（現場試験 S3）
	reNo7RelCombo = regexp.MustCompile(no7Relations + `の(?:子|` + no7Relations[3:] + `[ 　\t]*[:：]?[ 　\t]*`)
	// 行の頭（または区切りのあと）の続柄＋名前（「長男鈴木太郎」）
	reNo7RelHead = regexp.MustCompile(`(?:^|[ 　\t、。（(／/])` + no7Relations + `[ 　\t]*[:：]?[ 　\t]*`)
)

func (m *masker) no7LabelSuspects(s string) []suspect {
	var out []suspect
	add := func(st, en int, reason string) {
		out = append(out, suspect{word: strings.TrimSpace(s[st:en]), context: contextAround(s, st, en), reason: reason})
	}
	const relReason = "氏名欄（連絡先・家族・続柄などの見出し）のすぐ後ろに、氏名一覧に登録されていない名前らしき文字があります"
	for _, ix := range reNo7RelLabel.FindAllStringSubmatchIndex(s, -1) {
		if m.inTok(s, ix[0], ix[1]) || ix[1] >= len(s) {
			continue
		}
		rel := ix[2] >= 0
		st, en := nameRunAt(s, ix[1])
		if en <= st {
			continue
		}
		w := s[st:en]
		// 続柄があれば助詞が続いてもよい。続柄がなければ行の終わり・区切り・敬称だけ
		if !no7CleanEnd(s, en, rel) || !m.no7NameOK(w, s, st, en) {
			continue
		}
		add(st, en, relReason)
	}
	for _, ix := range reNo7RelHead.FindAllStringIndex(s, -1) {
		if ix[1] >= len(s) || m.inTok(s, ix[0], ix[1]) {
			continue
		}
		st, en := nameRunAt(s, ix[1])
		// 見出しのない「続柄だけ」の形は、名前の後ろに敬称か「より・から・が・と・に」が続くときだけ（「長女市内在住」などを止めない）
		if en <= st || !no7HonorOrParticle(s, en) || !m.no7NameOK(s[st:en], s, st, en) {
			continue
		}
		add(st, en, relReason)
	}
	// 続柄の組み合わせ（「長女の夫」「長男の妻」「長女の子」など）のあとの名前（現場試験 S3）
	for _, ix := range reNo7RelCombo.FindAllStringIndex(s, -1) {
		if ix[1] >= len(s) || m.inTok(s, ix[0], ix[1]) {
			continue
		}
		st, en := nameRunAt(s, ix[1])
		if en <= st || !no7CleanEnd(s, en, true) || !m.no7NameOKMin(s[st:en], s, st, en, 2) {
			continue
		}
		add(st, en, relReason)
	}
	// No.26 の見出し（担当者・記入者など）のすぐ後ろの名前に、助詞が続く形（「担当者〇〇〇〇が訪問」）
	for _, ix := range reLabelNoSep.FindAllStringIndex(s, -1) {
		if m.inTok(s, ix[0], ix[1]) || ix[1] >= len(s) {
			continue
		}
		r, _ := utf8.DecodeRuneInString(s[ix[1]:])
		if !isNameHan(r) {
			continue
		}
		st, en := nameRunAt(s, ix[1])
		if en <= st || en >= len(s) {
			continue
		}
		if q, _ := utf8.DecodeRuneInString(s[en:]); !unicode.Is(unicode.Hiragana, q) {
			continue // 助詞以外は今までの規則（No.26）で見る
		}
		if !no7CleanEnd(s, en, true) || !m.no7NameOK(s[st:en], s, st, en) {
			continue
		}
		add(st, en, "氏名欄（担当者・氏名などの見出し）のすぐ後ろに、氏名一覧に登録されていない名前らしき文字があります")
	}
	return out
}

// ---- ② 日付の後ろに名前だけで終わる行 ----

var reNo7MD = regexp.MustCompile(`[0-9０-９]{1,2}\s*(?:[/／]\s*[0-9０-９]{1,2}|月\s*[0-9０-９]{1,2}\s*日)` +
	`(?:\s*[（(]\s*[月火水木金土日]\s*(?:曜日?)?\s*[)）])?(?:\s*[0-9０-９]{1,2}\s*[:：時]\s*(?:[0-9０-９]{1,2}\s*分?)?(?:\s*[~〜～－-]\s*[0-9０-９]{1,2}\s*[:：時]\s*(?:[0-9０-９]{1,2}\s*分?)?)?)?[ 　\t]*`)

func (m *masker) no7DateSuspects(s string) []suspect {
	var out []suspect
	seen := map[int]bool{}
	try := func(a, b int) {
		if a > 0 {
			if r, _ := utf8.DecodeLastRuneInString(s[:a]); isDigitRune(r) || strings.ContainsRune("/／.．", r) {
				return
			}
		}
		if b >= len(s) || seen[b] {
			return
		}
		st, en := nameRunAt(s, b)
		if en <= st {
			return
		}
		// 名前のあとは行の終わりまで空白だけ
		if strings.TrimRight(s[en:], " 　\t\r\n") != "" {
			return
		}
		w := strings.TrimSpace(s[st:en])
		if !m.no7NameOK(w, s, st, en) {
			return
		}
		seen[b] = true
		out = append(out, suspect{word: w, context: contextAround(s, st, en), reason: "日付のすぐ後ろに、氏名一覧に登録されていない名前らしき語があります（行の終わりまで名前だけ。担当者などの名前の可能性）"})
	}
	for _, ix := range reOCRDate.FindAllStringIndex(s, -1) {
		try(ix[0], ix[1])
	}
	for _, ix := range reNo7MD.FindAllStringIndex(s, -1) {
		try(ix[0], ix[1])
	}
	return out
}

// ---- ③ 登録済みの姓の記号のすぐ後ろの、登録されていない別の名 ----

// 姓のすぐ後ろに付いても名前ではない字（宅・家・様など）
const no7NotGivenHead = "宅家方氏殿君様邸夫妻兄姉弟妹父母祖孫嫁婿甥姪宛各等及又"

func (m *masker) no7SurnameGivenSuspects(s string) []suspect {
	var out []suspect
	for _, t := range ourTokens(s, m.cb) {
		sur, ok := m.surOfCode[s[t[0]:t[1]]]
		if !ok {
			continue
		}
		b := t[1]
		if b < len(s) {
			if r, sz := utf8.DecodeRuneInString(s[b:]); r == ' ' || r == '　' {
				b += sz
			}
		}
		e := b
		n := 0
		for e < len(s) {
			r, sz := utf8.DecodeRuneInString(s[e:])
			if !isNameHan(r) {
				break
			}
			e += sz
			n++
		}
		if n < 1 || n > 3 {
			continue
		}
		g := s[b:e]
		// 1字の名（「緑川凛より」）は、名によく使う字で、後ろに敬称か「より・から・が・と・に」が続くときだけ
		if n == 1 && (!no7GivenLike(g) || !no7HonorOrParticle(s, e)) {
			continue
		}
		if fr, _ := utf8.DecodeRuneInString(g); strings.ContainsRune(no7NotGivenHead, fr) {
			continue
		}
		if lr, _ := utf8.DecodeLastRuneInString(g); strings.ContainsRune(ocrNotNameEnd, lr) || !no7GivenLike(g) {
			continue
		}
		bad := ocrNotName[g] || labelNoSepNotName[g] || labelNotName[g] || stopWords[g] || isCommonWord(g) || hasCounter(g) ||
			m.exclude[g] || m.exclude[sur+g] || m.exclude[sur+" "+g] || m.exclude[sur+"　"+g] || m.excludeOne[g]
		for _, p := range no7NotNameParts {
			if strings.Contains(g, p) {
				bad = true
			}
		}
		for _, p := range personNouns {
			if strings.HasPrefix(g, p) || strings.HasSuffix(g, p) {
				bad = true
			}
		}
		for k := range ocrNotName {
			if utf8.RuneCountInString(k) >= 2 && strings.HasPrefix(g, k) {
				bad = true
			}
		}
		if bad || isAddrLike(sur+g) {
			continue
		}
		// 後ろにひらがなが続くときは、助詞・敬称だけ（「【姓001】次郎と面談」はよい、「【姓001】作りたい」は外す）
		if e < len(s) {
			if q, _ := utf8.DecodeRuneInString(s[e:]); unicode.Is(unicode.Hiragana, q) && !no7CleanEnd(s, e, true) {
				continue
			}
		}
		out = append(out, suspect{word: s[t[0]:e], context: contextAround(s, t[0], e), reason: "氏名一覧の方の姓のすぐ後ろに、登録されていない下の名前らしき文字があります（同じ姓の別の方の可能性）"})
	}
	return out
}

// no7SurnameBeforeGiven: 利用者・家族の記号（名の記号・呼び名の記号・フルネームの記号）のすぐ前に、
// 登録されていない姓らしき字が残る形を保留に出す（「R8.9.1 中野【利用者001の名】」「長女　田辺【家族004】」）。
// 記号との間の空白1つはまたぐ。点検役の再点検・現場試験 S1（R8.10.1）
// 名の記号の前に来ても姓ではない、時・回数の語（「毎朝美沙子さんが訪問」。点検役 R8.10.1）
var no7TimeWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`毎朝 毎日 毎週 毎月 毎晩 毎夕 毎年 毎回 時々 本日 今日 昨日 明日 今朝 今夜 今晩 昨夜 先日 先週 先月 今週 今月 来週 来月 翌日 前日 当日 翌朝
		午前 午後 夕方 夜間 早朝 深夜 日中 以前 最近 普段 常時 随時 適宜 再度 今回 前回 次回 初回 今後 現在 平日 週末 休日 土日 月曜 火曜 水曜 木曜 金曜 土曜 日曜
		朝 昼 夜 夕 毎 今 次 前 後 先 再 又 既 常 都度 其後 同日 同夜 同月`) {
		no7TimeWords[w] = true
	}
}

var reNo7PersonTok = regexp.MustCompile(`^【(?:利用者|家族)[0-9]{3,}(?:の名|の呼び名[0-9]+)?】$`)
var reNo7RelOnly = regexp.MustCompile(`^` + no7Relations + `$`)

func (m *masker) no7SurnameBeforeGiven(s string) []suspect {
	var out []suspect
	for _, t := range ourTokens(s, m.cb) {
		tok := s[t[0]:t[1]]
		if !m.givenCodes[tok] && !reNo7PersonTok.MatchString(tok) {
			continue
		}
		end := t[0]
		if end > 0 {
			if r, sz := utf8.DecodeLastRuneInString(s[:end]); r == ' ' || r == '　' {
				end -= sz
			}
		}
		st := end
		n := 0
		for n < 5 && st > 0 {
			r, sz := utf8.DecodeLastRuneInString(s[:st])
			if !isNameHan(r) {
				break
			}
			st -= sz
			n++
		}
		if n < 2 {
			continue
		}
		run := s[st:end]
		// 前の字の並びの終わりが、見出し・続柄・一般の語なら姓ではない（「連絡先」「長女」「報告者」「同居中」など）
		bad := false
		rr := []rune(run)
		for k := 2; k <= len(rr); k++ {
			suf := string(rr[len(rr)-k:])
			if ocrNotName[suf] || labelNoSepNotName[suf] || labelNotName[suf] || stopWords[suf] || reNo7RelOnly.MatchString(suf) || m.exclude[suf] || no7TimeWords[suf] {
				bad = true
			}
			for _, p := range personNouns {
				if suf == p {
					bad = true
				}
			}
		}
		g := run
		if len(rr) > 3 {
			g = string(rr[len(rr)-2:])
			st = end - len(g)
		}
		if fr, _ := utf8.DecodeRuneInString(g); strings.ContainsRune(no7NotGivenHead, fr) {
			continue
		}
		if lr, _ := utf8.DecodeLastRuneInString(g); strings.ContainsRune(ocrNotNameEnd, lr) || strings.ContainsRune(no7NotGivenHead, lr) {
			continue
		}
		// 地名と同じ字の姓（中野・大和・座間 など）もあるので、よく出る語（isCommonWord）では外さない
		bad = bad || ocrNotName[g] || labelNoSepNotName[g] || labelNotName[g] || stopWords[g] || hasCounter(g) || m.exclude[g] || m.excludeOne[g] || isAddrLike(g)
		if _, ok := m.lookup[normName(g)]; ok {
			bad = true // 登録済みの語（姓など）
		}
		for _, p := range no7NotNameParts {
			if strings.Contains(g, p) {
				bad = true
			}
		}
		for _, p := range personNouns {
			if strings.HasSuffix(g, p) || strings.HasPrefix(p, g) {
				bad = true
			}
		}
		if bad || reNo7RelOnly.MatchString(g) {
			continue
		}
		out = append(out, suspect{word: s[st:t[1]], context: contextAround(s, st, t[1]), tok: s[t[0]:t[1]], reason: "名前の一部（登録済みの方の記号）のすぐ前に、登録されていない姓らしき文字があります（姓が記号になっていない、または同じ名の別の方の可能性）"})
	}
	return out
}

func (m *masker) no7Suspects(s string) [][]suspect {
	return [][]suspect{m.no7LabelSuspects(s), m.no7DateSuspects(s), m.no7SurnameGivenSuspects(s), m.no7SurnameBeforeGiven(s), m.no7JobNameSuspects(s), m.no46Suspects(s)}
}

// ---- 改善13 No.46：全角の元号の字・全角の区切り（「生年月日Ｓ２１．５．３」「Ｓ21.5.3」）の生年月日らしき日付 ----
// 登録した生年月日（呼び名）の書き方の違いは birth_alias.go で記号になる。今までの日付の決まり（reOldDate・生年月日の見出し）は
// 半角の元号の字（S）と「.」「/」の区切りだけを見ていたため、全角の形は置き換わらず橙にもならずに渡っていた。
// 置き換えのあとに残っている、明治・大正・昭和（M・T・S）の形と 1965年までの西暦の形を保留（要確認）に出す。
// 令和・平成（R・H）は記録の日付によく使うので出さない（止めすぎないため）。
var reNo46Date = regexp.MustCompile(`(?:明治|大正|昭和|明|大|昭|[MTSＭＴＳｍｔｓ])\s*[.．]?\s*` + D + `{1,2}\s*[年.．/／]\s*` + D + `{1,2}\s*[月.．/／]\s*` + D + `{1,2}\s*日?` +
	`|(?:18|19[0-5]|196[0-5]|１８|１９[０-５]|１９６[０-５])` + D + `{0,2}\s*[年.．/／]\s*` + D + `{1,2}\s*[月.．/／]\s*` + D + `{1,2}\s*日?`)

// 「生年月日」の見出しのすぐ後ろなら、平成・令和（H・R）の形も出す
var reNo46Birth = regexp.MustCompile(`生年月日\s*[：:]?\s*((?:平成|令和|平|令|[HRＨＲｈｒ])\s*[.．]?\s*` + D + `{1,2}\s*[年.．/／]\s*` + D + `{1,2}\s*[月.．/／]\s*` + D + `{1,2}\s*日?)`)

func (m *masker) no46Suspects(s string) []suspect {
	var out []suspect
	var locs [][]int
	for _, ix := range reNo46Birth.FindAllStringSubmatchIndex(s, -1) {
		locs = append(locs, []int{ix[2], ix[3]})
	}
	locs = append(locs, reNo46Date.FindAllStringIndex(s, -1)...)
	for _, ix := range locs {
		if m.inTok(s, ix[0], ix[1]) {
			continue
		}
		// 英字・数字の途中（「ABS21.5.3」「123.4.5」）は日付とみなさない
		if ix[0] > 0 {
			if r, _ := utf8.DecodeLastRuneInString(s[:ix[0]]); isDigitRune(r) || unicode.IsLetter(r) && r < 0x3000 || (r >= 'Ａ' && r <= 'ｚ') {
				continue
			}
		}
		w := s[ix[0]:ix[1]]
		// 西暦の形は年が4けたのときだけ（「19.5.3」は日付とみなさない）
		if first, _ := utf8.DecodeRuneInString(w); isDigitRune(first) {
			n := 0
			for _, r := range w {
				if !isDigitRune(r) {
					break
				}
				n++
			}
			if n != 4 {
				continue
			}
		}
		if m.exclude[w] || m.exclude[strings.TrimSpace(w)] {
			continue
		}
		out = append(out, suspect{word: strings.TrimSpace(w), context: contextAround(s, ix[0], ix[1]), reason: "生年月日らしき日付（全角の字・区切りの書き方）が残っています。利用者・家族の生年月日なら、その方の「呼び名」に書き足すと記号になります。生年月日でなければ「除外」に登録してください"})
	}
	return out
}

// ---- 職種の語のすぐ後ろの名前（「オペレーター 猪俣」「生活相談員 巽」「ＭＳＷ　奥寺」。現場試験 R8.10.1）----
// 職種の語（カタカナ・英字の職種も）＋区切り（空白・：）＋1〜4字の漢字で、そのあとが行の終わり・区切り・敬称のとき、保留に出す。
// 1字の名前（巽 など）も出す。止めすぎないよう、勤務の形・資格などの語（jobNameOK）は出さない
func (m *masker) no7JobNameSuspects(s string) []suspect {
	var out []suspect
	for _, jw := range staffWords {
		for from := 0; from < len(s); {
			i := strings.Index(s[from:], jw)
			if i < 0 {
				break
			}
			a := from + i + len(jw)
			from = a
			if m.inTok(s, a-len(jw), a) {
				continue
			}
			// 区切り（空白・：）が1つ以上
			j := a
			for j < len(s) {
				r, sz := utf8.DecodeRuneInString(s[j:])
				if r == ' ' || r == '　' || r == ':' || r == '：' || r == '\t' {
					j += sz
					continue
				}
				break
			}
			if j == a || j >= len(s) {
				continue
			}
			st, en := j, j
			n := 0
			for en < len(s) && n < 5 {
				r, sz := utf8.DecodeRuneInString(s[en:])
				if !isNameHan(r) {
					break
				}
				en += sz
				n++
			}
			if n < 1 || n > 4 || m.inTok(s, st, en) {
				continue
			}
			// 姓＋空白1つ＋名（「ヘルパー 堀江 美和」）は、名まで含めて1つの語にする
			fullEn := en
			if en < len(s) {
				if r, sz := utf8.DecodeRuneInString(s[en:]); r == ' ' || r == '　' {
					k, gn := en+sz, 0
					for k < len(s) && gn < 4 {
						r2, sz2 := utf8.DecodeRuneInString(s[k:])
						if !isNameHan(r2) {
							break
						}
						k += sz2
						gn++
					}
					if gn >= 1 && gn <= 3 && !careTaskWord(s[en+sz:k]) && (k >= len(s) || func() bool {
						q, _ := utf8.DecodeRuneInString(s[k:])
						return isNameSep(q) || strings.ContainsRune("、。，．,.（(）)【「」：:／/・", q) || strings.HasPrefix(s[k:], "さん") || strings.HasPrefix(s[k:], "様")
					}()) {
						fullEn = k
					}
				}
			}
			if en < len(s) {
				q, _ := utf8.DecodeRuneInString(s[en:])
				if !(isNameSep(q) || strings.ContainsRune("、。，．,.（(）)【「」：:／/・", q) || strings.HasPrefix(s[en:], "さん") || strings.HasPrefix(s[en:], "様") || strings.HasPrefix(s[en:], "氏")) {
					continue
				}
			}
			w := s[st:en]
			// 名前として出すのは、①敬称が付く ②この書類のほかの所で敬称付きで出てくる ③よくある姓 のときだけ。
			// 1字の語は①②のときだけ。介護・看護の作業・予定の語は出さない（点検役 R8.10.1：「看護師 交代」「ヘルパー 洗濯」など）
			honor := en < len(s) && (strings.HasPrefix(s[en:], "さん") || strings.HasPrefix(s[en:], "様") || strings.HasPrefix(s[en:], "氏"))
			seen := m.docHonor[w]
			if !(honor || seen || (n >= 2 && commonSurnames[w])) || careTaskWord(w) {
				continue
			}
			if !jobNameOK(w) || ocrNotName[w] || labelNotName[w] || labelNoSepNotName[w] || stopWords[w] || m.exclude[w] || m.excludeOne[w] || hasCounter(w) || isCommonWord(w) || isAddrLike(w) {
				continue
			}
			if _, ok := m.lookup[normName(w)]; ok {
				continue
			}
			if n >= 2 && !m.ocrNameOK(w, s, st, en) {
				continue
			}
			if fullEn > en {
				en = fullEn
				w = s[st:en]
			}
			out = append(out, suspect{word: w, context: contextAround(s, st, en), reason: "氏名欄（職種の見出し）のすぐ後ろに、氏名一覧に登録されていない名前らしき文字があります"})
		}
	}
	return out
}

var reDocHonor = regexp.MustCompile(`([\p{Han}々ヶ]{1,4})[ 　]?(?:さん|様|氏|先生)`)

// docHonorWords: 書類の中で敬称が付いて出てくる漢字の並び（後ろから1〜4字の部分も）
func docHonorWords(text string) map[string]bool {
	out := map[string]bool{}
	for _, m := range reDocHonor.FindAllStringSubmatch(text, -1) {
		rs := []rune(m[1])
		for k := 1; k <= len(rs); k++ {
			out[string(rs[len(rs)-k:])] = true
		}
	}
	return out
}

// 介護・看護の作業・予定・時の語（職種のあとに来ても名前ではない）
var careTasks = strings.Fields(`交代 交替 入浴 継続 処置 褥瘡 良好 清拭 排便 排尿 排泄 点眼 点滴 吸引 摘便 採血 軟膏 足浴 手浴 洗髪 洗濯 掃除 買物 調理 配膳 下膳 更衣 移乗 移動 安否 布団 見守 服薬 配薬 与薬 指示 診察 処方 往診 通院 同行 同席 巡回 訪問 評価 訓練 歩行 嚥下 送迎 新規 導入 中止 変更 未定 担当 不在 済 特 同上
	朝 昼 夕 夜 晩 午前 午後 夜間 日中 毎日 毎週 毎朝 週 月 回 名 時 分 体制 状態 観察 確認 報告 連絡 相談 調整 電話 対応 支援 介助 援助 介護 看護 保清 口腔 食事 水分 体位 体交 測定 計測 血圧 体温 脈拍 酸素 胃瘻 経管 注入 浣腸 導尿 カテ 創傷 処理 交換 貼付 塗布 内服 外用 準備 片付 整理 整頓 環境 整備 休み 休止 終了 開始 予定 延長 短縮 追加 減少 増加 あり なし`)

func careTaskWord(w string) bool {
	nw := normName(w)
	for _, t := range careTasks {
		// 1字の語（朝・夕・月 など）は、その字だけのときに限る（「望月」「名取」などの姓を落とさないため）
		if utf8.RuneCountInString(t) == 1 {
			if nw == t {
				return true
			}
			continue
		}
		if strings.Contains(nw, t) {
			return true
		}
	}
	return false
}

// よくある姓（職種のあとに敬称なしで来ても名前とみなす姓。置き換えるのではなく、保留にして人に聞くだけ）
var commonSurnames = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`佐藤 鈴木 高橋 田中 伊藤 渡辺 渡部 山本 中村 小林 加藤 吉田 山田 佐々木 山口 松本 井上 木村 林 斎藤 斉藤 清水 山崎 森 池田 橋本 阿部 石川 山下 中島 石井 小川 前田 岡田 長谷川 藤田 後藤 近藤 村上 遠藤 青木 坂本 斉藤 福田 太田 西村 藤井 金子 岡本 藤原 中野 三浦 原田 中川 松田 竹内 小野 田村 中山 和田 石田 森田 上田 原 柴田 酒井 工藤 横山 宮崎 宮本 内田 高木 安藤 島田 谷口 大野 高田 丸山 今井 河野 藤本 村田 武田 上野 杉山 増田 小山 大塚 平野 菅原 久保 松井 千葉 岩崎 桜井 木下 野口 松尾 菊地 菊池 野村 新井 渡部 佐野 杉本 古川 大西 小島 小松 岩田 市川 中西 松岡 桑原 樋口 服部 永井 北村 内藤 高野 荒木 松下 西田 菅野 大久保 安田 中田 平田 川口 飯田 辻 本田 久保田 吉川 須藤 沢田 澤田 小池 中尾 西川 東 岩本 石原 岡崎 堀 関 島 秋山 吉村 大橋 松浦 星野 中谷 早川 浅野 田口 大川 片山 土屋 荒井 富田 川村 桑田 黒田 永田 川上 今村 堀江 堀内 堀川 大石 望月 熊谷 石橋 小西 水野 岩井 前川 平井 五十嵐 小澤 小沢 上原 北川 若林 関口 奥村 奥田 奥山 奥野 川島 松村 大谷 河合 河村 岡村 川崎 宮田 宮下 宮内 西山 北野 岸 岸本 米田 浜田 濱田 横田 吉岡 吉野 吉本 秋元 片岡 天野 飯島 飯塚 石塚 大森 大竹 大島 大和田 小田 小田切 小池 坂口 坂井 坂田 桜田 佐久間 篠原 篠崎 島崎 白石 杉浦 鈴村 関根 高山 高須 滝沢 武藤 田島 田代 田辺 谷 千田 塚本 土田 手塚 徳永 豊田 中本 永井 成田 西岡 西野 根本 野田 萩原 長田 長島 服部 浜野 早瀬 平山 広瀬 福井 福島 福本 藤川 藤村 古田 細川 細谷 堀田 本間 前原 牧野 松原 松山 丸田 三上 三木 水谷 溝口 南 宮川 村井 村山 室田 森本 森下 八木 矢野 山内 山川 山中 山根 山村 湯浅 横井 吉原 米山 和久井 若松 渡邊 渡邉
		桐生 宍戸 相良 猪俣 奥寺 雨宮 久我 柏木 芦田 東條 小野寺 門脇 三宅 能登 真鍋 吉良 岩佐 楢崎 田所 坂下 巽 巴 芹沢 芹澤 神田 神谷 神山 鎌田 蒲田 金田 金井 川田 川端 河内 菅 栗原 栗田 黒川 小寺 駒井 近江 相馬 曽根 高松 高見 竹田 立花 橘 玉井 塚田 津田 寺田 寺島 土井 戸田 富永 内藤 仲田 中澤 中沢 夏目 西尾 西本 能勢 野々村 野沢 野澤 橋口 畑中 秦 花田 羽田 浜口 林田 春日 日野 平岡 平川 深沢 深澤 福永 藤木 二宮 古屋 保坂 星 真田 松永 三好 宮沢 宮澤 向井 村瀬 望月 本山 森川 安井 柳田 山岸 山田 結城 横川 吉沢 吉澤 依田 若山 脇田 鷲尾`) {
		commonSurnames[w] = true
	}
}
