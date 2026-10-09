package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ---- PDF（FAX・スキャン）の読み取り ----
// Windows に最初から入っている文字読み取り機能（Windows.Media.Ocr）と
// PDF表示機能（Windows.Data.Pdf）を Windows PowerShell 5.1 から呼び出す。
// 通信はしない。スクリプトは英数字だけで書く（文字コードの問題を避けるため）。

const ocrScript = `param([string]$Pdf, [string]$Out)
$ErrorActionPreference = 'Stop'
try {
  Add-Type -AssemblyName System.Runtime.WindowsRuntime
  $null = [Windows.Storage.StorageFile, Windows.Storage, ContentType = WindowsRuntime]
  $null = [Windows.Data.Pdf.PdfDocument, Windows.Data.Pdf, ContentType = WindowsRuntime]
  $null = [Windows.Data.Pdf.PdfPageRenderOptions, Windows.Data.Pdf, ContentType = WindowsRuntime]
  $null = [Windows.Media.Ocr.OcrEngine, Windows.Foundation, ContentType = WindowsRuntime]
  $null = [Windows.Globalization.Language, Windows.Globalization, ContentType = WindowsRuntime]
  $null = [Windows.Graphics.Imaging.BitmapDecoder, Windows.Graphics, ContentType = WindowsRuntime]
  $null = [Windows.Graphics.Imaging.SoftwareBitmap, Windows.Graphics, ContentType = WindowsRuntime]
  $null = [Windows.Graphics.Imaging.BitmapTransform, Windows.Graphics, ContentType = WindowsRuntime]
  $null = [Windows.Storage.Streams.InMemoryRandomAccessStream, Windows.Storage.Streams, ContentType = WindowsRuntime]

  $methods = [System.WindowsRuntimeSystemExtensions].GetMethods()
  $asTaskOp = ($methods | Where-Object { $_.Name -eq 'AsTask' -and $_.GetParameters().Count -eq 1 -and $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncOperation` + "`" + `1' })[0]
  $asTaskAct = ($methods | Where-Object { $_.Name -eq 'AsTask' -and $_.GetParameters().Count -eq 1 -and $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncAction' })[0]
  function Await($op, [Type]$t) {
    $task = $asTaskOp.MakeGenericMethod($t).Invoke($null, @($op))
    $null = $task.Wait(-1)
    return $task.Result
  }
  function AwaitAction($act) {
    $task = $asTaskAct.Invoke($null, @($act))
    $null = $task.Wait(-1)
  }

  $lang = New-Object Windows.Globalization.Language('ja')
  if (-not [Windows.Media.Ocr.OcrEngine]::IsLanguageSupported($lang)) { throw 'NOJA' }
  $engine = [Windows.Media.Ocr.OcrEngine]::TryCreateFromLanguage($lang)
  if ($engine -eq $null) { throw 'NOJA' }

  $file = Await ([Windows.Storage.StorageFile]::GetFileFromPathAsync($Pdf)) ([Windows.Storage.StorageFile])
  $doc = Await ([Windows.Data.Pdf.PdfDocument]::LoadFromFileAsync($file)) ([Windows.Data.Pdf.PdfDocument])
  $sb = New-Object System.Text.StringBuilder
  for ($i = 0; $i -lt $doc.PageCount; $i++) {
    $page = $doc.GetPage([uint32]$i)
    $w = [double]$page.Size.Width
    $h = [double]$page.Size.Height
    $target = [Math]::Min(2400.0, [double][Windows.Media.Ocr.OcrEngine]::MaxImageDimension)
    $scale = $target / [Math]::Max($w, $h)
    $opt = New-Object Windows.Data.Pdf.PdfPageRenderOptions
    $opt.DestinationWidth = [uint32]([Math]::Round($w * $scale))
    $opt.DestinationHeight = [uint32]([Math]::Round($h * $scale))
    $stream = New-Object Windows.Storage.Streams.InMemoryRandomAccessStream
    AwaitAction ($page.RenderToStreamAsync($stream, $opt))
    $stream.Seek(0)
    $dec = Await ([Windows.Graphics.Imaging.BitmapDecoder]::CreateAsync($stream)) ([Windows.Graphics.Imaging.BitmapDecoder])
    $bmp = Await ($dec.GetSoftwareBitmapAsync()) ([Windows.Graphics.Imaging.SoftwareBitmap])
    $res = Await ($engine.RecognizeAsync($bmp)) ([Windows.Media.Ocr.OcrResult])
    # FAX_SCAN may be upside down or sideways: if few hiragana are read, try other rotations and keep the best
    $best = ([regex]::Matches([string]$res.Text, '[\u3041-\u3096]')).Count
    if ($best -lt 10) {
      foreach ($rn in @('Clockwise180Degrees', 'Clockwise90Degrees', 'Clockwise270Degrees')) {
        try {
          $tr = New-Object Windows.Graphics.Imaging.BitmapTransform
          $tr.Rotation = [Windows.Graphics.Imaging.BitmapRotation]::$rn
          $rb = Await ($dec.GetSoftwareBitmapAsync([Windows.Graphics.Imaging.BitmapPixelFormat]::Bgra8, [Windows.Graphics.Imaging.BitmapAlphaMode]::Premultiplied, $tr, [Windows.Graphics.Imaging.ExifOrientationMode]::IgnoreExifOrientation, [Windows.Graphics.Imaging.ColorManagementMode]::DoNotColorManage)) ([Windows.Graphics.Imaging.SoftwareBitmap])
          $rr = Await ($engine.RecognizeAsync($rb)) ([Windows.Media.Ocr.OcrResult])
          $sc = ([regex]::Matches([string]$rr.Text, '[\u3041-\u3096]')).Count
          if ($sc -gt $best) { $best = $sc; $res = $rr }
        } catch { }
      }
    }
    $null = $sb.AppendLine('#PAGE ' + ($i + 1))
    foreach ($line in $res.Lines) {
      $words = @($line.Words)
      if ($words.Count -eq 0) { continue }
      $x = [Math]::Round($words[0].BoundingRect.X)
      $y = [Math]::Round($words[0].BoundingRect.Y)
      $lh = [Math]::Round($words[0].BoundingRect.Height)
      $null = $sb.AppendLine([string]$y + "` + "`" + `t" + [string]$x + "` + "`" + `t" + [string]$lh + "` + "`" + `t" + $line.Text)
    }
    $page.Dispose()
    $stream.Dispose()
  }
  [System.IO.File]::WriteAllText($Out, $sb.ToString(), (New-Object System.Text.UTF8Encoding($false)))
  exit 0
} catch {
  $msg = $_.Exception.Message
  if ($_.Exception.InnerException) { $msg = $msg + ' / ' + $_.Exception.InnerException.Message }
  [System.IO.File]::WriteAllText($Out + '.err', $msg, (New-Object System.Text.UTF8Encoding($false)))
  exit 1
}
`

type ocrLine struct {
	y, x, h int
	text    string
}

type ocrPage struct {
	lines []ocrLine
}

// runOCR is replaced in tests.
var runOCR = runOCRWindows

func runOCRWindows(pdfPath, workDir string) ([]ocrPage, error) {
	stamp := fmt.Sprint(time.Now().UnixNano())
	script := filepath.Join(workDir, ".読取_"+stamp+".ps1")
	out := filepath.Join(workDir, ".読取_"+stamp+".txt")
	defer os.Remove(script)
	defer os.Remove(out)
	defer os.Remove(out + ".err")
	if err := os.WriteFile(script, []byte(ocrScript), 0600); err != nil {
		return nil, err
	}
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(ps); err != nil {
		ps = "powershell.exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-Pdf", pdfPath, "-Out", out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if b, err := os.ReadFile(out + ".err"); err == nil {
		msg := strings.TrimSpace(string(b))
		if strings.Contains(msg, "NOJA") {
			return nil, fmt.Errorf("このパソコンに日本語の文字読み取り機能が入っていません（Windowsの「設定」→「時刻と言語」→「言語」→「日本語」の「言語のオプション」で「光学式文字認識」を追加してください）")
		}
		return nil, fmt.Errorf("PDFを読み取れませんでした（%s）", msg)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("読み取りに時間がかかりすぎたため止めました（15分）。ページ数の多いPDFは分けてください")
	}
	if runErr != nil {
		return nil, fmt.Errorf("読み取りの処理を起動できませんでした（%v %s）", runErr, strings.TrimSpace(stderr.String()))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("読み取り結果がありません: %v", err)
	}
	return parseOCROutput(string(data)), nil
}

func parseOCROutput(s string) []ocrPage {
	var pages []ocrPage
	sc := bufio.NewScanner(strings.NewReader(strings.TrimPrefix(s, bom)))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.HasPrefix(line, "#PAGE ") {
			pages = append(pages, ocrPage{})
			continue
		}
		f := strings.SplitN(line, "\t", 4)
		if len(f) < 4 || len(pages) == 0 {
			continue
		}
		y, _ := strconv.Atoi(f[0])
		x, _ := strconv.Atoi(f[1])
		h, _ := strconv.Atoi(f[2])
		pages[len(pages)-1].lines = append(pages[len(pages)-1].lines, ocrLine{y: y, x: x, h: h, text: f[3]})
	}
	return pages
}

func isWide(r rune) bool {
	return r > 0x2E7F || unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
}

// joinWide removes the spaces the Japanese OCR puts between characters
// (「遠 藤 太 一」→「遠藤太一」、「0 4 6 - 2 3 4」→「046-234」).
// Spaces between multi-letter Latin words (「Excel file」) are kept.
func joinWide(s string) string {
	toks := strings.Split(s, " ")
	var b strings.Builder
	for i, t := range toks {
		if i > 0 {
			prev := toks[i-1]
			pr, _ := utf8.DecodeLastRuneInString(prev)
			nr, _ := utf8.DecodeRuneInString(t)
			single := utf8.RuneCountInString(prev) == 1 && utf8.RuneCountInString(t) == 1
			if prev == "" || t == "" || !(isWide(pr) || isWide(nr) || single) {
				b.WriteString(" ")
			}
		}
		b.WriteString(t)
	}
	return b.String()
}

// pageRows sorts OCR lines top-to-bottom and joins lines on the same height into one row.
func pageRows(p ocrPage) []string {
	ls := append([]ocrLine(nil), p.lines...)
	sort.SliceStable(ls, func(a, b int) bool {
		if ls[a].y != ls[b].y {
			return ls[a].y < ls[b].y
		}
		return ls[a].x < ls[b].x
	})
	var rows [][]ocrLine
	for _, l := range ls {
		if n := len(rows); n > 0 {
			last := rows[n-1]
			ref := last[0]
			tol := ref.h / 2
			if tol < 8 {
				tol = 8
			}
			if l.y-ref.y <= tol {
				rows[n-1] = append(last, l)
				continue
			}
		}
		rows = append(rows, []ocrLine{l})
	}
	var out []string
	for _, r := range rows {
		sort.SliceStable(r, func(a, b int) bool { return r[a].x < r[b].x })
		var parts []string
		for _, l := range r {
			t := strings.TrimSpace(joinWide(l.text))
			if t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			out = append(out, strings.Join(parts, "　"))
		}
	}
	return out
}

// ---- Word（.docx）の書き出し（読み取った文字だけ） ----

func writeSimpleDocx(path, title, note string, pages [][]string) error {
	var body strings.Builder
	para := func(text string, bold bool, size int, gray bool) {
		rpr := `<w:rFonts w:ascii="游ゴシック" w:eastAsia="游ゴシック" w:hAnsi="游ゴシック"/>`
		if bold {
			rpr += `<w:b/>`
		}
		if gray {
			rpr += `<w:color w:val="595959"/>`
		}
		rpr += fmt.Sprintf(`<w:sz w:val="%d"/><w:szCs w:val="%d"/>`, size, size)
		body.WriteString(`<w:p><w:pPr><w:spacing w:after="0"/></w:pPr><w:r><w:rPr>` + rpr + `</w:rPr><w:t xml:space="preserve">` + encodeText(text) + `</w:t></w:r></w:p>`)
	}
	para(title, true, 22, false)
	para(note, false, 18, true)
	para("", false, 21, false)
	for i, rows := range pages {
		para(fmt.Sprintf("■ %dページ目", i+1), true, 21, false)
		if len(rows) == 0 {
			para("（このページからは文字を読み取れませんでした）", false, 21, true)
		}
		for _, r := range rows {
			para(r, false, 21, false)
		}
		para("", false, 21, false)
	}
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body.String() +
		`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134" w:header="567" w:footer="567" w:gutter="0"/></w:sectPr></w:body></w:document>`
	files := []struct{ name, data string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/document.xml", doc},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(f.data)); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

// ---- PDF 1件の処理 ----

type pdfResult struct {
	pages     [][]string // masked rows
	counts    map[string]int
	suspects  []suspect
	leftovers []string
	emptyPg   []int
	chars     int
}

var reJunkRow = regexp.MustCompile(`^[\p{P}\p{S}\s]*$`)

func maskPDF(m *masker, pdfPath, workDir string) (*pdfResult, error) {
	pages, err := runOCR(pdfPath, workDir)
	if err != nil {
		return nil, err
	}
	res := &pdfResult{counts: map[string]int{}}
	// 書類ごとの見方（改善C：下の名前が同じ別の人・薬情・フリガナ欄の読み）
	var all []string
	for _, p := range pages {
		for _, r := range pageRows(p) {
			all = append(all, fixOCRDigits(r))
		}
	}
	m.setDocContext(all, nil)
	prev, prevMR := "", ""
	for pi, p := range pages {
		rows := pageRows(p)
		var out []string
		for _, r := range rows {
			if reJunkRow.MatchString(r) {
				continue
			}
			r = fixOCRDigits(r)
			res.chars += utf8.RuneCountInString(r)
			ms := m.findMatches(r, true)
			if bm := m.birthNext(prev, r); len(bm) > 0 && (len(ms) == 0 || ms[0].start >= bm[0].end) {
				ms = append(bm, ms...)
			}
			ms = m.addrContinue(prev, r, ms) // 前の行の住所に続く建物名（改善6）
			prev = r
			mr := applyMatches(r, ms)
			res.suspects = append(res.suspects, m.addrContSuspect(prevMR, mr)...)
			fs := m.drugFilter(m.fieldCellSuspect(prevMR, mr)) // 見出しだけの行の次の行（改善C No.40）
			prevMR = mr
			for _, x := range ms {
				res.counts[x.kind+"\t"+x.orig+"\t"+x.repl]++
			}
			rowSus := m.findSuspects(mr)
			if len(fs) > 0 {
				rowSus = m.mergeSus(rowSus, fs) // 同じ所の短い候補は、欄の中身（長い方）にまとめる
			}
			res.suspects = append(res.suspects, rowSus...)
			res.suspects = append(res.suspects, m.drugFilter(m.tokenNeighborSuspects(mr))...) // 薬情の語は外す（改善C No.38）
			res.leftovers = append(res.leftovers, m.leftoverCheckFiltered(fmt.Sprintf("%dページ目", pi+1), mr)...)
			out = append(out, mr)
		}
		if len(out) == 0 {
			res.emptyPg = append(res.emptyPg, pi+1)
		}
		res.pages = append(res.pages, out)
	}
	return res, nil
}

var ocrDigitMap = map[rune]rune{'O': '0', 'o': '0', 'l': '1', 'I': '1', '|': '1', 'B': '8'}

func isASCIILetter(r rune) bool { return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') }

// fixOCRDigits turns letters misread inside numbers back into digits (「O46-234」→「046-234」、「567B」→「5678」),
// but only where the result is a phone number / long number / postal code; 「O2 2L/分」「B5用紙」は変えない。
func fixOCRDigits(s string) string {
	fixed := fixOCRDigitsAll(s)
	if fixed == s {
		return s
	}
	fr, orig := []rune(fixed), []rune(s)
	keep := make([]bool, len(fr))
	for _, re := range []*regexp.Regexp{rePhoneHy, rePhoneSp, reLongNum, reZip} {
		for _, ix := range re.FindAllStringIndex(fixed, -1) {
			a := utf8.RuneCountInString(fixed[:ix[0]])
			b := a + utf8.RuneCountInString(fixed[ix[0]:ix[1]])
			for k := a; k < b; k++ {
				keep[k] = true
			}
		}
	}
	for k := range fr {
		if fr[k] != orig[k] && !keep[k] {
			fr[k] = orig[k]
		}
	}
	return string(fr)
}

func fixOCRDigitsAll(s string) string {
	rs := []rune(s)
	for i, r := range rs {
		d, ok := ocrDigitMap[r]
		if !ok {
			continue
		}
		var prev, next rune
		if i > 0 {
			prev = rs[i-1]
		}
		if i < len(rs)-1 {
			next = rs[i+1]
		}
		pd, nd := isDigitRune(prev), isDigitRune(next)
		if (pd && nd) || (nd && !isASCIILetter(prev)) || (pd && !isASCIILetter(next)) {
			rs[i] = d
		}
	}
	return string(rs)
}

var (
	reTokBefore = regexp.MustCompile(`([\p{Han}ー]{1,3})(【[^【】]+】)`)
	reTokAfter  = regexp.MustCompile(`(【[^【】]+】)([\p{Han}ー]{1,3})`)
)

var okAfterTok = "様氏殿宅方家邸宛君"

// 名前の記号の前後にくっつきやすい、時・動作の語（「本日【利用者001】」「【利用者001】入院中」）
var neighborWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`本日 昨日 今日 明日 午前 午後 夕方 朝 夜 翌日 前日 同日 当日 毎日 先日 先週 今週 来週 今月 先月 来月 以前 以後 入院 退院 入所 退所 来所 来訪 同席 同行 訪問 受診 通院 帰宅 在宅 不在 外出 転倒 死亡 逝去 永眠 面談 連絡 電話 相談 報告 説明 確認 了承 同意 希望 本人 自宅 宅 方 対応 介助 支援 利用 契約 入浴 食事 排泄 服薬 送迎 要望 意向 状態 様子 責 サ責`) {
		neighborWords[w] = true
	}
}

func isNeighborWord(w string) bool {
	if neighborWords[w] {
		return true
	}
	for _, suf := range []string{"中", "後", "時", "前", "済", "予定"} {
		if t := strings.TrimSuffix(w, suf); t != w && (t == "" || neighborWords[t]) {
			return true
		}
	}
	for n := range neighborWords {
		if utf8.RuneCountInString(n) >= 2 && (strings.HasSuffix(w, n) || strings.HasPrefix(w, n)) {
			return true
		}
	}
	return false
}

// FAXのチェック欄「□」は、読み取りで漢字の「口」になる（R8.9.29 資料で確認）。
// 名前の前後に1文字だけ「口」が付いているのはチェック欄なので、確かめる対象にしない
// （除外は2文字以上の決まりなので、氏名一覧では消せなかった）。
func isCheckboxGlyph(w string) bool {
	// 口（くち）U+53E3・囗（くにがまえ）U+56D7・部首の⼝ U+2F1D／⼞ U+2F1E。どれも見た目は「□」
	switch w {
	case "\u53e3", "\u56d7", "\u2f1d", "\u2f1e":
		return true
	}
	return false
}

// tokenNeighborSuspects: in OCR text, kanji stuck to a replaced name may be the rest of a
// misread name (「遠籐【利用者001の名】」「【姓001】太ー」).
func (m *masker) tokenNeighborSuspects(s string) []suspect {
	var out []suspect
	for _, ix := range reTokBefore.FindAllStringSubmatchIndex(s, -1) {
		w := s[ix[2]:ix[3]]
		if !m.nameTok(s[ix[4]:ix[5]]) || isCheckboxGlyph(w) || m.excludeOne[w] || endsWithPersonNoun(w, s[:ix[2]]) || relationWord(w) || isNeighborWord(w) || stopWords[w] || m.exclude[w] {
			continue
		}
		out = append(out, suspect{word: s[ix[0]:ix[1]], context: contextAround(s, ix[0], ix[1]), tok: s[ix[4]:ix[5]], reason: "置き換えた名前のすぐ前に漢字「" + w + "」が残っています（読み取りの誤りで名前の一部が残った可能性）。名前の一部でなければ「" + w + "」を氏名一覧の「除外」に書いてください"})
	}
	for _, ix := range reTokAfter.FindAllStringSubmatchIndex(s, -1) {
		w := s[ix[4]:ix[5]]
		if !m.nameTok(s[ix[2]:ix[3]]) {
			continue
		}
		first, _ := utf8.DecodeRuneInString(w)
		if strings.ContainsRune(okAfterTok, first) || isCheckboxGlyph(w) || m.excludeOne[w] || isNeighborWord(w) || stopWords[w] || m.exclude[w] {
			continue
		}
		out = append(out, suspect{word: s[ix[0]:ix[1]], context: contextAround(s, ix[0], ix[1]), tok: s[ix[2]:ix[3]], reason: "置き換えた名前のすぐ後に漢字「" + w + "」が残っています（読み取りの誤りで名前の一部が残った可能性）。名前の一部でなければ「" + w + "」を氏名一覧の「除外」に書いてください"})
	}
	return out
}

var relationWords = strings.Fields(`長男 長女 次男 次女 三男 三女 妻 夫 父 母 娘 息子 孫 嫁 婿 兄 姉 弟 妹 甥 姪 叔父 叔母 伯父 伯母 祖父 祖母 義父 義母 本人 利用者 家族 担当 主治医 医師 看護師 相談員 氏名 名前 宛 様 氏 殿 故 亡 旧姓 通称`)

func relationWord(w string) bool {
	// 「責任者」「記入者」「相談員」「看護師」「所長」など、名前の前に付く肩書き
	for _, suf := range []string{"者", "員", "師", "長", "人", "士", "官", "係", "名"} {
		if strings.HasSuffix(w, suf) {
			return true
		}
	}
	for _, r := range relationWords {
		if strings.HasSuffix(w, r) {
			return true
		}
	}
	return false
}
