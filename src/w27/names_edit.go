package main

// 氏名一覧.xlsx の1枚目を、行単位で読む・直す・消す（ほかの部分・書式には触れない）。
// 記号対応表（記号対応表.tsv）には一切触れない：消した人も、同じ区分・同じ氏名で登録し直せば同じ記号に戻る。

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const namePart = "xl/worksheets/sheet1.xml"

type nameRow struct {
	Row   int    `json:"row"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Alias string `json:"alias"`
	Code  string `json:"code,omitempty"`
	Dup   int    `json:"dup,omitempty"` // 同じ区分・同じ氏名の行の数（2以上のとき。改善5）
	// 呼び名に入っている住所（番地まで書かれたもの）。氏名一覧のページで「住所の登録へ移す」を出す（改善6）
	AddrAlias []string `json:"addrAlias,omitempty"`
}

type xRowSheet struct {
	Rows []struct {
		R string `xml:"r,attr"`
		C []struct {
			R  string `xml:"r,attr"`
			T  string `xml:"t,attr"`
			V  string `xml:"v"`
			IS struct {
				T string `xml:"t"`
				R []struct {
					T string `xml:"t"`
				} `xml:"r"`
			} `xml:"is"`
		} `xml:"c"`
	} `xml:"sheetData>row"`
}

// readNameBook: xlsx の中身（全エントリ）と1枚目のシートXML
func readNameBook(path string) (*zip.Reader, []byte, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("氏名一覧.xlsx を開けません（%v）", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("氏名一覧.xlsx が壊れているようです: %v", err)
	}
	var sheet []byte
	for _, f := range zr.File {
		if f.Name == namePart {
			rc, err := f.Open()
			if err != nil {
				return nil, nil, nil, err
			}
			sheet, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, nil, nil, err
			}
		}
	}
	if sheet == nil {
		return nil, nil, nil, fmt.Errorf("氏名一覧.xlsx の1枚目のシートが見つかりません")
	}
	return zr, data, sheet, nil
}

func zipSST(zr *zip.Reader) []string {
	var sst []string
	for _, f := range zr.File {
		if f.Name != "xl/sharedStrings.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		var x xSST
		if xml.Unmarshal(b, &x) == nil {
			for _, si := range x.SI {
				s := si.T
				for _, r := range si.R {
					s += r.T
				}
				sst = append(sst, s)
			}
		}
	}
	return sst
}

// parseNameRows: readNameList と同じ読み方（1つ目の行は見出し、氏名が空の行は飛ばす）で、行番号つきで返す
func parseNameRows(sheet []byte, sst []string) ([]nameRow, error) {
	var sh xRowSheet
	if err := xml.Unmarshal(bytes.TrimPrefix(sheet, []byte(bom)), &sh); err != nil {
		return nil, fmt.Errorf("氏名一覧.xlsx の読み取りに失敗しました: %v", err)
	}
	out := []nameRow{}
	for ri, row := range sh.Rows {
		if ri == 0 {
			continue
		}
		cols := map[int]string{}
		for _, c := range row.C {
			v := c.V
			switch c.T {
			case "s":
				if i, err := strconv.Atoi(c.V); err == nil && i < len(sst) {
					v = sst[i]
				}
			case "inlineStr":
				v = c.IS.T
				for _, r := range c.IS.R {
					v += r.T
				}
			}
			cols[colOf(c.R)] = strings.TrimSpace(v)
		}
		if cols[2] == "" {
			continue
		}
		n, _ := strconv.Atoi(row.R)
		out = append(out, nameRow{Row: n, Kind: cols[1], Name: cols[2], Alias: cols[3]})
	}
	return out, nil
}

func readNameRows(path string) ([]nameRow, error) {
	zr, _, sheet, err := readNameBook(path)
	if err != nil {
		return nil, err
	}
	return parseNameRows(sheet, zipSST(zr))
}

// readNameRowsXML: 書き換えたあとのシートXML（まだ保存していないもの）を、行番号つきで読む
func readNameRowsXML(path, sheet string) ([]nameRow, error) {
	zr, _, _, err := readNameBook(path)
	if err != nil {
		return nil, err
	}
	return parseNameRows([]byte(sheet), zipSST(zr))
}

// writeNameSheet: 1枚目のシートXMLだけを差し替えて保存（ほかのエントリはそのまま写す）
func writeNameSheet(path string, zr *zip.Reader, sheet string) error {
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		h := f.FileHeader
		w, err := zw.CreateHeader(&h)
		if err != nil {
			return err
		}
		if f.Name == namePart {
			if _, err := w.Write([]byte(sheet)); err != nil {
				return err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		_, err = io.Copy(w, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out.Bytes(), 0600); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("氏名一覧.xlsx に書き込めませんでした。Excelで開いていたら閉じてください（%v）", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("氏名一覧.xlsx に書き込めませんでした。Excelで開いていたら閉じてください（%v）", err)
	}
	return nil
}

// nameListOpenInExcel: Excel が開いているときにできる「~$氏名一覧.xlsx」があるか
func nameListOpenInExcel(path string) bool {
	_, err := os.Stat(filepath.Join(filepath.Dir(path), "~$"+filepath.Base(path)))
	return err == nil
}

type rowSpan struct{ r, start, end int }

// sheetRows: sheetData の中の各 <row> の位置（body の中の位置）
func sheetRows(x string) (sd, se int, rows []rowSpan, err error) {
	if strings.Contains(x, "<sheetData/>") {
		return 0, 0, nil, fmt.Errorf("氏名一覧にまだ誰も登録されていません")
	}
	sd = strings.Index(x, "<sheetData>")
	se = strings.Index(x, "</sheetData>")
	if sd < 0 || se < 0 {
		return 0, 0, nil, fmt.Errorf("氏名一覧.xlsx の形が想定と違います")
	}
	sd += len("<sheetData>")
	body := x[sd:se]
	for _, ix := range reRowOpen.FindAllStringSubmatchIndex(body, -1) {
		r, _ := strconv.Atoi(body[ix[2]:ix[3]])
		end := ix[1]
		if body[ix[4]:ix[5]] == ">" {
			e := strings.Index(body[ix[1]:], "</row>")
			if e < 0 {
				return 0, 0, nil, fmt.Errorf("氏名一覧.xlsx の形が想定と違います")
			}
			end = ix[1] + e + len("</row>")
		}
		rows = append(rows, rowSpan{r, ix[0], end})
	}
	return sd, se, rows, nil
}

var (
	reCellAny  = regexp.MustCompile(`(?s)<c\b[^>]*?(?:/>|>.*?</c>)`)
	reCellHead = regexp.MustCompile(`^<c\b([^>]*?)(/?)>`)
	reAttrR    = regexp.MustCompile(`\sr="([A-Z]+)(\d+)"`)
	reAttrT    = regexp.MustCompile(`\st="[^"]*"`)
	reRowHead  = regexp.MustCompile(`^<row\b[^>]*?(/?)>`)
)

// setRowCells: 1行の A〜C 列の値を置き換える（セルの書式 s="" などはそのまま）。空の値は値だけ消す。
func setRowCells(rowXML string, n int, vals map[string]string) string {
	hm := reRowHead.FindStringSubmatch(rowXML)
	head := rowXML[:len(hm[0])]
	inner := ""
	if hm[1] == "/" {
		head = strings.TrimSuffix(head, "/>") + ">"
	} else {
		inner = strings.TrimSuffix(rowXML[len(hm[0]):], "</row>")
	}
	type cell struct {
		col int
		x   string
	}
	var cells []cell
	have := map[string]bool{}
	ns := strconv.Itoa(n)
	for _, cx := range reCellAny.FindAllString(inner, -1) {
		h := reCellHead.FindStringSubmatch(cx)
		if h == nil {
			continue
		}
		rm := reAttrR.FindStringSubmatch(" " + h[1])
		col := ""
		if rm != nil {
			col = rm[1]
		}
		if v, ok := vals[col]; ok {
			have[col] = true
			attrs := reAttrT.ReplaceAllString(reAttrR.ReplaceAllString(" "+h[1], ""), "")
			attrs = strings.TrimSpace(attrs)
			if attrs != "" {
				attrs = " " + attrs
			}
			if v == "" {
				cx = `<c r="` + col + ns + `"` + attrs + `/>`
			} else {
				cx = `<c r="` + col + ns + `"` + attrs + ` t="inlineStr"><is><t>` + xmlText(v) + `</t></is></c>`
			}
		}
		cells = append(cells, cell{colOf(col), cx})
	}
	for col, v := range vals {
		if have[col] || v == "" {
			continue
		}
		cells = append(cells, cell{colOf(col), `<c r="` + col + ns + `" t="inlineStr"><is><t>` + xmlText(v) + `</t></is></c>`})
	}
	sort.SliceStable(cells, func(a, b int) bool { return cells[a].col < cells[b].col })
	var b strings.Builder
	b.WriteString(head)
	for _, c := range cells {
		b.WriteString(c.x)
	}
	b.WriteString("</row>")
	return b.String()
}

// editNameRowXML: 行 n の区分・氏名・呼び名を書き換える
func editNameRowXML(x string, n int, kind, name, alias string) (string, error) {
	sd, se, rows, err := sheetRows(x)
	if err != nil {
		return "", err
	}
	body := x[sd:se]
	for _, rp := range rows {
		if rp.r != n {
			continue
		}
		nr := setRowCells(body[rp.start:rp.end], n, map[string]string{"A": kind, "B": name, "C": alias})
		return x[:sd] + body[:rp.start] + nr + body[rp.end:] + x[se:], nil
	}
	return "", fmt.Errorf("氏名一覧の %d 行目が見つかりません", n)
}

var (
	reRowNumAttr = regexp.MustCompile(`^(<row\b[^>]*?\br=")(\d+)(")`)
	reCellRefNum = regexp.MustCompile(`(<c\b[^>]*?\br="[A-Z]+)(\d+)(")`)
	reDimFull    = regexp.MustCompile(`<dimension ref="([A-Z]+\d+):([A-Z]+)(\d+)"\s*/>`)
)

// deleteNameRowXML: 行 n を消し、下の行を1つずつ上げる。
// 結合セル・条件付き書式・リンク・テーブル・数式があるシートでは、ずれを避けるため行の中身だけを消す。
func deleteNameRowXML(x string, n int) (string, bool, error) {
	sd, se, rows, err := sheetRows(x)
	if err != nil {
		return "", false, err
	}
	body := x[sd:se]
	idx := -1
	for i, rp := range rows {
		if rp.r == n {
			idx = i
		}
	}
	if idx <= 0 { // 見出し行（1つ目の行）は消さない
		return "", false, fmt.Errorf("氏名一覧の %d 行目は消せません", n)
	}
	shift := !strings.Contains(x, "<mergeCell") && !strings.Contains(x, "<conditionalFormatting") &&
		!strings.Contains(x, "<hyperlink") && !strings.Contains(x, "<tablePart") && !strings.Contains(body, "<f")
	rp := rows[idx]
	if !shift {
		nr := setRowCells(body[rp.start:rp.end], n, map[string]string{"A": "", "B": "", "C": ""})
		return x[:sd] + body[:rp.start] + nr + body[rp.end:] + x[se:], false, nil
	}
	var b strings.Builder
	b.WriteString(body[:rp.start])
	last := 1
	for i, o := range rows {
		if i < idx {
			if o.r > last {
				last = o.r
			}
			continue
		}
		if i == idx {
			if i+1 < len(rows) {
				b.WriteString(body[o.end:rows[i+1].start])
			} else {
				b.WriteString(body[o.end:])
			}
			continue
		}
		seg := body[o.start:o.end]
		if o.r > n {
			nn := strconv.Itoa(o.r - 1)
			seg = reRowNumAttr.ReplaceAllString(seg, "${1}"+nn+"${3}")
			seg = reCellRefNum.ReplaceAllString(seg, "${1}"+nn+"${3}")
			if o.r-1 > last {
				last = o.r - 1
			}
		} else if o.r > last {
			last = o.r
		}
		b.WriteString(seg)
		if i+1 < len(rows) {
			b.WriteString(body[o.end:rows[i+1].start])
		} else {
			b.WriteString(body[o.end:])
		}
	}
	nx := x[:sd] + b.String() + x[se:]
	if m := reDimFull.FindStringSubmatchIndex(nx); m != nil {
		nx = nx[:m[0]] + `<dimension ref="` + nx[m[2]:m[3]] + `:` + nx[m[4]:m[5]] + strconv.Itoa(last) + `"/>` + nx[m[1]:]
	}
	return nx, true, nil
}

// modifyNameRow: 画面で見ていた内容（old）と今のファイルが同じことを確かめてから、書き換える
func modifyNameRow(path string, old nameRow, f func(x string) (string, error)) error {
	if nameListOpenInExcel(path) {
		return fmt.Errorf("氏名一覧.xlsx がExcelで開かれています。Excelを閉じてから、もう一度押してください")
	}
	zr, _, sheet, err := readNameBook(path)
	if err != nil {
		return err
	}
	rows, err := parseNameRows(sheet, zipSST(zr))
	if err != nil {
		return err
	}
	found := false
	for _, r := range rows {
		if r.Row == old.Row && old.Row > 0 {
			if r.Kind != old.Kind || r.Name != old.Name || r.Alias != old.Alias {
				return fmt.Errorf("氏名一覧が、画面を開いたあとで変わっています。画面を読み直してから、もう一度操作してください")
			}
			found = true
		}
	}
	if !found {
		return fmt.Errorf("氏名一覧の該当する行が見つかりません。画面を読み直してください")
	}
	nx, err := f(string(sheet))
	if err != nil {
		return err
	}
	return writeNameSheet(path, zr, nx)
}
