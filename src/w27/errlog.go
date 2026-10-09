package main

// エラーの記録をAIに届ける（実名なし。改善12 No.43）
// AI作業＼00_指示＼パネルのエラー記録＼エラー記録_R8.9.30.md（1日1ファイル・追記）
//
// 書いてよいのは：時刻、画面（①〜⑥）、エラーの種類、ファイルの種類（.xlsx など）と書類の種類（様式から分かれば）、
//               シート名（様式の決まった名前だけ）・セル番地、利用者の記号（【利用者017】など）、決まりの名前
// 書かないもの：ファイル名（「ファイル1（.xlsx）」のように番号で書く）、残った語そのもの（「8けたの数字」のように形だけ）、
//               元の文、実名、住所、電話
// 記録の1行は、決まった部品（下の関数が作る文）だけで組み立てる。最後に念のため、氏名一覧の名前・番号などの形と、
// 今あるファイルの名前が紛れていないかを確かめ、見つかったら伏せる。

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const errLogDirName = "パネルのエラー記録"

func (s *panelServer) errLogPath(t time.Time) string {
	return filepath.Join(s.p.ai, "00_指示", errLogDirName, "エラー記録_"+warekiDate(t)+".md")
}

var reOnlyUserCode = regexp.MustCompile(`【利用者\d{3,}】`)

// errLogScrub: 念のための見張り（組み立てた文に、名前・番号・ファイル名が紛れていたら伏せる）
func (s *panelServer) errLogScrub(line string) string {
	// 利用者の記号以外の【…】は外す（記号の中に元の語が入ることはないが、形をそろえる）
	line = reTokenAny.ReplaceAllStringFunc(line, func(t string) string {
		if reOnlyUserCode.MatchString(t) {
			return t
		}
		return "【記号】"
	})
	// 今あるファイルの名前（元データ・渡す前・処理済み・完成）
	var names []string
	for _, d := range []string{s.p.src, s.p.stage, s.p.done, s.p.out} {
		for _, f := range listFiles(d) {
			names = append(names, f, strings.TrimSuffix(f, filepath.Ext(f)))
		}
	}
	sort.Slice(names, func(a, b int) bool { return len(names[a]) > len(names[b]) })
	for _, n := range names {
		if len([]rune(n)) >= 4 && strings.Contains(line, n) {
			line = strings.ReplaceAll(line, n, "（ファイル名は書きません）")
		}
	}
	// 氏名一覧の名前・呼び名・住所、電話・番号・生年月日などの形
	if people, excl, err := readNameList(s.p.nameList); err == nil {
		if cb, err := loadCodeBook(s.p.codeBookF); err == nil {
			m := newMasker(people, excl, cb)
			var ms []match
			for _, x := range m.findMatches(line, true) {
				if reOnlyUserCode.MatchString(line[x.start:x.end]) {
					continue
				}
				x.repl = "（伏せました）"
				ms = append(ms, x)
			}
			line = applyMatches(line, ms)
		}
	}
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(line)
}

// errLog: 1行を追記する（書けなくても、パネルの作業は止めない）
func (s *panelServer) errLog(screen string, parts ...string) {
	if s.p.ai == "" {
		return
	}
	now := time.Now()
	dir := filepath.Join(s.p.ai, "00_指示", errLogDirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	path := s.errLogPath(now)
	var head string
	if _, err := os.Stat(path); os.IsNotExist(err) {
		head = "# パネルのエラー記録 " + warekiDate(now) + "\n\n" +
			"AI作業パネルで出た赤・橙・③の止め・画面のエラーの記録です（パネルが自動で書きます）。\n" +
			"実名・ファイル名・残った語・元の文は書きません（ファイルは「ファイル1（.xlsx）」のように番号で、語は「8けたの数字」のように形だけで書きます）。\n\n"
	}
	line := s.errLogScrub(now.Format("15:04") + "　" + screen + "　" + strings.Join(parts, "　"))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	io.WriteString(f, head+"- "+line+"\n")
}

// docKindOf: 書類の種類（様式から分かるときだけ。分からなければ空）
func docKindOf(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".pdf" {
		return "FAX・スキャン"
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return ""
	}
	defer zr.Close()
	has := func(t string, ws ...string) bool {
		for _, w := range ws {
			if strings.Contains(t, w) {
				return true
			}
		}
		return false
	}
	if ext == ".docx" {
		t := string(zipRead(&zr.Reader, "word/document.xml"))
		t = reTagStrip.ReplaceAllString(t, "")
		if len(t) > 6000 {
			t = t[:6000]
		}
		switch {
		case has(t, "支援経過"):
			return "支援経過"
		case has(t, "モニタリング"):
			return "モニタリング表"
		case has(t, "居宅サービス計画", "ケアプラン"):
			return "ケアプラン"
		case has(t, "アセスメント"):
			return "アセスメントシート"
		case has(t, "報告書"):
			return "事業所の報告書"
		case has(t, "文字起こし"):
			return "文字起こし"
		}
		return ""
	}
	_, _, names := sheetVisibility(&zr.Reader)
	var all []string
	for _, n := range names {
		all = append(all, n)
	}
	t := strings.Join(all, "|")
	switch {
	case has(t, "フェイスシート", "アセスメント"):
		return "アセスメントシート"
	case has(t, "モニタリング"):
		return "モニタリング表"
	case has(t, "第2表", "2表", "第１表", "第1表", "3表", "要点"):
		return "ケアプラン"
	case has(t, "支援経過"):
		return "支援経過"
	}
	return ""
}

// fileLabel: 記録に書くファイルの呼び方（名前は書かない）
func fileLabel(n int, name, kind string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = "拡張子なし"
	}
	if kind != "" {
		return fmt.Sprintf("ファイル%d（%s・%s）", n, ext, kind)
	}
	return fmt.Sprintf("ファイル%d（%s）", n, ext)
}

// susCategory: 橙の理由を、決まりの名前に直す（理由の文には語が入ることがあるので、そのままは書かない）
func susCategory(reason string) string {
	for _, c := range [][2]string{
		{"敬称", "敬称の付いた名前らしき語"}, {"氏名一覧の方の姓", "登録した方の姓と同じ語"}, {"氏名欄", "氏名欄の名前らしき語"},
		{"番地", "住所らしき語"}, {"8けた", "8けた以上の数字"}, {"このツールでは中身", "中を確認できない部分（埋め込み・マクロ）"},
		{"生年月日が置き換え", "生年月日の欄"}, {"置き換えた名前のすぐ", "名前のすぐ前後に残った字"}, {"日付のすぐ後ろ", "日付のすぐ後ろの名前らしき語"},
		{"名前の一部", "名前の一部が化けた字"}, {"敬称のない", "敬称のない名前らしき語"}, {"住所", "住所らしき語"},
	} {
		if strings.HasPrefix(reason, c[0]) {
			return c[1]
		}
	}
	return "そのほかの確かめる所"
}

var screenLabels = map[string]string{"": "トップ", "p1": "①資料を入れる", "p2": "②置き換えて確認", "p3done": "③AIに渡す", "p4": "④届いた合格品", "p5": "⑤要相談", "p6": "⑥完成した書類",
	"names": "氏名一覧", "org": "組織図", "shiji": "00_指示", "clean": "片づけ", "srv": "サーバーへ保存", "keika": "支援経過", "keikaadd": "支援経過", "fix": "⑥直してもらう", "work": "マスキング作業フォルダ"}

var screenErrCount = map[string]int{}

// errlogAPI: 画面のエラー（プログラムの不具合）を記録する。画面から送るのは画面の名前だけ（エラーの文は書かない）
func (s *panelServer) errlogAPI(r *http.Request) (any, error) {
	var q struct{ Screen string }
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	lab, ok := screenLabels[q.Screen]
	if !ok {
		lab = "その他の画面"
	}
	k := time.Now().Format("2006010215")
	if screenErrCount[k] >= 30 { // 1時間に30行まで（同じエラーが続けて出るとき）
		return map[string]string{"ok": "skip"}, nil
	}
	screenErrCount[k]++
	s.errLog(lab, "画面のエラー（プログラムの不具合。エラーの文は書きません）")
	return map[string]string{"ok": "書きました"}, nil
}
