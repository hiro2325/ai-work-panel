package main

// ②の橙の箱を減らす・分かりやすくする（改善23・R8.10.5 試験役ベテランケアマネの指摘）
//   - 「各事業所様」「関係者各位」など、事業所・宛名の語は人の名前として止めない
//   - 「利用者 三〇 延〇 様」の見出し（利用者・氏名・緊急連絡先・長女 など）を外して、名前の所だけを候補にする
//   - 見出しを外すと伏せ字だけ（「長女〇〇様」「利用者〇〇様」）なら、もともと伏せてあるので止めない

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// 事業所・宛名の語（これを含む語は人の名前ではない）
var orgWords = strings.Fields(`事業所 事業者 各位 関係者 御中 ご担当 御担当 担当者様 皆様 皆さま 施設 病院 医院 クリニック センター ステーション 薬局 会社 法人 包括 居宅 協会 組合 役所 市役所 区役所 保健所 社協 協議会`)

func isOrgWord(w string) bool {
	n := normName(w)
	for _, o := range orgWords {
		if strings.Contains(n, o) {
			return true
		}
	}
	for _, o := range []string{"各事業", "各担当", "各関係", "各サービス", "各機関", "各部署", "各施設", "各位", "各ご家族", "各御家族"} {
		if strings.HasPrefix(n, o) {
			return true
		}
	}
	return false // 「各務」のような姓があるので、「各」だけでは決めない（点検役）
}

// 名前の前に付く見出し・続柄（2字以上。長いものから）
var nameHeadWords = func() []string {
	w := strings.Fields(`利用者様氏名 ご利用者名 利用者氏名 利用者名 利用者 被保険者氏名 被保険者名 被保険者 本人氏名 本人 患者氏名 患者名 患者 契約者氏名 契約者 申請者 対象者 お名前 名前 氏名 緊急連絡先 連絡先 宛先 キーパーソン 主介護者 介護者 担当者 担当
		長女 長男 次女 次男 三女 三男 四女 四男 息子 娘婿 叔父 叔母 伯父 伯母 祖父 祖母 義父 義母 義兄 義姉 義弟 義妹 実父 実母 配偶者 夫婦 家族 親族 知人 隣人 友人`)
	sort.SliceStable(w, func(a, b int) bool { return utf8.RuneCountInString(w[a]) > utf8.RuneCountInString(w[b]) })
	return w
}()

// stripNameHead: 名前の前に付いた見出し・続柄を外した残り（残りが1字以上のときだけ外す）。
// 「急連絡先長女00」のように前が切れていても、いちばん後ろの見出しから後ろを残す。
func stripNameHead(w string) string {
	n := strings.TrimLeft(w, " 　:：・")
	best := -1
	for _, h := range nameHeadWords {
		if i := strings.LastIndex(n, h); i >= 0 && i+len(h) < len(n) && i+len(h) > best {
			best = i + len(h)
		}
	}
	if best < 0 {
		return w
	}
	rest := strings.TrimLeft(n[best:], " 　:：・")
	if rest == "" {
		return w
	}
	return rest
}

// allFuse: 伏せ字の字（と空白）だけの語（「〇〇」「00」）
func allFuse(w string) bool {
	any := false
	for _, r := range w {
		if r == ' ' || r == '　' {
			continue
		}
		if !isFuse(r) {
			return false
		}
		any = true
	}
	return any
}

// notPersonBox: 人の名前として箱にしない語（事業所・宛名の語、見出しを外すと伏せ字だけの語）
func notPersonBox(w string) bool {
	if isOrgWord(w) {
		return true
	}
	c := stripNameHead(w)
	return allFuse(c) || allFuse(w)
}

// addresseeTitle: 「ご担当ケアマネジャー様」「当ケアマネジャー」「担当者様」のような、あて名の職種だけの語（人の名前ではない。改善27 試験役 P2）。
// 職種の前に付いてよいのは「ご・御・当・担当・貴・各」だけ（「山田ケアマネジャー」のような名前つきは今までどおり）
func addresseeTitle(w string) bool {
	c := normName(reHonorTail.ReplaceAllString(strings.TrimSpace(w), ""))
	for _, j := range append([]string{"担当者", "ご担当者", "ご担当"}, commonJobWords...) {
		if utf8.RuneCountInString(j) < 3 && j != "担当者" {
			continue
		}
		if !strings.HasSuffix(c, j) {
			continue
		}
		head := strings.TrimSuffix(c, j)
		for _, p := range []string{"ご担当", "御担当", "担当", "ご", "御", "当", "貴", "各"} {
			head = strings.TrimPrefix(head, p)
		}
		if head == "" || jobHeadWords[head] { // 「病棟看護師様」「主任ケアマネジャー様」も
			return true
		}
	}
	return false
}

// knownNow: 箱の語が、いま氏名一覧に登録済みか（登録・除外・呼び名・住所）。行の番号は書かない（あとで行がずれるため）
func knownNow(sj susJSON, kn *knownIdx) string {
	look := func(w string) string {
		if w == "" {
			return ""
		}
		if r, ok := kn.names[normName(w)]; ok {
			if r.Kind == "除外" {
				return "除外に登録済み"
			}
			return r.Kind + "として登録済み"
		}
		if r, ok := kn.aliases[fuseKey(w)]; ok {
			return r.Kind + "「" + r.Name + "」の呼び名に登録済み"
		}
		return ""
	}
	switch sj.Kind {
	case "addr":
		reg := sj.Reg
		if reg == "" {
			reg = sj.Word
		}
		if registeredAddrRow(kn.rows, reg) != nil {
			return "住所として登録済み"
		}
		if r, ok := kn.names[normName(sj.Word)]; ok && r.Kind == "除外" {
			return "除外に登録済み"
		}
		return ""
	case "name":
		for _, w := range []string{sj.Name, sj.Orig, sj.Fix} {
			if k := look(w); k != "" {
				return k
			}
		}
		return ""
	default:
		if sj.Fix != "" {
			if k := look(sj.Fix); k != "" && !strings.HasPrefix(k, "除外") {
				return k
			}
		}
		if sj.Ex != "" {
			if r, ok := kn.names[normName(sj.Ex)]; ok && r.Kind == "除外" {
				return "除外に登録済み"
			}
		}
		return ""
	}
}

// lastFresh: 画面に出す置き換えの結果。押したあと・読み直したあとも、登録済みの箱は「✓ 登録済み」で出す（改善23）
func (s *panelServer) lastFresh() *maskReport {
	if s.last == nil {
		return nil
	}
	kn := s.knownIndex()
	r := *s.last
	r.Files = make([]fileJSON, len(s.last.Files))
	for i, f := range s.last.Files {
		nf := f
		nf.Suspects = make([]susJSON, len(f.Suspects))
		for j, sj := range f.Suspects {
			if sj.Known == "" {
				sj.Known = knownNow(sj, kn)
			}
			nf.Suspects[j] = sj
		}
		r.Files[i] = nf
	}
	return &r
}

// ctxCore: 箱の前後の文（記号を元の字に戻し、［］と空白を外したもの）。同じ所の箱を見分ける
func ctxCore(c string, cb *codeBook) string {
	c = unmaskWith(c, cb)
	return strings.NewReplacer("［", "", "］", "", " ", "", "　", "").Replace(c)
}

func boxWordKey(sj susJSON) string {
	w := sj.Fix
	if w == "" {
		w = reHonorTail.ReplaceAllString(sj.Name, "")
	}
	return fuseKey(w)
}

// dedupeBoxes: 1か所に2つの箱（「山口花子」の1字違いと「山口【家族002の名】」、「三武延0様」と「【姓021】延」）を1つにする。
// 同じ前後の文で、語が同じか、片方がもう片方に含まれるときは、長いほうを残し、だれの名前かの候補を合わせる
func dedupeBoxes(sus []susJSON, cb *codeBook) []susJSON {
	type info struct {
		key, ctx string
		drop     bool
	}
	in := make([]info, len(sus))
	for i, sj := range sus {
		if sj.Kind == "addr" || escOnlyWord(sj.Word) {
			continue
		}
		in[i] = info{key: boxWordKey(sj), ctx: ctxCore(sj.Context, cb)}
	}
	for i := range sus {
		if in[i].key == "" || in[i].drop {
			continue
		}
		for j := range sus {
			if i == j || in[j].key == "" || in[j].drop || in[i].drop {
				continue
			}
			a, b := in[i], in[j]
			if utf8.RuneCountInString(a.key) < 2 || utf8.RuneCountInString(b.key) < 2 {
				continue
			}
			sameCtx := a.ctx == b.ctx || (a.key != b.key && (strings.Contains(a.ctx, b.ctx) || strings.Contains(b.ctx, a.ctx)))
			if !sameCtx || !(strings.Contains(a.key, b.key) || strings.Contains(b.key, a.key)) {
				continue
			}
			nameLike := func(x susJSON) bool { return x.Kind == "name" || x.Fix != "" }
			if a.key != b.key && !(nameLike(sus[i]) && nameLike(sus[j])) {
				continue // 名前の箱どうしのときだけ、含む・含まれるでまとめる
			}
			// 残すほう：語が長いほう（同じなら、候補のある・名前の箱のほう）
			keep, drop := i, j
			la, lb := utf8.RuneCountInString(a.key), utf8.RuneCountInString(b.key)
			if lb > la || (lb == la && len(sus[j].Owners) > len(sus[i].Owners)) {
				keep, drop = j, i
			}
			for _, o := range sus[drop].Owners {
				if len(sus[keep].Owners) > 0 {
					break // 残す箱にすでに候補があれば、そちらを使う（字の合う方だけ。改善23 C3）
				}
				dup := false
				for _, k := range sus[keep].Owners {
					if k.Row == o.Row {
						dup = true
					}
				}
				if !dup {
					sus[keep].Owners = append(sus[keep].Owners, o)
				}
			}
			if sus[keep].Fix == "" {
				sus[keep].Fix = sus[drop].Fix
			}
			in[drop].drop = true
		}
	}
	out := []susJSON{}
	for i, sj := range sus {
		if !in[i].drop {
			out = append(out, sj)
		}
	}
	return out
}

var reEscMark = regexp.MustCompile(`_x00[0-9a-fA-F]{2}_`)

func escOnlyWord(w string) bool {
	return strings.Contains(w, "_x00") && strings.Trim(reEscMark.ReplaceAllString(w, ""), " \t　") == ""
}

// stripAddrHead: 住所の前に付いた見出し（住所・現住所・住所地・所在地）を外す
func stripAddrHead(w string) string {
	n := strings.TrimSpace(w)
	for _, h := range []string{"現住所", "住所地", "所在地", "住所"} {
		if strings.HasPrefix(n, h) {
			rest := strings.TrimLeft(strings.TrimPrefix(n, h), " 　:：")
			if utf8.RuneCountInString(rest) >= 4 {
				return rest
			}
		}
	}
	return w
}

var rePlaceKana = regexp.MustCompile(`登録した方の名前「([^」]+)」と同じ字。事業所名・地名なら除外`)
