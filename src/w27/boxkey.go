package main

// ②の橙の箱をまとめる・入力欄を整える（現場試験 T1・T2・R8.10.1）
//   - 同じ住所（都道府県の有無・空白の違い）は1つの箱に（鍵は addrCanon）
//   - 名前の前に職種・事業所の語が付いただけのもの（「福祉用具門脇さん」と「門脇さん」）は1つの箱に（鍵は前の語を除いた名前）
//   - すでに氏名一覧にある語の箱は「登録済み」（判断の数に入れない）
//   - 姓と名の間にスペースがない名前は、姓らしき所が分かれば、スペースを入れた形を入力欄に入れておく

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 名前の前に付いても名前ではない語（職種・事業所の種類）。長いものから外す
var boxPrefixWords = func() []string {
	w := append([]string{}, commonJobWords...)
	w = append(w, strings.Fields(`福祉用具 福祉用具専門相談員 訪問看護 訪問介護 訪問入浴 訪問リハ 通所リハ 通所介護 通所 デイサービス デイケア デイ ショートステイ ショート
		地域包括 包括 事業所 ステーション 病院 医院 クリニック 薬局 担当医 担当 看護 介護 リハビリ リハ 相談 サ責 ＰＴ ＯＴ ＳＴ ＮＳ ＭＳＷ MSW 居宅 施設 特養 老健`)...)
	sort.SliceStable(w, func(a, b int) bool { return utf8.RuneCountInString(w[a]) > utf8.RuneCountInString(w[b]) })
	return w
}()

// stripBoxPrefix: 名前の前に付いた職種・事業所の語を外す（残りが2字以上のときだけ）
func stripBoxPrefix(name string) string {
	n := strings.TrimSpace(name)
	for _, p := range boxPrefixWords {
		if strings.HasPrefix(n, p) {
			rest := strings.TrimLeft(strings.TrimPrefix(n, p), " 　")
			if utf8.RuneCountInString(normName(rest)) >= 2 && !isCommonWord(rest) {
				return rest
			}
		}
	}
	return n
}

type knownIdx struct {
	names map[string]nameRow // normName → 行（利用者・家族・その他・除外）
	rows  []nameRow
	surs  []string // 登録済みの姓（長いものから）
	givs  []string // 登録済みの名
	// 呼び名（伏せ字の字を〇にそろえた鍵）→ 行（利用者・家族・その他。改善23）
	aliases map[string]nameRow
}

func (s *panelServer) knownIndex() *knownIdx {
	k := &knownIdx{names: map[string]nameRow{}, aliases: map[string]nameRow{}}
	rows, err := readNameRows(s.p.nameList)
	if err != nil {
		return k
	}
	k.rows = rows
	for _, r := range rows {
		switch r.Kind {
		case "利用者", "家族", "その他", "除外":
		default:
			continue
		}
		if _, ok := k.names[normName(r.Name)]; !ok {
			k.names[normName(r.Name)] = r
		}
		if r.Kind != "除外" {
			for _, a := range aliasList(r.Alias) {
				if _, ok := k.aliases[fuseKey(a)]; !ok && utf8.RuneCountInString(normName(a)) >= 2 {
					k.aliases[fuseKey(a)] = r
				}
			}
			if f := strings.FieldsFunc(r.Name, func(c rune) bool { return c == ' ' || c == '　' }); len(f) == 2 {
				k.surs = append(k.surs, f[0])
				k.givs = append(k.givs, f[1])
			}
		}
	}
	sort.SliceStable(k.surs, func(a, b int) bool { return len(k.surs[a]) > len(k.surs[b]) })
	return k
}

// spacedName: 姓と名の間にスペースがない漢字の名前に、姓らしき所でスペースを入れる（分からなければそのまま）
func spacedName(n string, k *knownIdx) string {
	if strings.ContainsAny(n, " 　") {
		return n
	}
	rs := []rune(n)
	for _, r := range rs {
		if !unicode.Is(unicode.Han, r) && r != '々' {
			return n
		}
	}
	if len(rs) < 3 || len(rs) > 6 {
		return n
	}
	for _, sur := range k.surs {
		if strings.HasPrefix(n, sur) && utf8.RuneCountInString(n) > utf8.RuneCountInString(sur) {
			return sur + " " + strings.TrimPrefix(n, sur)
		}
	}
	for _, g := range k.givs {
		if strings.HasSuffix(n, g) && utf8.RuneCountInString(n) > utf8.RuneCountInString(g) {
			return strings.TrimSuffix(n, g) + " " + g
		}
	}
	// 氏名一覧の姓・名から分からないときは、スペースなしのまま（3字・1字の姓で位置をまちがえるため。点検役 R8.10.1）
	return n
}

func (s *panelServer) boxKey(sj *susJSON, k *knownIdx) {
	switch sj.Kind {
	case "addr":
		reg := sj.Reg
		if reg == "" {
			reg = sj.Word
		}
		if c := addrCanon(reg); c != "" {
			sj.Key = "a:" + c
		}
		if r := registeredAddrRow(k.rows, reg); r != nil {
			sj.Known = "住所として登録済み"
			_ = r
		}
	case "name":
		orig := sj.Name
		base := stripBoxPrefix(stripNameHead(sj.Name)) // 「利用者」「緊急連絡先」「長女」などの見出しも外す（改善23）
		if base != sj.Name {
			sj.Name = base
		}
		sj.Key = "n:" + normName(base)
		if sj.Garbled {
			sj.Key = "n:" + fuseKey(base) // 〇・O・0 などの違いは同じ箱に（改善22）
		}
		sj.Staff, sj.Fam = staffFam(sj.Context, sj.Word, base, base != strings.TrimSpace(orig))
		for _, r := range k.rows {
			if f := strings.FieldsFunc(r.Name, func(c rune) bool { return c == ' ' || c == '　' }); r.Kind == "除外" && len(f) == 2 && f[0] == normName(base) {
				sj.Self = true // 自分の事業所の職員（除外）と同じ姓
			}
		}
		if r, ok := k.names[normName(base)]; ok {
			sj.Known = r.Kind + "として登録済み" // 行の番号は書かない（あとで行がずれるため。改善23）
			if r.Kind == "除外" {
				sj.Known = "除外に登録済み"
			}
			return
		}
		if sp := spacedName(sj.Name, k); sp != sj.Name {
			sj.Orig = sj.Name
			sj.Name = sp
		}
	}
}

// 職員らしさ・家族らしさ（改善14・点検役 R8.10.1）：まとめて「その他」の箱に入れる（チェックを付ける）のは、
// 職種の語が名前のすぐ前後に直接付く形だけ（「生活相談員 宍戸」「福祉用具門脇」「相談員：巽」「オペレーター猪俣」「ＭＳＷ奥寺」「早瀬医師」「〇〇NS」）。
// 「主治医より田辺さんへ」のように「より・へ・と・が」でつながるだけのものは入れない。置き換えの決まりは変えない
var staffWords = strings.Fields(`生活相談員 支援相談員 医療相談員 相談員 看護師 准看護師 保健師 薬剤師 医師 先生 歯科医師 歯科衛生士 理学療法士 作業療法士 言語聴覚士 療法士 介護福祉士 社会福祉士 精神保健福祉士
	管理栄養士 栄養士 介護職員 職員 介護員 訪問介護員 ヘルパー オペレーター ドライバー 運転手 ケアマネ ケアマネジャー 介護支援専門員 サ責 サービス提供責任者 提供責任者 管理者 所長 施設長 事務員
	福祉用具専門相談員 専門相談員 ＭＳＷ MSW ＰＴ ＯＴ ＳＴ PT OT ST Ns NS ＮＳ Ｎｓ Dr Ｄｒ 主治医 担当医 看護 師長 主任`)
var famWords = strings.Fields(`長男 長女 次男 次女 三男 三女 息子 娘 妻 夫 嫁 婿 孫 家族 兄 姉 弟 妹 父 母 本人 友人 知人 近所 義母 義父 甥 姪 叔父 叔母 伯父 伯母`)

// 名前ではない語（まとめ箱に入れない。「記載医師」の「記載」、「病棟看護師」の「病棟」など）
var staffNotName = strings.Fields(`記載 病棟 主治 担当 当院 外来 病院 紹介 依頼 当該 貴院 同院 各 同 訪問 同行 同席 往診 夜勤 日勤 当直 常勤 非常勤 専任 新任 前任 後任 代理 代行`)

func staffFam(ctx, word, base string, stripped bool) (bool, bool) {
	nb := normName(base)
	for _, w := range staffNotName {
		if nb == w {
			return false, false
		}
	}
	if ocrNotName[nb] || labelNotName[nb] || labelNoSepNotName[nb] {
		return false, false
	}
	i := strings.Index(ctx, "［")
	j := strings.LastIndex(ctx, "］")
	pre, post := "", ""
	if i >= 0 && j > i {
		pre, post = ctx[:i], ctx[j+len("］"):]
	}
	pre = strings.TrimRight(pre, " 　:：・")
	preNo := strings.TrimSuffix(pre, "の") // 「ヘルパーの桐生さん」「長女の田辺さん」
	preNo = strings.TrimRight(preNo, " 　:：・")
	post = strings.TrimLeft(post, " 　")
	w := reHonorTail.ReplaceAllString(strings.TrimSpace(word), "")
	fam := false
	for _, f := range famWords {
		if strings.HasSuffix(preNo, f) || strings.HasPrefix(post, "（"+f) || strings.HasPrefix(post, "("+f) {
			fam = true
		}
	}
	if fam {
		return false, true
	}
	if stripped {
		return true, false
	}
	for _, x := range staffWords {
		if strings.HasSuffix(preNo, x) || strings.HasPrefix(post, x) || strings.HasSuffix(word, x) && !strings.HasSuffix(w, x) {
			return true, false
		}
	}
	return false, false
}

// namesAddMany: いくつかの名前をまとめて同じ区分で氏名一覧に書く（②の「まとめて『その他』」。現場試験 T1）
func (s *panelServer) namesAddMany(r *http.Request) (any, error) {
	var q struct {
		Words []string
		Kind  string
	}
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	if q.Kind != "その他" && q.Kind != "家族" && q.Kind != "利用者" && q.Kind != "除外" {
		return nil, fmt.Errorf("区分が正しくありません")
	}
	if len(q.Words) == 0 || len(q.Words) > 200 {
		return nil, fmt.Errorf("書く名前がありません")
	}
	if nameListOpenInExcel(s.p.nameList) {
		return nil, fmt.Errorf("氏名一覧.xlsx がExcelで開かれています。Excelを閉じてから、もう一度押してください")
	}
	rows, err := readNameRows(s.p.nameList)
	if err != nil {
		return nil, err
	}
	var added, already []string
	for _, w := range q.Words {
		w = strings.TrimSpace(w)
		if w == "" || len([]rune(w)) > 40 || strings.ContainsAny(w, "\t\r\n【】") {
			continue
		}
		dup := false
		for _, x := range rows {
			if x.Kind == q.Kind && normName(x.Name) == normName(w) {
				dup = true
			}
		}
		if dup {
			already = append(already, w)
			continue
		}
		if err := appendNameRow(s.p.nameList, q.Kind, w); err != nil {
			return nil, err
		}
		rows = append(rows, nameRow{Kind: q.Kind, Name: w})
		added = append(added, w)
	}
	msg := fmt.Sprintf("%d 人を「%s」に登録しました", len(added), q.Kind)
	if len(added) > 0 {
		msg += "（" + strings.Join(added, "、") + "）"
	}
	if len(already) > 0 {
		msg += "。すでに登録済み：" + strings.Join(already, "、")
	}
	return map[string]any{"ok": msg, "added": added, "already": already, "pend": s.bumpPend(len(added))}, nil
}
