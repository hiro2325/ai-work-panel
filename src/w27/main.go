package main

import (
	"bufio"
	_ "embed"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

//go:embed template_namelist.xlsx
var templateNameList []byte

// mode is set at build time: "mask" or "restore"
var mode = "mask"

const aiFolderName = "AI作業"

type paths struct {
	work, src, done, out, table, report, nameList, codeBookF, cellCfg, stage string
	ai, aiIn, aiOut                                                          string
}

func findAIFolder() string {
	if v := os.Getenv("MASKTOOL_AI_DIR"); v != "" {
		return v
	}
	exe, err := os.Executable()
	if err == nil {
		d := filepath.Dir(exe)
		for i := 0; i < 6; i++ {
			if filepath.Base(d) == aiFolderName {
				return d
			}
			nd := filepath.Dir(d)
			if nd == d {
				break
			}
			d = nd
		}
	}
	var cands []string
	if od := os.Getenv("OneDrive"); od != "" {
		cands = append(cands, filepath.Join(od, "デスクトップ", aiFolderName), filepath.Join(od, "Desktop", aiFolderName))
	}
	if h, err := os.UserHomeDir(); err == nil {
		cands = append(cands, filepath.Join(h, "OneDrive", "デスクトップ", aiFolderName), filepath.Join(h, "Desktop", aiFolderName))
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return ""
}

func setupPaths() (*paths, error) {
	home := os.Getenv("MASKTOOL_WORK_DIR")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(h, "マスキング作業")
	}
	p := &paths{work: home}
	p.src = filepath.Join(home, "1_元データ")
	p.done = filepath.Join(p.src, "処理済み")
	p.out = filepath.Join(home, "2_完成")
	p.table = filepath.Join(home, "対応表")
	p.report = filepath.Join(home, "確認リスト")
	p.stage = filepath.Join(home, "3_確認待ち")
	p.nameList = filepath.Join(p.table, "氏名一覧.xlsx")
	p.codeBookF = filepath.Join(p.table, "記号対応表.tsv")
	p.cellCfg = filepath.Join(p.table, "登録セル.txt")
	p.ai = findAIFolder()
	if p.ai == "" {
		return nil, fmt.Errorf("「%s」フォルダが見つかりません。このツールを %s の中に置いて実行してください", aiFolderName, aiFolderName)
	}
	p.aiIn = filepath.Join(p.ai, "30_記入係作業中")
	p.aiOut = filepath.Join(p.ai, "40_合格・確認待ち")
	// safety: the work folder must never be inside the AI folder
	if rel, err := filepath.Rel(p.ai, home); err == nil && !strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("安全のため中止します：作業フォルダがAI作業フォルダの中にあります")
	}
	for _, d := range []string{p.src, p.done, p.out, p.table, p.report, p.stage} {
		if err := os.MkdirAll(d, 0700); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func pause() {
	if os.Getenv("MASKTOOL_NOPAUSE") != "" {
		return
	}
	fmt.Println()
	fmt.Print("Enterキーを押すと閉じます。")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

func openInExplorer(target string) {
	if runtime.GOOS != "windows" || os.Getenv("MASKTOOL_NOPAUSE") != "" {
		return
	}
	exec.Command("explorer", target).Start()
}

func uniquePath(dir, name string) string {
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		p = filepath.Join(dir, fmt.Sprintf("%s(%d)%s", base, i, ext))
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
	}
}

func listTargets(dir string, withPDF bool) (targets, skipped []string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() || strings.HasPrefix(e.Name(), "~$") {
			continue
		}
		if isTargetExt(e.Name()) || (withPDF && strings.EqualFold(filepath.Ext(e.Name()), ".pdf")) {
			targets = append(targets, e.Name())
		} else if isOldExt(e.Name()) {
			skipped = append(skipped, e.Name())
		}
	}
	sort.Strings(targets)
	return
}

func main() {
	fmt.Println("==============================================")
	if mode == "panel" {
		fmt.Println("  AI作業パネル（ブラウザ画面）")
	} else if mode == "restore" {
		fmt.Println("  個人情報マスキングツール【元に戻す】")
	} else {
		fmt.Println("  個人情報マスキングツール【置き換え】")
	}
	fmt.Println("  （このパソコンの中だけで動きます。通信しません）")
	fmt.Println("==============================================")
	p, err := setupPaths()
	if err != nil {
		fmt.Println("エラー：", err)
		pause()
		os.Exit(1)
	}
	if mode == "panel" {
		err = runPanel(p)
	} else if mode == "restore" {
		err = runRestore(p)
	} else {
		err = runMask(p)
	}
	if err != nil {
		fmt.Println()
		fmt.Println("エラー：", err)
	}
	pause()
}

// ---------------- mask ----------------

type fileSummary struct {
	name, outName string
	counts        map[string]int // "種類\t元\t記号" -> n
	suspects      []suspect
	media         int
	err           string
	leftover      []string
	unknownTok    []string
	held          bool
	total         int
	hiddenSus     int
	pdf           bool
	pdfPages      int
	emptyPages    []int
	preview       []string  // 置き換え後の文（確認画面用。個人情報はこのパソコンの中だけ）
	poorPages     []int     // PDF：読み取りがうまくいかなかった可能性のあるページ
	report        string    // この処理の確認リストのファイル名（片づけの紐付け用。中身には関係しない）
	lefts         []leftHit // 残りの場所と種類（改善12 No.33・36）
}

func runMask(p *paths) error {
	_, _, err := runMaskTo(p, p.aiIn, true)
	return err
}

// runMaskTo: dest に置き換え済みファイルを置く。interactive=false のときは確認リストを開かない。
func runMaskTo(p *paths, dest string, interactive bool) ([]*fileSummary, []string, error) {
	if _, err := os.Stat(p.nameList); os.IsNotExist(err) {
		if err := os.WriteFile(p.nameList, templateNameList, 0600); err != nil {
			return nil, nil, err
		}
		fmt.Println()
		fmt.Println("はじめての実行です。準備用のフォルダと「氏名一覧.xlsx」を作りました。")
		fmt.Println("  場所：", p.work)
		fmt.Println("1) 対応表フォルダの「氏名一覧.xlsx」に、利用者・家族の氏名を書いて保存してください。")
		fmt.Println("2) マスキングしたいExcel・Word・PDFを「1_元データ」フォルダに入れてください。")
		fmt.Println("3) もう一度このツールを実行してください。")
		if interactive {
			openInExplorer(p.work)
		}
		return nil, nil, nil
	}
	people, excl, err := readNameList(p.nameList)
	if err != nil {
		return nil, nil, err
	}
	cb, err := loadCodeBook(p.codeBookF)
	if err != nil {
		return nil, nil, fmt.Errorf("記号対応表が読めません: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		return nil, nil, fmt.Errorf("渡し先フォルダがありません: %s", dest)
	}
	targets, skipped := listTargets(p.src, true)
	nAddr := 0
	for _, pp := range people {
		if strings.TrimSpace(pp.kind) == addrKind {
			nAddr++
		}
	}
	if nAddr > 0 {
		fmt.Printf("\n氏名一覧：%d人分（除外する語 %d件・住所 %d件）\n", len(people)-nAddr, len(excl), nAddr)
	} else {
		fmt.Printf("\n氏名一覧：%d人分（除外する語 %d件）\n", len(people), len(excl))
	}
	if len(people) == 0 {
		fmt.Println("※ 氏名一覧が空です。電話番号などの形で見つかるものだけ置き換えます。")
	}
	if len(targets) == 0 {
		fmt.Println("\n「1_元データ」に Excel（.xlsx/.xlsm）・Word（.docx）・PDF がありません。")
		if interactive {
			openInExplorer(p.src)
		}
		return nil, skipped, nil
	}
	m := newMasker(people, excl, cb)
	cfg, err := loadCellConfig(p.cellCfg)
	if err != nil {
		return nil, skipped, fmt.Errorf("登録セル.txt を読めません: %v", err)
	}
	var sums []*fileSummary
	var fatal error
	for _, name := range targets {
		fmt.Printf("\n処理中：%s\n", name)
		s := &fileSummary{name: name, counts: map[string]int{}}
		sums = append(sums, s)
		// file name: names only
		base := strings.TrimSuffix(name, filepath.Ext(name))
		// 書類ごとの見方（改善C：下の名前が同じ別の人・薬情・読み）。PDF は読み取ったあとに決める
		m.clearDocContext()
		if !strings.EqualFold(filepath.Ext(name), ".pdf") {
			lines, rph := docTextOOXML(filepath.Join(p.src, name))
			m.setDocContext(append(lines, base), rph)
		}
		nbase := applyMatches(base, m.findMatches(base, false))
		outExt := filepath.Ext(name)
		outBase := nbase
		var tmpOut string
		if strings.EqualFold(outExt, ".pdf") {
			outExt = ".docx"
			outBase = nbase + "（読取）"
			tmpOut = filepath.Join(p.work, ".作業中_"+fmt.Sprint(time.Now().UnixNano())+".docx")
			fmt.Println("  PDFの文字を読み取っています（1ページにつき数秒かかります）…")
			pr, err := maskPDF(m, filepath.Join(p.src, name), p.work)
			if err != nil {
				s.err = err.Error()
				fmt.Println("  → 失敗：", err)
				continue
			}
			for k, v := range pr.counts {
				s.counts[k] += v
			}
			s.suspects = append(pr.suspects, m.findSuspects(nbase)...)
			s.leftover = pr.leftovers
			s.lefts = m.pdfLeftHits(pr.leftovers)
			s.pdf = true
			s.pdfPages = len(pr.pages)
			s.emptyPages = pr.emptyPg
			for pi, pg := range pr.pages {
				if poorOCR(pg) {
					s.poorPages = append(s.poorPages, pi+1)
				}
				for _, r := range pg {
					if len(s.preview) < 80 && (strings.Contains(r, "【") || len(m.findSuspects(r)) > 0) {
						s.preview = append(s.preview, r)
					}
				}
			}
			note := "このWordは、FAX・スキャンのPDFから文字を読み取り、個人情報を記号に置き換えたものです。読み取りの誤りが含まれることがあります。表の形・手書きの文字・印影・図は含まれません。"
			if err := writeSimpleDocx(tmpOut, "【読み取り・置き換え済み】元ファイル："+nbase+filepath.Ext(name), note, pr.pages); err != nil {
				os.Remove(tmpOut)
				s.err = "Wordを作れませんでした: " + err.Error()
				continue
			}
		} else {
			tmpOut = filepath.Join(p.work, ".作業中_"+fmt.Sprint(time.Now().UnixNano())+filepath.Ext(name))
			var fileSus []suspect
			prevText, prevMaskedText := "", ""
			var prevPos [2]int
			tf := func(t string, susOK bool, hdr bool) []match {
				ms := m.findMatchesHdr(t, true, hdr)
				if bm := m.birthNext(prevText, t); len(bm) > 0 && (len(ms) == 0 || ms[0].start >= bm[0].end) {
					ms = append(bm, ms...)
				}
				pos := groupPos()
				adj := adjacentGroup(prevPos, pos)
				prevPos = pos
				if adj {
					ms = m.addrContinue(prevText, t, ms) // 同じ部品のすぐ前の段落・セルの住所に続く建物名（改善6）
				}
				prevMasked := prevMaskedText
				prevText = t
				prevMaskedText = applyMatches(t, ms)
				if susOK {
					ss := m.findSuspects(applyMatches(t, ms))
					ss = append(ss, m.labelCellSuspect(prevMasked, prevMaskedText)...)
					ss = m.mergeSus(ss, m.drugFilter(m.fieldCellSuspect(prevMasked, prevMaskedText))) // 見出しだけの欄の次の欄の中身（改善C No.40）
					if adj {
						ss = append(ss, m.addrContSuspect(prevMasked, prevMaskedText)...)
					}
					fileSus = append(fileSus, ss...)
					if (len(ms) > 0 || len(ss) > 0) && len(s.preview) < 80 {
						s.preview = append(s.preview, applyMatches(t, ms))
					}
				} else {
					s.hiddenSus += len(m.findSuspects(applyMatches(t, ms)))
				}
				return ms
			}
			res, err := processOOXML(filepath.Join(p.src, name), tmpOut, tf, m.rawMask, m.leftoverCheckFiltered, &zipOpts{m: m, cfg: cfg})
			if err != nil {
				s.err = err.Error()
				os.Remove(tmpOut)
				fmt.Println("  → 失敗：", err)
				continue
			}
			for _, r := range res.reports {
				for _, mt := range r.matches {
					s.counts[mt.kind+"\t"+mt.orig+"\t"+mt.repl]++
				}
			}
			if res.rawCount > 0 {
				s.counts["その他の場所\t（シート名・数式・リンク等）\t記号"] += res.rawCount
			}
			s.suspects = append(fileSus, m.findSuspects(nbase)...)
			for _, ns := range res.numSus {
				if val := ns[strings.LastIndex(ns, "：")+len("："):]; m.exclude[val] {
					continue
				}
				s.suspects = append(s.suspects, suspect{word: ns, context: "数字だけのセル", reason: "8けた以上の数字（被保険者番号・電話番号の可能性）。個人情報でなければ、その数字を氏名一覧に「除外」で登録してください"})
			}
			for _, u := range res.unprocessable {
				s.suspects = append(s.suspects, suspect{word: u, context: "中を確認できない部分", reason: "このツールでは中身を置き換えられません。埋め込み（貼り付けたExcel・グラフなど）やマクロを外すか、.xlsx/.docx で保存し直してください"})
			}
			for _, b := range res.birthSus {
				s.suspects = append(s.suspects, suspect{word: b, context: "生年月日の欄", reason: "生年月日が置き換えられずに残っているようです。この様式の生年月日を入力するセル（自動計算のセルではなく、手で入力するセル）を、対応表フォルダの「登録セル.txt」に追加してから、もう一度実行してください"})
			}
			s.media = res.mediaCount
			s.leftover = res.leftovers
			s.lefts = res.leftHits
		}
		if len(m.nameMatches(nbase)) > 0 {
			s.leftover = append(s.leftover, "ファイル名")
			s.lefts = append(s.lefts, leftHit{Loc: "ファイル名", Kind: lkOther, Reg: true, Shape: "氏名一覧に登録した名前・住所", LogLoc: "ファイル名"})
		}
		if len(s.leftover) > 0 {
			os.Remove(tmpOut)
			// 場所は「何シートの何セル」「何段落目」で出す（xml のファイル名は出さない。改善12 No.33）
			var locs []string
			for _, h := range s.lefts {
				if h.Word != "" {
					locs = append(locs, h.Loc+"：「"+h.Word+"」")
				} else {
					locs = append(locs, h.Loc)
				}
			}
			if len(locs) == 0 {
				locs = s.leftover
			}
			s.err = "置き換えた後も個人情報が残る所があったため、AIには渡していません：" + strings.Join(locs, "、")
			fmt.Println("  → 中止：", s.err)
			continue
		}
		if len(s.suspects) > 0 {
			// 要確認があるうちはAIに渡さない
			os.Remove(tmpOut)
			s.held = true
			fmt.Printf("  → 要確認が %d 件あるため、AIには渡さず保留しました（元データはそのまま残しています）\n", len(s.suspects))
			continue
		}
		// 1) 対応表を先に保存（保存できなければ渡さない）
		if cb.dirty {
			if err := saveCodeBook(p.codeBookF, cb); err != nil {
				os.Remove(tmpOut)
				s.err = "記号対応表を保存できませんでした（Excelなどで開いていたら閉じてください）。このファイルは渡していません"
				fatal = fmt.Errorf("記号対応表を保存できません: %v", err)
				break
			}
			cb.dirty = false
		}
		// 2) 元データを処理済みへ移す（移せなければ渡さない＝二重処理を防ぐ）
		donePath := uniquePath(p.done, name)
		if err := os.Rename(filepath.Join(p.src, name), donePath); err != nil {
			os.Remove(tmpOut)
			s.err = "元データを「処理済み」へ移せませんでした。ファイルを開いていたら閉じて、もう一度実行してください"
			fmt.Println("  → 中止：", s.err)
			continue
		}
		// 3) AIフォルダへ（書き終えてから名前を付ける）
		outPath := uniquePath(dest, outBase+outExt)
		if err := copyFile(tmpOut, outPath); err != nil {
			os.Remove(tmpOut)
			os.Rename(donePath, filepath.Join(p.src, name))
			s.err = "30_記入係作業中 に保存できませんでした: " + err.Error()
			fmt.Println("  → 中止：", s.err)
			continue
		}
		os.Remove(tmpOut)
		s.outName = filepath.Base(outPath)
		n := 0
		for _, c := range s.counts {
			n += c
		}
		fmt.Printf("  → %d か所を置き換えました。%s に「%s」として置きました\n", n, filepath.Base(dest), s.outName)
	}
	if cb.dirty && fatal == nil {
		if err := saveCodeBook(p.codeBookF, cb); err != nil {
			fatal = fmt.Errorf("記号対応表を保存できません: %v", err)
		}
	}
	rp := filepath.Join(p.report, "確認リスト_置き換え_"+time.Now().Format("20060102_150405")+".html")
	werr := os.WriteFile(rp, []byte(maskReportHTML(sums, skipped)), 0600)
	if werr == nil && interactive {
		fmt.Println("\n確認リストを開きます：", rp)
		openInExplorer(rp)
	}
	if werr == nil {
		for _, s := range sums {
			s.report = filepath.Base(rp)
		}
	}
	return sums, skipped, fatal
}

func applyMatches(s string, ms []match) string {
	if len(ms) == 0 {
		return s
	}
	var b strings.Builder
	pos := 0
	for _, x := range ms {
		b.WriteString(s[pos:x.start])
		b.WriteString(x.repl)
		pos = x.end
	}
	b.WriteString(s[pos:])
	return b.String()
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	part := dst + ".part"
	if err := os.WriteFile(part, b, 0644); err != nil {
		os.Remove(part)
		return err
	}
	if err := os.Rename(part, dst); err != nil {
		os.Remove(part)
		return err
	}
	return nil
}

// ---------------- restore ----------------

func runRestore(p *paths) error {
	_, err := runRestoreDir(p, p.aiOut, true)
	return err
}

// runRestoreDir: dir の中の書類を元に戻して 2_完成 に置く。
func runRestoreDir(p *paths, dir string, interactive bool) ([]*fileSummary, error) {
	cb, err := loadCodeBook(p.codeBookF)
	if err != nil {
		return nil, err
	}
	if len(cb.codeToOrig) == 0 {
		return nil, fmt.Errorf("記号対応表が空です。先に【置き換え】を実行してください")
	}
	targets, _ := listTargets(dir, false)
	if len(targets) == 0 {
		fmt.Println("\n「" + filepath.Base(dir) + "」に Excel・Word がありません。")
		return nil, nil
	}
	var sums []*fileSummary
	for _, name := range targets {
		s := &fileSummary{name: name, counts: map[string]int{}}
		base := strings.TrimSuffix(name, filepath.Ext(name))
		bm, _ := restoreMatches(base, cb)
		nbase := applyMatches(base, bm)
		tmpOut := filepath.Join(p.work, ".作業中_"+fmt.Sprint(time.Now().UnixNano())+filepath.Ext(name))
		res, err := processOOXML(filepath.Join(dir, name), tmpOut, func(t string, _ bool, _ bool) []match {
			ms, unk := restoreMatches(t, cb)
			s.unknownTok = append(s.unknownTok, unk...)
			return ms
		}, func(_, x string) (string, int) { return rawRestore(x, cb) }, nil, &zipOpts{restoreCB: cb})
		if err != nil {
			os.Remove(tmpOut)
			s.err = err.Error()
			sums = append(sums, s)
			continue
		}
		total := len(bm) + res.rawCount
		for _, r := range res.reports {
			total += len(r.matches)
			for _, mt := range r.matches {
				s.counts["戻す\t"+mt.orig+"\t"+mt.repl]++
			}
		}
		s.total = total
		if total == 0 && len(s.unknownTok) == 0 {
			os.Remove(tmpOut)
			continue // 記号を含まないファイル（ツール自身など）は対象外
		}
		fmt.Printf("\n処理中：%s\n", name)
		outPath := uniquePath(p.out, nbase+filepath.Ext(name))
		if err := copyFile(tmpOut, outPath); err != nil {
			s.err = err.Error()
		} else {
			s.outName = filepath.Base(outPath)
			fmt.Printf("  → %d か所を元に戻し、「2_完成」に置きました\n", total)
		}
		os.Remove(tmpOut)
		sums = append(sums, s)
	}
	if len(sums) == 0 {
		fmt.Println("\n記号（【利用者001】など）を含むファイルはありませんでした。")
		return nil, nil
	}
	rp := filepath.Join(p.report, "確認リスト_元に戻す_"+time.Now().Format("20060102_150405")+".html")
	werr := os.WriteFile(rp, []byte(restoreReportHTML(sums)), 0600)
	if werr == nil && interactive {
		openInExplorer(rp)
	}
	if werr == nil {
		for _, s := range sums {
			s.report = filepath.Base(rp)
		}
	}
	if interactive {
		openInExplorer(p.out)
	}
	return sums, nil
}

// ---------------- reports ----------------

const css = `<style>body{font-family:"Yu Gothic UI","Meiryo",sans-serif;margin:24px;color:#222;line-height:1.6}h1{font-size:22px}h2{font-size:18px;margin-top:28px;border-bottom:2px solid #ccc}table{border-collapse:collapse;margin:8px 0}td,th{border:1px solid #bbb;padding:4px 8px;font-size:14px}th{background:#f0f0f0}.warn{background:#fff3cd;padding:8px 12px;border-left:4px solid #e0a800}.err{background:#fde2e2;padding:8px 12px;border-left:4px solid #c00}.ok{color:#1a7f37}.sus{background:#ffe8a3}</style>`

func maskReportHTML(sums []*fileSummary, skipped []string) string {
	var b strings.Builder
	b.WriteString("<!doctype html><html lang=ja><meta charset=utf-8><title>確認リスト（置き換え）</title>" + css + "<body>")
	b.WriteString("<h1>確認リスト（置き換え）" + time.Now().Format("2006/01/02 15:04") + "</h1>")
	b.WriteString("<p>このリストには個人情報が含まれます。AI作業には移さないでください。</p>")
	for _, s := range sums {
		b.WriteString("<h2>" + html.EscapeString(s.name) + "</h2>")
		if s.err != "" {
			b.WriteString("<p class=err>処理できませんでした：" + html.EscapeString(s.err) + "</p>")
			continue
		}
		if s.held {
			b.WriteString("<p class=err><b>保留（AIには渡していません）</b>：要確認が " + fmt.Sprint(len(s.suspects)) + " 件あります。下の表を見て、氏名一覧.xlsx を直してから、もう一度【置き換え】を実行してください。元データは 1_元データ にそのまま残っています。<br>・個人の名前なら → 氏名一覧に「利用者・家族・その他」で追加<br>・名前ではない、またはAIに渡してよい語なら → 氏名一覧に「除外」で追加（「様」「さん」は付けずに書く）</p><table><tr><th>語</th><th>前後</th><th>理由</th></tr>")
		} else {
			b.WriteString("<p class=ok>30_記入係作業中 に「" + html.EscapeString(s.outName) + "」として置きました。</p>")
		}
		if len(s.suspects) > 0 {
			for _, x := range s.suspects {
				b.WriteString("<tr><td class=sus>" + html.EscapeString(x.word) + "</td><td>" + html.EscapeString(x.context) + "</td><td>" + html.EscapeString(x.reason) + "</td></tr>")
			}
			b.WriteString("</table>")
		} else {
			b.WriteString("<p>要確認：なし</p>")
		}
		if s.held {
			continue
		}
		if s.pdf {
			b.WriteString("<p>PDF（" + fmt.Sprint(s.pdfPages) + "ページ）から文字を読み取り、Wordにして渡しました。手書きの文字・印影・図・表の形はWordに含まれません（AIには渡りません）。読み取れなかった部分は、必要なら元のPDFを見て補ってください。</p>")
			if len(s.emptyPages) > 0 {
				b.WriteString("<p class=warn>文字をほとんど読み取れなかったページがあります：" + fmt.Sprint(s.emptyPages) + " ページ目（手書き・薄い印刷・写真の可能性）。</p>")
			}
		}
		if s.hiddenSus > 0 {
			b.WriteString("<p>（参考）隠しシート（郵便番号マスタなど）にも名前らしき語が " + fmt.Sprint(s.hiddenSus) + " 件ありました。隠しシートは地名などの一覧が多いため、保留の対象にはしていません。隠しシートに利用者の情報を入れている場合は、その語を氏名一覧に登録してください。</p>")
		}
		if s.media > 0 {
			b.WriteString("<p class=warn>画像が " + fmt.Sprint(s.media) + " 個含まれています。画像の中の文字（写真・印影・スキャン画像など）は置き換えられません。目で確認してください。</p>")
		}
		if len(s.counts) > 0 {
			b.WriteString("<table><tr><th>種類</th><th>元の文字列</th><th>記号</th><th>か所</th></tr>")
			keys := make([]string, 0, len(s.counts))
			for k := range s.counts {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				f := strings.SplitN(k, "\t", 3)
				b.WriteString("<tr><td>" + html.EscapeString(f[0]) + "</td><td>" + html.EscapeString(f[1]) + "</td><td>" + html.EscapeString(f[2]) + "</td><td>" + fmt.Sprint(s.counts[k]) + "</td></tr>")
			}
			b.WriteString("</table>")
		} else {
			b.WriteString("<p>置き換えた箇所はありませんでした。</p>")
		}
	}
	if len(skipped) > 0 {
		b.WriteString("<h2>対象外のファイル</h2><p>次のファイルは今回の対象外です（.doc/.xls は .docx/.xlsx で保存し直すと処理できます）。</p><ul>")
		for _, n := range skipped {
			b.WriteString("<li>" + html.EscapeString(n) + "</li>")
		}
		b.WriteString("</ul>")
	}
	b.WriteString("</body></html>")
	return b.String()
}

func restoreReportHTML(sums []*fileSummary) string {
	var b strings.Builder
	b.WriteString("<!doctype html><html lang=ja><meta charset=utf-8><title>確認リスト（元に戻す）</title>" + css + "<body>")
	b.WriteString("<h1>確認リスト（元に戻す）" + time.Now().Format("2006/01/02 15:04") + "</h1>")
	for _, s := range sums {
		b.WriteString("<h2>" + html.EscapeString(s.name) + "</h2>")
		if s.err != "" {
			b.WriteString("<p class=err>処理できませんでした：" + html.EscapeString(s.err) + "</p>")
			continue
		}
		b.WriteString("<p class=ok>2_完成 に「" + html.EscapeString(s.outName) + "」として置きました。</p>")
		if len(s.unknownTok) > 0 {
			seen := map[string]bool{}
			b.WriteString("<div class=warn><b>戻せなかった記号があります</b>（対応表にない記号です。AIが記号を書き換えた可能性があります。手で直してください）：")
			for _, t := range s.unknownTok {
				if !seen[t] {
					b.WriteString(" " + html.EscapeString(t))
					seen[t] = true
				}
			}
			b.WriteString("</div>")
		}
		n := 0
		for _, c := range s.counts {
			n += c
		}
		b.WriteString("<p>元に戻した箇所：" + fmt.Sprint(s.total) + " か所</p>")
	}
	b.WriteString("</body></html>")
	return b.String()
}

// poorOCR: 読み取った文字にひらがながほとんどない（向きが違う・かすれている）ページ
func poorOCR(rows []string) bool {
	hira, all := 0, 0
	for _, r := range rows {
		for _, c := range r {
			if c >= 'ぁ' && c <= 'ゖ' {
				hira++
			}
			if isWide(c) {
				all++
			}
		}
	}
	return all >= 10 && hira < 5
}
