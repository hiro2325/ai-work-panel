package main

// カイポケのCSV（改善19・R8.10.3）
//   - 月1回出して、作業フォルダの「カイポケCSV」に置く（ファイル名は自由・中の見出しで見分ける）。
//   - 利用者情報（名前・カナ・生年月日・住所）と認定情報（要介護度・認定日・有効期間）の2種類。
//   - 文字コードは UTF-8（BOMあり／なし）と CP932（Shift-JIS）の両方を読む（CP932の表は cp932.bin を埋め込み）。
//   - ここは読むだけ。CSVは書き換えない・消さない。中身はこのパソコンの中だけで使い、AIには渡さない。

import (
	"bytes"
	_ "embed"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const kaipokeCSVDir = "カイポケCSV"

//go:embed cp932.bin
var cp932Table []byte // 先頭バイト 0x81〜0xFC × 後続バイト 0x40〜0xFC の uint16（リトルエンディアン。0 は割り当てなし）

// decodeCP932: CP932（Shift-JIS）→ UTF-8。割り当てのない並びは U+FFFD にする
func decodeCP932(b []byte) string {
	var sb strings.Builder
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c < 0x80:
			sb.WriteByte(c)
			i++
		case c >= 0xA1 && c <= 0xDF:
			sb.WriteRune(rune(0xFF61 + int(c) - 0xA1))
			i++
		case (c >= 0x81 && c <= 0x9F) || (c >= 0xE0 && c <= 0xFC):
			if i+1 < len(b) && b[i+1] >= 0x40 && b[i+1] <= 0xFC {
				idx := (int(c)-0x81)*189 + int(b[i+1]) - 0x40
				if idx*2+1 < len(cp932Table) {
					v := int(cp932Table[idx*2]) | int(cp932Table[idx*2+1])<<8
					if v != 0 {
						sb.WriteRune(rune(v))
						i += 2
						continue
					}
				}
				sb.WriteRune(0xFFFD)
				i += 2
				continue
			}
			sb.WriteRune(0xFFFD)
			i++
		default:
			sb.WriteRune(0xFFFD)
			i++
		}
	}
	return sb.String()
}

// decodeKaipoke: BOMあり→UTF-8／UTF-8として正しければ UTF-8／それ以外は CP932
func decodeKaipoke(b []byte) (string, string) {
	if bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}) {
		return string(b[3:]), "UTF-8（BOMあり）"
	}
	if utf8.Valid(b) {
		return string(b), "UTF-8"
	}
	return decodeCP932(b), "CP932"
}

// ---------------- 名前・カナ・日付の正規化 ----------------

const hankakuKana = "ｦｧｨｩｪｫｬｭｮｯｰｱｲｳｴｵｶｷｸｹｺｻｼｽｾｿﾀﾁﾂﾃﾄﾅﾆﾇﾈﾉﾊﾋﾌﾍﾎﾏﾐﾑﾒﾓﾔﾕﾖﾗﾘﾙﾚﾛﾜﾝ"
const zenkakuKana = "ヲァィゥェォャュョッーアイウエオカキクケコサシスセソタチツテトナニヌネノハヒフヘホマミムメモヤユヨラリルレロワン"

func kpNormName(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r == ' ' || r == '　' || r == '\t' || r == ' ' || r == '\r' || r == '\n' || r == 0xFEFF:
		case r >= 'Ａ' && r <= 'Ｚ':
			sb.WriteRune(r - 'Ａ' + 'A')
		case r >= 'ａ' && r <= 'ｚ':
			sb.WriteRune(r - 'ａ' + 'a')
		case r >= '０' && r <= '９':
			sb.WriteRune(r - '０' + '0')
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// kpNormKana: 空白を除き、ひらがな→カタカナ、半角カナ→全角カナ（濁点・半濁点をまとめる）
func kpNormKana(s string) string {
	hk := []rune(hankakuKana)
	zk := []rune(zenkakuKana)
	var out []rune
	rs := []rune(kpNormName(s))
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r >= 0xFF66 && r <= 0xFF9D {
			z := zk[int(r)-0xFF66]
			if i+1 < len(rs) && rs[i+1] == 0xFF9E { // ﾞ
				if strings.ContainsRune("カキクケコサシスセソタチツテトハヒフヘホ", z) {
					z++
					i++
				} else if z == 'ウ' {
					z = 'ヴ'
					i++
				}
			} else if i+1 < len(rs) && rs[i+1] == 0xFF9F && strings.ContainsRune("ハヒフヘホ", z) { // ﾟ
				z += 2
				i++
			}
			_ = hk
			out = append(out, z)
			continue
		}
		if r >= 0x3041 && r <= 0x3096 {
			r += 0x60
		}
		out = append(out, r)
	}
	return string(out)
}

var reKpDate = regexp.MustCompile(`^(\d{4})\s*[/\-.年／－．]\s*(\d{1,2})\s*[/\-.月／－．]\s*(\d{1,2})`)

func kpParseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(strings.Trim(toHalfDigits(strings.TrimSpace(s)), `"`))
	m := reKpDate.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	y, mo, d := atoi(m[1]), atoi(m[2]), atoi(m[3])
	if y < 1850 || y > 2200 || mo < 1 || mo > 12 || d < 1 || d > 31 {
		return time.Time{}, false
	}
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	if int(t.Month()) != mo || t.Day() != d {
		return time.Time{}, false
	}
	return t, true
}

// kpLevel: 要介護度の文字 → 1〜5。要支援・非該当・読めないものは 0 と理由を返す
func kpLevel(s string) (int, string) {
	t := kpNormName(toHalfDigits(s))
	if t == "" {
		return 0, "要介護度が空です"
	}
	if strings.Contains(t, "経過的") || strings.Contains(t, "要支援") {
		return 0, "「" + strings.TrimSpace(s) + "」は要支援のため、第1表の要介護1〜5には当てはまりません（選びません）"
	}
	if m := regexp.MustCompile(`要介護度?([1-5])`).FindStringSubmatch(t); m != nil {
		return atoi(m[1]), ""
	}
	if regexp.MustCompile(`^[1-5]$`).MatchString(t) {
		return atoi(t), ""
	}
	return 0, "要介護度（" + strings.TrimSpace(s) + "）を読み取れませんでした（選びません）"
}

// ---------------- 見出し ----------------

type kpCol struct {
	idx int
	err string // 見分けられない理由
}

// kpFindCol: 見出しの中から、toks のどれかを含む列を探す（なければ、見出しが途中で切れていることを考え、見出しが tok の前方一致になる列）
func kpFindCol(hdr []string, toks ...string) kpCol {
	norm := make([]string, len(hdr))
	for i, h := range hdr {
		norm[i] = kpNormName(h)
	}
	pick := func(match func(h, tok string) bool) []int {
		var hit []int
		for i, h := range norm {
			if h == "" {
				continue
			}
			for _, t := range toks {
				if match(h, t) {
					hit = append(hit, i)
					break
				}
			}
		}
		return hit
	}
	for tier := 0; tier < 2; tier++ {
		var hit []int
		if tier == 0 {
			hit = pick(func(h, t string) bool { return strings.Contains(h, t) })
		} else {
			hit = pick(func(h, t string) bool { return utf8.RuneCountInString(h) >= 3 && strings.HasPrefix(t, h) })
		}
		if len(hit) == 1 {
			return kpCol{idx: hit[0]}
		}
		if len(hit) > 1 {
			var exact []int
			for _, i := range hit {
				for _, t := range toks {
					if norm[i] == t {
						exact = append(exact, i)
						break
					}
				}
			}
			if len(exact) == 1 {
				return kpCol{idx: exact[0]}
			}
			return kpCol{idx: -1, err: "見出し「" + toks[0] + "」に当てはまる列が2つ以上あります"}
		}
	}
	return kpCol{idx: -1, err: "見出し「" + toks[0] + "」が見つかりません"}
}

// ---------------- 読み込み ----------------

type kpPerson struct { // 利用者情報の1行
	Name, Kana                 string
	Birth                      time.Time
	BirthOK                    bool
	BirthRaw                   string
	Pref, City, Town, Building string
}

type kpCert struct { // 認定情報の1行
	Name, Kana             string
	LevelRaw               string
	Cert, Start, End       time.Time
	CertOK, StartOK, EndOK bool
}

type kpData struct {
	Dir        string
	Persons    []kpPerson
	Certs      []kpCert
	File1      string // 使った利用者情報のファイル名
	File2      string // 使った認定情報のファイル名
	Enc1, Enc2 string
	Problems1  []string // 利用者情報を読めなかった理由
	Problems2  []string
	Notes      []string // 読めなかったCSV・同じ種類が複数あったことなど
}

type kpFile struct {
	name string
	mod  time.Time
}

// loadKaipoke: dir（作業フォルダ\カイポケCSV）の中のCSVを読む。同じ種類が複数あれば、更新日がいちばん新しいもの
func loadKaipoke(dir string) *kpData {
	d := &kpData{Dir: dir}
	ents, err := os.ReadDir(dir)
	if err != nil {
		d.Problems1 = []string{"「" + kaipokeCSVDir + "」フォルダがありません。カイポケからCSVを出して、このフォルダに置いてください"}
		d.Problems2 = d.Problems1
		return d
	}
	var files []kpFile
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, "~$") || strings.HasPrefix(n, ".") || !strings.EqualFold(filepath.Ext(n), ".csv") {
			continue
		}
		st, err := os.Stat(filepath.Join(dir, n))
		if err != nil || !st.Mode().IsRegular() || st.Size() > 64<<20 {
			continue
		}
		files = append(files, kpFile{n, st.ModTime()})
	}
	sort.Slice(files, func(a, b int) bool {
		if !files[a].mod.Equal(files[b].mod) {
			return files[a].mod.After(files[b].mod)
		}
		return files[a].name < files[b].name
	})
	if len(files) == 0 {
		d.Problems1 = []string{"「" + kaipokeCSVDir + "」フォルダにCSVがありません。カイポケからCSVを出して置いてください"}
		d.Problems2 = d.Problems1
		return d
	}
	var bad1, bad2 []string // 見出しが合わない理由（種類ごと）
	var unknown []string
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil {
			d.Notes = append(d.Notes, "「"+f.name+"」を読めませんでした（Excelなどで開いていたら閉じてください）")
			continue
		}
		text, enc := decodeKaipoke(b)
		recs := parseKpCSV(text)
		if len(recs) == 0 {
			unknown = append(unknown, f.name)
			continue
		}
		hdr := recs[0]
		k1, why1 := classifyKp1(hdr)
		k2, why2 := classifyKp2(hdr)
		switch {
		case k1 != nil:
			if d.File1 != "" {
				d.Notes = append(d.Notes, "利用者情報のCSVが2つ以上あります。いちばん新しい「"+d.File1+"」を使い、「"+f.name+"」は使いません")
				continue
			}
			d.File1, d.Enc1 = f.name, enc
			d.Persons = k1.rows(recs[1:])
		case k2 != nil:
			if d.File2 != "" {
				d.Notes = append(d.Notes, "認定情報のCSVが2つ以上あります。いちばん新しい「"+d.File2+"」を使い、「"+f.name+"」は使いません")
				continue
			}
			d.File2, d.Enc2 = f.name, enc
			d.Certs = k2.rows(recs[1:])
		default:
			if why1 != "" {
				bad1 = append(bad1, "「"+f.name+"」："+why1)
			}
			if why2 != "" {
				bad2 = append(bad2, "「"+f.name+"」："+why2)
			}
			if why1 == "" && why2 == "" {
				unknown = append(unknown, f.name)
			}
		}
	}
	if d.File1 == "" {
		d.Problems1 = append(bad1, d.Problems1...)
		if len(d.Problems1) == 0 {
			d.Problems1 = []string{"利用者情報のCSV（見出しに「利用者名」「生年月日」「住所」などがあるもの）が見つかりません"}
		}
	}
	if d.File2 == "" {
		d.Problems2 = append(bad2, d.Problems2...)
		if len(d.Problems2) == 0 {
			d.Problems2 = []string{"認定情報のCSV（見出しに「利用者名」「要介護度」「認定年月日」などがあるもの）が見つかりません"}
		}
	}
	if len(unknown) > 0 && (d.File1 == "" || d.File2 == "") {
		d.Notes = append(d.Notes, "見出しを見分けられなかったCSV："+strings.Join(unknown, "、"))
	}
	return d
}

func parseKpCSV(text string) [][]string {
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	first := text
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		first = text[:i]
	}
	comma := ','
	if strings.Count(first, "\t") > strings.Count(first, ",") {
		comma = '\t'
	} else if strings.Count(first, ";") > strings.Count(first, ",") {
		comma = ';'
	}
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = comma
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	var out [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			if rec == nil {
				continue
			}
		}
		blank := true
		for _, c := range rec {
			if strings.TrimSpace(c) != "" {
				blank = false
				break
			}
		}
		if !blank {
			out = append(out, rec)
		}
	}
	return out
}

type kpClass1 struct{ name, kana, birth, pref, city, town, bld kpCol }
type kpClass2 struct{ name, kana, level, cert, start, end kpCol }

func classifyKp1(hdr []string) (*kpClass1, string) {
	c := &kpClass1{
		name:  kpFindCol(hdr, "利用者名"),
		kana:  kpFindCol(hdr, "利用者カナ", "利用者フリガナ", "利用者ふりがな"),
		birth: kpFindCol(hdr, "利用者生年月日", "生年月日"),
		pref:  kpFindCol(hdr, "都道府県"),
		city:  kpFindCol(hdr, "市区町村"),
		town:  kpFindCol(hdr, "町名以下", "町名"),
		bld:   kpFindCol(hdr, "建物名"),
	}
	if c.name.idx < 0 || c.birth.idx < 0 {
		return nil, ""
	}
	if c.city.idx < 0 && c.town.idx < 0 {
		return nil, "利用者情報らしいCSVですが、住所の見出し（市区町村・町名以下）が見つかりません"
	}
	return c, ""
}

func classifyKp2(hdr []string) (*kpClass2, string) {
	c := &kpClass2{
		name:  kpFindCol(hdr, "利用者名"),
		kana:  kpFindCol(hdr, "利用者カナ", "利用者フリガナ", "利用者ふりがな"),
		level: kpFindCol(hdr, "要介護度", "要介護状態区分", "介護度"),
		cert:  kpFindCol(hdr, "認定年月日", "認定日"),
		start: kpFindCol(hdr, "認定有効開始日", "有効開始日", "認定有効期間開始", "有効期間開始", "認定有効開始年月日", "有効開始年月日"), // 「認定有効開始年月日」（カイポケの被保険者証情報。改善27の2）
		end:   kpFindCol(hdr, "認定有効終了日", "有効終了日", "認定有効期間終了", "有効期間終了", "認定有効終了年月日", "有効終了年月日"),
	}
	if c.name.idx < 0 || c.level.idx < 0 {
		return nil, ""
	}
	var miss []string
	for _, x := range []struct {
		c kpCol
		n string
	}{{c.cert, "認定年月日"}, {c.start, "認定有効開始日"}, {c.end, "認定有効終了日"}} {
		if x.c.idx < 0 {
			miss = append(miss, x.n)
		}
	}
	if len(miss) > 0 {
		return nil, "認定情報らしいCSVですが、見出し（" + strings.Join(miss, "・") + "）が見つからないか、見分けられません"
	}
	return c, ""
}

func kpAt(rec []string, c kpCol) string {
	if c.idx < 0 || c.idx >= len(rec) {
		return ""
	}
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(rec[c.idx]), `"`))
}

func (c *kpClass1) rows(recs [][]string) []kpPerson {
	var out []kpPerson
	for _, r := range recs {
		p := kpPerson{Name: kpAt(r, c.name), Kana: kpAt(r, c.kana), BirthRaw: kpAt(r, c.birth),
			Pref: kpAt(r, c.pref), City: kpAt(r, c.city), Town: kpAt(r, c.town), Building: kpAt(r, c.bld)}
		if p.Name == "" {
			continue
		}
		p.Birth, p.BirthOK = kpParseDate(p.BirthRaw)
		out = append(out, p)
	}
	return out
}

func (c *kpClass2) rows(recs [][]string) []kpCert {
	var out []kpCert
	for _, r := range recs {
		x := kpCert{Name: kpAt(r, c.name), Kana: kpAt(r, c.kana), LevelRaw: kpAt(r, c.level)}
		if x.Name == "" {
			continue
		}
		x.Cert, x.CertOK = kpParseDate(kpAt(r, c.cert))
		x.Start, x.StartOK = kpParseDate(kpAt(r, c.start))
		x.End, x.EndOK = kpParseDate(kpAt(r, c.end))
		out = append(out, x)
	}
	return out
}

// ---------------- 1人を探す ----------------

type kpFind struct {
	// 利用者情報（nil なら書かない）
	Person    *kpPerson
	PersonMsg string // 書かない理由
	Address   string
	AddrMsg   string // 住所を書かない理由
	// 認定情報（nil なら書かない）
	Cert     *kpCert
	CertMsg  string
	Level    int // 0 なら選ばない
	LevelMsg string
	Valid    bool   // 今日が有効期間内
	ValidMsg string // 認定済を選ばない理由
	Notes    []string
}

// kpAddress: 都道府県＋市区町村＋町名以下＋建物名（建物名の前は全角空白1つ。空の項目は飛ばす）
func kpAddress(p *kpPerson) string {
	base := p.Pref + p.City + p.Town
	if p.Building != "" {
		if base != "" {
			return base + "　" + p.Building
		}
		return p.Building
	}
	return base
}

func kpSerial(t time.Time) int {
	return int(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Sub(time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)).Hours() / 24)
}

// find: 名前（空白を除いて正規化）で探す。today は今日（認定の有効期間の判定に使う）
func (d *kpData) find(name string, today time.Time) *kpFind {
	f := &kpFind{}
	want := kpNormName(name)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)

	// 利用者情報
	kana := ""
	person1OK := false
	if d.File1 == "" {
		f.PersonMsg = strings.Join(d.Problems1, "／")
	} else {
		var hits []kpPerson
		for _, p := range d.Persons {
			if kpNormName(p.Name) == want {
				hits = append(hits, p)
			}
		}
		groups := map[string][]kpPerson{}
		var keys []string
		for _, p := range hits {
			k := kpNormKana(p.Kana)
			if _, ok := groups[k]; !ok {
				keys = append(keys, k)
			}
			groups[k] = append(groups[k], p)
		}
		switch {
		case len(hits) == 0:
			f.PersonMsg = "利用者情報のCSV（" + d.File1 + "）に、この名前が見つかりません"
		case len(keys) > 1:
			f.PersonMsg = "利用者情報のCSV（" + d.File1 + "）に、同じ名前の方が2人以上います（" + strconv.Itoa(len(keys)) + "人）。どの方か決められないので、書きません"
		default:
			g := groups[keys[0]]
			same := true
			for _, p := range g[1:] {
				if p.BirthRaw != g[0].BirthRaw || kpAddress(&p) != kpAddress(&g[0]) {
					same = false
				}
			}
			if !same {
				f.PersonMsg = "利用者情報のCSV（" + d.File1 + "）に、同じ名前・カナの行が内容の違う形で2行以上あります。どれか決められないので、書きません"
			} else {
				p := g[0]
				f.Person = &p
				kana = keys[0]
				person1OK = true
				f.Address = kpAddress(&p)
				if !p.BirthOK {
					f.Notes = append(f.Notes, "生年月日（"+p.BirthRaw+"）を日付として読めなかったので、書きません")
				}
				if f.Address == "" {
					f.AddrMsg = "住所が空なので、書きません"
				}
			}
		}
	}

	// 認定情報
	if d.File2 == "" {
		f.CertMsg = strings.Join(d.Problems2, "／")
	} else if d.File1 != "" && !person1OK {
		f.CertMsg = "利用者情報で方が決まらなかったので、認定情報も書きません"
	} else {
		var hits []kpCert
		kset := map[string]bool{}
		for _, c := range d.Certs {
			if kpNormName(c.Name) != want {
				continue
			}
			ck := kpNormKana(c.Kana)
			if kana != "" && ck != "" && ck != kana {
				// カナが違う同名の別人
				kset[ck] = true
				continue
			}
			hits = append(hits, c)
			kset[ck] = true
		}
		switch {
		case len(hits) == 0:
			if len(kset) > 0 && kana != "" {
				f.CertMsg = "認定情報のCSV（" + d.File2 + "）に、同じ名前でカナの合う行が見つかりません"
			} else {
				f.CertMsg = "認定情報のCSV（" + d.File2 + "）に、この名前が見つかりません"
			}
		case kana == "" && len(kset) > 1:
			f.CertMsg = "認定情報のCSV（" + d.File2 + "）に、同じ名前の方が2人以上います。どの方か決められないので、書きません"
		default:
			var cur *kpCert
			for i := range hits {
				c := &hits[i]
				if c.StartOK && c.EndOK && !today.Before(c.Start) && !today.After(c.End) {
					if cur == nil || !cur.StartOK || c.Start.After(cur.Start) {
						cur = c
					}
				}
			}
			if cur != nil {
				f.Valid = true
			} else {
				for i := range hits {
					c := &hits[i]
					if cur == nil || (c.StartOK && (!cur.StartOK || c.Start.After(cur.Start))) {
						cur = c
					}
				}
			}
			f.Cert = cur
			f.Level, f.LevelMsg = kpLevel(cur.LevelRaw)
			if !f.Valid {
				switch {
				case !cur.StartOK || !cur.EndOK:
					f.ValidMsg = "認定の有効期間を日付として読めないので、「認定済」は選びません"
				case today.After(cur.End):
					f.ValidMsg = "認定の有効期間が切れています。「認定済」は選びません"
				default:
					f.ValidMsg = "認定の有効期間がまだ始まっていません。「認定済」は選びません"
				}
			}
			if !cur.CertOK {
				f.Notes = append(f.Notes, "認定年月日を日付として読めなかったので、書きません")
			}
		}
	}
	return f
}

func (d *kpData) usedFiles() string {
	var s []string
	if d.File1 != "" {
		s = append(s, d.File1)
	}
	if d.File2 != "" {
		s = append(s, d.File2)
	}
	return strings.Join(s, "・")
}

var _ = fmt.Sprintf
