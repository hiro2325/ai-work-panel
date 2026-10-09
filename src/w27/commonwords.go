package main

// よく出る語（職種・神奈川県内の市町村名・よく出る地名）（改善12 No.34）
// 橙の確認に出たとき、画面でまとめて表示し「よく出る語をまとめて除外」を1回押しで出せるようにする。
// 名字と同じ字のことがある（大和さん・座間さん・厚木さん など）ので、自動では除外しない。

import "strings"

var commonJobWords = strings.Fields(`看護師 准看護師 保健師 助産師 訪問看護師 看護職員 ヘルパー ホームヘルパー 訪問介護員 介護員 介護士 介護福祉士 介護職員 ケアマネ ケアマネジャー ケアマネージャー 介護支援専門員 主任ケアマネ 医師 主治医 歯科医師 歯科衛生士 薬剤師 相談員 生活相談員 支援相談員 医療相談員 社会福祉士 精神保健福祉士 理学療法士 作業療法士 言語聴覚士 管理栄養士 栄養士 福祉用具専門相談員 サービス提供責任者 サ責 機能訓練指導員 民生委員 施設長 所長 センター長 管理者 事務員 ドクター 先生 PT OT ST Dr Ns`)

var commonPlaceWords = strings.Fields(`神奈川 横浜 川崎 相模原 横須賀 平塚 鎌倉 藤沢 小田原 茅ヶ崎 茅ケ崎 逗子 三浦 秦野 厚木 大和 伊勢原 さくら 座間 南足柄 綾瀬 葉山 寒川 大磯 二宮 中井 大井 松田 山北 開成 箱根 真鶴 湯河原 愛川 清川 相模 湘南 県央 ` +
	`桜町 東桜町 西桜町 桜台 若葉台 花園 緑町 青葉町 本町 栄町 旭町 宮前 新町 富士見 日の出町 港町 弥生町 寿町 朝日町 春日町 梅ケ丘 ` +
	`入谷 相武台 栗原 小松原 ひばりが丘 相模が丘 立野台 緑ケ丘 南栗原 広野台 明王 新田宿 四ツ谷 ` +
	`本厚木 愛甲石田 南林間 中央林間 鶴間 さがみ野 かしわ台 相模大塚 大和駅 桜ヶ丘 高座渋谷 長後 湘南台 ` +
	`青葉 都筑 港北 瀬谷 戸塚 港南 磯子 金沢 保土ケ谷 鶴見 中原 高津 宮前 多摩 麻生`)

var commonWordSet = map[string]bool{}

func init() {
	for _, w := range commonJobWords {
		commonWordSet[w] = true
	}
	for _, w := range commonPlaceWords {
		commonWordSet[w] = true
	}
}

// isCommonWord: 職種・地名など、よく出る語か（「さくら市」「座間駅」「訪問看護師」も含める）
func isCommonWord(w string) bool {
	w = strings.TrimSpace(normName(w))
	if w == "" {
		return false
	}
	if commonWordSet[w] {
		return true
	}
	for _, suf := range []string{"市", "町", "村", "区", "駅", "県", "郡", "地区"} {
		if t := strings.TrimSuffix(w, suf); t != w && commonWordSet[t] {
			return true
		}
	}
	return false
}
