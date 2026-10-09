package main

// 氏名一覧.xlsx の1枚目に1行だけ書き足す（ほかの部分には触れない）

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var (
	reRowOpen  = regexp.MustCompile(`<row\b[^>]*\br="(\d+)"[^>]*?(/>|>)`)
	reHasValue = regexp.MustCompile(`<v>|<is>`)
	reDimRef   = regexp.MustCompile(`<dimension ref="[^"]*"\s*/>`)
)

func xmlText(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func appendNameRow(path, kind, name string, alias ...string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("氏名一覧.xlsx を開けません: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("氏名一覧.xlsx が壊れているようです: %v", err)
	}
	const part = "xl/worksheets/sheet1.xml"
	var sheet []byte
	for _, f := range zr.File {
		if f.Name == part {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			sheet, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return err
			}
		}
	}
	if sheet == nil {
		return fmt.Errorf("氏名一覧.xlsx の1枚目のシートが見つかりません")
	}
	al := ""
	if len(alias) > 0 {
		al = alias[0]
	}
	ns, err := insertNameRow(string(sheet), kind, name, al)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		h := f.FileHeader
		w, err := zw.CreateHeader(&h)
		if err != nil {
			return err
		}
		if f.Name == part {
			if _, err := w.Write([]byte(ns)); err != nil {
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

func insertNameRow(x, kind, name, alias string) (string, error) {
	if strings.Contains(x, "<sheetData/>") {
		x = strings.Replace(x, "<sheetData/>", "<sheetData></sheetData>", 1)
	}
	sd := strings.Index(x, "<sheetData>")
	se := strings.Index(x, "</sheetData>")
	if sd < 0 || se < 0 {
		return "", fmt.Errorf("氏名一覧.xlsx の形が想定と違います")
	}
	body := x[sd+len("<sheetData>") : se]
	// 使われている最後の行を探す
	type rowPos struct{ r, start, end int }
	var rows []rowPos
	maxUsed := 0
	for _, ix := range reRowOpen.FindAllStringSubmatchIndex(body, -1) {
		r, _ := strconv.Atoi(body[ix[2]:ix[3]])
		end := ix[1]
		if body[ix[4]:ix[5]] == ">" {
			e := strings.Index(body[ix[1]:], "</row>")
			if e < 0 {
				return "", fmt.Errorf("氏名一覧.xlsx の形が想定と違います")
			}
			end = ix[1] + e + len("</row>")
		}
		rows = append(rows, rowPos{r, ix[0], end})
		if reHasValue.MatchString(body[ix[0]:end]) && r > maxUsed {
			maxUsed = r
		}
	}
	n := maxUsed + 1
	if n < 2 {
		n = 2
	}
	ns := strconv.Itoa(n)
	newRow := `<row r="` + ns + `"><c r="A` + ns + `" t="inlineStr"><is><t>` + xmlText(kind) + `</t></is></c><c r="B` + ns + `" t="inlineStr"><is><t>` + xmlText(name) + `</t></is></c>`
	if alias != "" {
		newRow += `<c r="C` + ns + `" t="inlineStr"><is><t>` + xmlText(alias) + `</t></is></c>`
	}
	newRow += `</row>`
	nb := ""
	done := false
	for _, rp := range rows {
		if done {
			break
		}
		if rp.r == n { // 空の行（書式だけ）があれば置き換える
			nb = body[:rp.start] + newRow + body[rp.end:]
			done = true
		} else if rp.r > n {
			nb = body[:rp.start] + newRow + body[rp.start:]
			done = true
		}
	}
	if !done {
		nb = body + newRow
	}
	x = x[:sd+len("<sheetData>")] + nb + x[se:]
	if loc := reDimRef.FindStringIndex(x); loc != nil {
		x = x[:loc[0]] + `<dimension ref="A1:C` + strconv.Itoa(max(n, maxUsed)) + `"/>` + x[loc[1]:]
	}
	return x, nil
}
