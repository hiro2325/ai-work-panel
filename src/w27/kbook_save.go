package main

// 支援経過_氏名.xlsx を新しく作る・今ある「支援経過1日分_…xlsx」をまとめる（改善29 No.57）。サーバーへ書く手順。
// 守ること：何も消さない・上書きしない。書く前に控え（または記録）→ 一時ファイル（.part）に書いて確かめる → 開いていないか確かめる → 名前を付ける。
// 確認画面で「保存する」が押されたときだけ動く。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// doCreateBook: 「支援経過_氏名.xlsx」を新しく作る（同じ名前があれば作らない）
func (s *panelServer) doCreateBook(dir, outName string, pl *dayAppendPlan, now time.Time) (*dayAppendResult, error) {
	ap := pl.info
	if ap.Blocked != "" || ap.Target == "" {
		return nil, fmt.Errorf("%s", ap.Blocked)
	}
	data, err := os.ReadFile(filepath.Join(s.p.out, outName))
	sum := sha256.Sum256(data)
	if err != nil || hex.EncodeToString(sum[:]) != pl.srcHash {
		return nil, fmt.Errorf("確認の画面のあとで、元の書類（%s）が変わりました。もう一度開き直してください（まだ何も書いていません）", outName)
	}
	if ap.Target != filepath.Base(ap.Target) || !strings.HasPrefix(ap.Target, keikaBookPrefix) {
		return nil, fmt.Errorf("書く先が正しくありません（まだ何も書いていません）")
	}
	if fs := bookXlsxFiles(dir); len(fs) > 0 {
		return nil, fmt.Errorf("確認の画面のあとで、利用者フォルダに「支援経過_…」のExcelができました。もう一度開き直してください（まだ何も書いていません）")
	}
	tpath := filepath.Join(dir, ap.Target)
	if _, err := os.Lstat(tpath); err == nil {
		return nil, fmt.Errorf("「%s」という名前のものがすでにあるため、作りませんでした（まだ何も書いていません）", ap.Target)
	}
	newData, err := buildBookNew(data)
	if err != nil {
		return nil, fmt.Errorf("作る中身を作れませんでした（まだ何も書いていません）：%v", err)
	}
	part, err := writeNoDelete(dir, ap.Target+".part", newData)
	if err != nil || !sameFile(part, newData) {
		return nil, fmt.Errorf("新しいファイルを正しく作れませんでした。途中のファイル（%s）が残っていたら、あとで手で消してください（パネルはサーバーで消しません）", filepath.Base(part))
	}
	if _, err := os.Lstat(tpath); err == nil {
		return nil, fmt.Errorf("書く直前に「%s」ができたため、入れませんでした。途中のファイル（%s）はそのまま残しています", ap.Target, filepath.Base(part))
	}
	if err := os.Rename(part, tpath); err != nil {
		return nil, fmt.Errorf("「%s」の名前にできませんでした。途中のファイル（%s）はそのまま残しています：%v", ap.Target, filepath.Base(part), err)
	}
	return &dayAppendResult{Name: outName, Target: ap.Target, Sheet: pl.day.sheet, Head: ap.Head, Kind: "create"}, nil
}

// doMerge: 今ある「支援経過1日分_…xlsx」を「支援経過_氏名.xlsx」にまとめる。
// 古いファイルは消さずに「控え」へ移す（名前の変更だけ）。まとめの記録も「控え」に書く
func (s *panelServer) doMerge(dir, outName string, pl *dayAppendPlan, now time.Time) (*dayAppendResult, error) {
	mp := pl.merge
	if mp == nil || mp.info.Blocked != "" {
		msg := "まとめられません"
		if mp != nil {
			msg = mp.info.Blocked
		}
		return nil, fmt.Errorf("%s", msg)
	}
	info := mp.info
	data, err := os.ReadFile(filepath.Join(s.p.out, outName))
	sum := sha256.Sum256(data)
	if err != nil || hex.EncodeToString(sum[:]) != pl.srcHash {
		return nil, fmt.Errorf("確認の画面のあとで、元の書類（%s）が変わりました。もう一度開き直してください（まだ何も書いていません）", outName)
	}
	if info.Legacy != filepath.Base(info.Legacy) || info.Book != filepath.Base(info.Book) || !strings.HasPrefix(info.Book, keikaBookPrefix) {
		return nil, fmt.Errorf("書く先が正しくありません（まだ何も書いていません）")
	}
	if fs := dayXlsxFiles(dir); len(fs) != 1 || fs[0] != info.Legacy {
		return nil, fmt.Errorf("確認の画面のあとで、利用者フォルダの「支援経過1日分」のExcelが変わりました。もう一度開き直してください（まだ何も書いていません）")
	}
	if fs := bookXlsxFiles(dir); len(fs) > 0 || len(keikaXlsmFiles(dir)) > 0 {
		return nil, fmt.Errorf("確認の画面のあとで、利用者フォルダの支援経過のExcelが変わりました。もう一度開き直してください（まだ何も書いていません）")
	}
	lpath, tpath := filepath.Join(dir, info.Legacy), filepath.Join(dir, info.Book)
	if _, err := os.Lstat(tpath); err == nil {
		return nil, fmt.Errorf("「%s」という名前のものがすでにあるため、まとめませんでした（まだ何も書いていません）", info.Book)
	}
	if xlsmLocked(dir, info.Legacy) {
		return nil, fmt.Errorf("%s がExcelで開かれています。閉じてから、もう一度押してください（まだ何も書いていません）", info.Legacy)
	}
	if fileSig(lpath) != mp.legacySig {
		return nil, fmt.Errorf("確認の画面のあとで、%s が変わりました。もう一度開き直してください（まだ何も書いていません）", info.Legacy)
	}
	orig, err := os.ReadFile(lpath)
	if err != nil {
		return nil, fmt.Errorf("%s を読めませんでした（Excelで開いていたら閉じてください）。まだ何も書いていません", info.Legacy)
	}
	if fileSig(lpath) != mp.legacySig {
		return nil, fmt.Errorf("読んでいる間に %s が変わりました。もう一度開き直してください（まだ何も書いていません）", info.Legacy)
	}
	tp, err := readPkg(orig)
	if err != nil {
		return nil, fmt.Errorf("%s をExcelとして開けません（まだ何も書いていません）", info.Legacy)
	}
	ip, err := readPkg(data)
	if err != nil {
		return nil, fmt.Errorf("元の書類をExcelとして開けません（まだ何も書いていません）")
	}
	tmpl, _, err := dayTemplate(tp, ip)
	if err != nil {
		return nil, fmt.Errorf("%v（まだ何も書いていません）", err)
	}
	nm, dup, err := kxChooseName(tp, pl.day)
	if err != nil {
		return nil, fmt.Errorf("%v（まだ何も書いていません）", err)
	}
	if dup != "" {
		return nil, fmt.Errorf("この1日分は、すでにシート「%s」に入っています（まだ何も書いていません）", dup)
	}
	newData, err := buildBookMerge(orig, pl.day, nm, tmpl)
	if err != nil {
		return nil, fmt.Errorf("まとめる中身を作れませんでした（まだ何も書いていません）：%v", err)
	}
	// 控え：まとめの記録（書く前に。古いファイルの名前・シート・まとめてできる名前。元に戻せるように）
	bdir := filepath.Join(dir, keikaBackupDir)
	if err := os.MkdirAll(bdir, 0755); err != nil {
		return nil, fmt.Errorf("「控え」のフォルダを作れませんでした（まだ何も書いていません）：%v", err)
	}
	stamp := now.Format("20060102_150405")
	var rec strings.Builder
	rec.WriteString("今ある1日分のExcelを、「" + info.Book + "」にまとめます（古いファイルは消さずに、この「控え」のフォルダへ移します）。\r\n")
	rec.WriteString("元に戻すときは、控えの中の古いファイルを、利用者フォルダのいちばん上へ戻し、「" + info.Book + "」の名前を変えるか別の場所へ移してください。\r\n\r\n")
	rec.WriteString("古いファイル：" + info.Legacy + "\r\n中のシート：" + strings.Join(info.Names, "、") + "\r\n")
	rec.WriteString("まとめてできるファイル：" + info.Book + "\r\nこのとき足すシート：" + nm + "（" + pl.info.Head + "）\r\n")
	if _, err := writeNoDelete(bdir, stamp+"_"+supportRecordName+".txt", []byte(bom+rec.String())); err != nil {
		return nil, fmt.Errorf("控えの記録を書けませんでした（まだ何も書いていません）：%v", err)
	}
	part, err := writeNoDelete(dir, info.Book+".part", newData)
	if err != nil || !sameFile(part, newData) {
		return nil, fmt.Errorf("まとめたファイルを正しく作れませんでした。%s はそのままです。途中のファイル（%s）が残っていたら、あとで手で消してください（パネルはサーバーで消しません）", info.Legacy, filepath.Base(part))
	}
	if xlsmLocked(dir, info.Legacy) || fileSig(lpath) != mp.legacySig {
		return nil, fmt.Errorf("書く直前に %s が開かれたか変わったため、まとめませんでした。%s はそのままです。途中のファイル（%s）はそのまま残しています", info.Legacy, info.Legacy, filepath.Base(part))
	}
	if _, err := os.Lstat(tpath); err == nil {
		return nil, fmt.Errorf("書く直前に「%s」ができたため、まとめませんでした。%s はそのままです。途中のファイル（%s）はそのまま残しています", info.Book, info.Legacy, filepath.Base(part))
	}
	if err := os.Rename(part, tpath); err != nil {
		return nil, fmt.Errorf("「%s」の名前にできませんでした。%s はそのままです。途中のファイル（%s）はそのまま残しています：%v", info.Book, info.Legacy, filepath.Base(part), err)
	}
	r := &dayAppendResult{Name: outName, Target: info.Book, Sheet: nm, Head: pl.info.Head, Kind: "merge", Merged: info.Sheets}
	// 古いファイルを「控え」へ移す（名前の変更だけ。同じ名前があれば別の名前にする。消さない）
	dest := ""
	for i := 1; i < 100 && dest == ""; i++ {
		n := stamp + "_" + info.Legacy
		if i > 1 {
			n = fmt.Sprintf("%s_(%d)_%s", stamp, i, info.Legacy)
		}
		if _, err := os.Lstat(filepath.Join(bdir, n)); os.IsNotExist(err) {
			dest = filepath.Join(bdir, n)
		}
	}
	if dest == "" {
		r.Warn = "「" + info.Book + "」はできましたが、古いファイル（" + info.Legacy + "）を控えへ移せませんでした（そのまま残っています）"
		return r, nil
	}
	if fileSig(lpath) != mp.legacySig || xlsmLocked(dir, info.Legacy) {
		r.Warn = "「" + info.Book + "」はできましたが、古いファイル（" + info.Legacy + "）が開かれたか変わったため、控えへ移していません（そのまま残っています）"
		return r, nil
	}
	if err := os.Rename(lpath, dest); err != nil {
		r.Warn = "「" + info.Book + "」はできましたが、古いファイル（" + info.Legacy + "）を控えへ移せませんでした（そのまま残っています）：" + err.Error()
		return r, nil
	}
	r.Moved = keikaBackupDir + "＼" + filepath.Base(dest)
	return r, nil
}

const supportRecordName = "支援経過まとめの記録"

func (s *panelServer) writeBookHistory(user, kind, sheet string) {
	// 記録には、実名の入ったファイル名は書かない（「支援経過_氏名.xlsx」と書く）
	what := map[string]string{
		"create": "「支援経過_氏名.xlsx」を新しく作り、シート「" + sheet + "」を入れた",
		"book":   "「支援経過_氏名.xlsx」にシート「" + sheet + "」を足し、一覧に1行足した",
		"merge":  "今ある1日分のExcelを「支援経過_氏名.xlsx」にまとめ、シート「" + sheet + "」を足した",
	}[kind]
	line := time.Now().Format("2006/01/02 15:04") + "\t【" + user + "】\t支援経過（1日分）\t" + what
	path := filepath.Join(s.p.report, serverHistoryName)
	_, statErr := os.Stat(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	if os.IsNotExist(statErr) {
		f.WriteString(bom + "日付\t記号\t書類\t件数\r\n")
	}
	f.WriteString(line + "\r\n")
}
