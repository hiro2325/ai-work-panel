package main

// ⑥ 完成した書類を、Windows の「ダウンロード」フォルダへ写す（改善28 No.55・R8.10.7 承認）
//   - 元のファイル（2_完成）は動かさない・消さない。写すだけ。
//   - 同じ名前があれば「名前（2）.xlsx」「名前（3）.xlsx」…にして、上書きしない。
//   - 保存先は %USERPROFILE%\Downloads。リンクなどで移されていれば、Windows の既知のフォルダの場所（SHGetKnownFolderPath）。

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// downloadsDir: ダウンロードフォルダ。試験用に環境変数 MASKTOOL_DOWNLOAD_DIR で置き場を変えられる
func downloadsDir() string {
	if v := os.Getenv("MASKTOOL_DOWNLOAD_DIR"); v != "" {
		return v
	}
	return platformDownloadsDir()
}

// uniqueCreate: 名前が空いていれば作り、あれば「名前（2）」「名前（3）」…。上書きしない（O_EXCL）
func uniqueCreate(dir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s（%d）%s", base, i, ext)
		}
		f, err := os.OpenFile(filepath.Join(dir, n), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err == nil {
			return f, n, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("同じ名前のファイルが多すぎます")
}

// /api/download：選んだ完成書類をダウンロードフォルダへ写す。Folder は仕事のフォルダ（「どの仕事か分からないもの」は空）
func (s *panelServer) downloadFiles(r *http.Request) (any, error) {
	var q struct {
		Folder string
		Names  []string
	}
	if err := readJSON(r, &q); err != nil {
		return nil, err
	}
	if len(q.Names) == 0 {
		return nil, fmt.Errorf("ダウンロードするファイルを選んでください")
	}
	jobs, other := s.doneJobs(s.codeBook())
	allowed := map[string]bool{}
	if q.Folder == "" {
		for _, d := range other {
			allowed[d.Name] = true
		}
	} else {
		for _, j := range jobs {
			if j.Folder == q.Folder {
				for _, d := range j.Files {
					allowed[d.Name] = true
				}
			}
		}
	}
	seen := map[string]bool{}
	var names []string
	for _, n := range q.Names {
		if !safeName(n) || !allowed[n] {
			return nil, fmt.Errorf("⑥の一覧にないファイルが送られてきました（何も写していません）")
		}
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	dl := downloadsDir()
	if st, err := os.Stat(dl); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("ダウンロードフォルダが見つかりません（何も写していません）")
	}
	type res struct {
		Name   string `json:"name"`
		SaveAs string `json:"saveAs,omitempty"`
		Note   string `json:"note,omitempty"`
	}
	saved, failed := []res{}, []res{}
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(s.p.out, n))
		if err != nil {
			failed = append(failed, res{Name: n, Note: "2_完成 のファイルを読めませんでした"})
			continue
		}
		f, as, err := uniqueCreate(dl, n)
		if err != nil {
			failed = append(failed, res{Name: n, Note: "写せませんでした：" + err.Error()})
			continue
		}
		_, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			os.Remove(filepath.Join(dl, as)) // いま自分で作った書きかけだけを片づける（もとのファイルは触らない）
			failed = append(failed, res{Name: n, Note: "写せませんでした"})
			continue
		}
		it := res{Name: n}
		if as != n {
			it.SaveAs = as
			it.Note = "同じ名前のファイルがあったので「" + as + "」にしました（前のものはそのまま）"
		}
		saved = append(saved, it)
	}
	return map[string]any{"dir": dl, "saved": saved, "failed": failed, "n": len(saved)}, nil
}
