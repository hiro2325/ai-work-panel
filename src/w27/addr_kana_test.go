package main

import (
	"strings"
	"testing"
)

// 住所の見つけ方のテスト。人物・住所はどれも架空、または市区町村名だけ実在の書き方。

func addrHeld(s string) []string {
	m := newMasker(nil, nil, newCodeBook())
	var out []string
	for _, su := range m.addrSuspects(s) {
		out = append(out, su.word)
	}
	return out
}

// ひらがな・カタカナを含む市区町村名：住所全体（市区町村名から）が保留になる
func TestAddrKanaMunicipality(t *testing.T) {
	cases := []struct{ text, want string }{
		{"長男は埼玉県さいたま市浦和区高砂1-2-3に住んでいる。", "埼玉県さいたま市浦和区高砂1-2-3"},
		{"長男はさいたま市浦和区高砂1-2-3に住んでいる。", "さいたま市浦和区高砂1-2-3"},
		{"住所：つくば市竹園1-2-3", "つくば市竹園1-2-3"},
		{"住所：つくばみらい市小絹1-2-3", "つくばみらい市小絹1-2-3"},
		{"住所：いわき市平1-2-3", "いわき市平1-2-3"},
		{"住所：南アルプス市小笠原1-2-3", "南アルプス市小笠原1-2-3"},
		{"住所：伊豆の国市長岡1-2-3", "伊豆の国市長岡1-2-3"},
		{"住所：架空県ほしぞら市中央1-2-3", "架空県ほしぞら市中央1-2-3"}, // 一覧にない名前も、都道府県名が前にあれば
		// 漢字だけの名前は今までどおり
		{"住所：架空県虹彩市星降町1-2-3", "架空県虹彩市星降町1-2-3"},
		{"住所：神奈川県大和市中央3丁目9番51号", "神奈川県大和市中央3丁目9番51号"},
	}
	for _, c := range cases {
		got := addrHeld(c.text)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s\n  保留の語 = %q\n  期待     = %q", c.text, got, c.want)
		}
	}
}

// 住所でない言葉は止めない
func TestAddrKanaNotAddress(t *testing.T) {
	for _, s := range []string{
		"のみの市は3-4日に開催。",
		"あさいちで1-2個買った。",
		"当市のサービスを週1-2回利用。",
		"市の窓口へ1-2回相談した。",
	} {
		if got := addrHeld(s); len(got) != 0 {
			t.Errorf("%s\n  止めすぎ：保留の語 = %q", s, got)
		}
	}
}

// 登録した住所は、かなの市区町村名も含めて1つの記号になる
func TestAddrKanaRegistered(t *testing.T) {
	people := []person{{kind: addrKind, name: "つくば市竹園1-2-3"}}
	m := newMasker(people, nil, newCodeBook())
	s := "長男はつくば市竹園1-2-3に住んでいる。"
	got := applyMatches(s, m.findMatches(s, true))
	if strings.Contains(got, "つくば") || !strings.Contains(got, "【住所001】") {
		t.Errorf("置き換えの結果 = %q（「つくば」が残らず【住所001】になること）", got)
	}
	if su := m.addrSuspects(got); len(su) != 0 {
		t.Errorf("置き換えたあとに保留が残る: %q", su[0].word)
	}
}
