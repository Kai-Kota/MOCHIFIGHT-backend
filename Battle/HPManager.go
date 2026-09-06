package main

// HPUpdate はプレイヤーのHPが変化したときに全クライアントへブロードキャストされるメッセージ。
type HPUpdate struct {
	Type   string `json:"type"`
	Target string `json:"target"`
	HP     int    `json:"hp"`
}

// DeathMessage はプレイヤーのHPが0になったときに全クライアントへブロードキャストされるメッセージ。
type DeathMessage struct {
	Type   string `json:"type"`
	Target string `json:"target"`
}

const (
	InitialHP = 100 // 対戦開始時の初期HP
	HitDamage = 20  // 1回のヒットで与えるダメージ量(固定値)
)

// WelcomeMessage は新規登録されたクライアント本人にだけ送られるメッセージ。
// サーバー側でそのクライアントをどのIDとして扱っているか(=UDPAddrの文字列表現)を通知する。
type WelcomeMessage struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}
