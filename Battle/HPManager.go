package main

// HPが変わったときにみんなに送るメッセージ
type HPUpdate struct {
	Type   string `json:"type"`
	Target string `json:"target"`
	HP     int    `json:"hp"`
}

// HPが0になった人が出たときに送るメッセージ
type DeathMessage struct {
	Type   string `json:"type"`
	Target string `json:"target"`
}

const (
	InitialHP = 100 // 最初のHP
	HitDamage = 20  // 1発あたりのダメージ
)

// 新しく入ってきた本人にだけ送るメッセージ
// サーバー内部でこの人をどのIDとして扱ってるかを教える
type WelcomeMessage struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}
