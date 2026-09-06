package main

import (
	"encoding/json"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	listenAddr    = ":9052"
	maxPlayers    = 2
	clientTimeout = 5 * time.Second // この時間パケットが来なければ切断したとみなす
)

// Server は対戦中(バトル)のUDP通信状態を管理する。
// 「誰が今この部屋にいるか」「各プレイヤーのHP」「最後にパケットを受け取った時刻」を保持し、
// 座標や攻撃メッセージを2人のプレイヤー間で中継する。
type Server struct {
	mu       sync.Mutex
	clients  map[string]*net.UDPAddr // key: UDPAddrの文字列表現(プレイヤーの識別子として使う)
	order    []string                // 登録順(現状は表示・管理用で参照はしていない)
	conn     *net.UDPConn
	hp       map[string]int
	lastSeen map[string]time.Time // タイムアウト検知用の最終受信時刻
}

// MatchReadyMessage は2人揃ったときに全クライアントへ送るJSONメッセージ。
// MakeRoom側の同名メッセージと役割は同じだが、こちらはUDP到達後(=実際に対戦サーバーに
// 接続できた後)に送られる、対戦サーバー側からの「準備完了」通知。
type MatchReadyMessage struct {
	Type        string `json:"type"`
	Connected   int    `json:"connected"`
	Description string `json:"description"`
}

func NewServer(conn *net.UDPConn) *Server {
	return &Server{
		clients:  make(map[string]*net.UDPAddr),
		order:    make([]string, 0, maxPlayers),
		conn:     conn,
		hp:       make(map[string]int),
		lastSeen: make(map[string]time.Time),
	}
}

// registerClient はUDPパケットの送信元アドレスを部屋に登録する。
// UDPにはTCPのような明示的な「接続」がないため、パケットを受け取るたびに
// このメソッドを呼んで「今このアドレスは生きている」ことを記録する。
//
// 戻り値:
//
//	known      : 以前から登録済みのアドレスだったか
//	accepted   : 今回のパケットを処理してよいか(満室なら false)
//	becameFull : このパケットの処理によって、たった今2人揃ったか
func (s *Server) registerClient(addr *net.UDPAddr) (known bool, accepted bool, becameFull bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := addr.String()
	if _, exists := s.clients[key]; exists {
		// 既知のクライアント: 生存確認(lastSeen)だけ更新して返す。
		s.lastSeen[key] = time.Now()
		return true, true, false
	}

	if len(s.clients) >= maxPlayers {
		// 3人目以降は対戦に参加できないので無視する。
		return false, false, false
	}

	s.clients[key] = addr
	s.order = append(s.order, key)
	s.lastSeen[key] = time.Now()
	if _, ok := s.hp[key]; !ok {
		// 初回登録時のみHPを満タンで初期化する
		// (evictStaleClientsで一時的に消えて復帰した場合の再初期化は起きない設計)。
		s.hp[key] = 100
	}
	return false, true, len(s.clients) == maxPlayers
}

// removeClient は指定アドレスのプレイヤーを部屋・HP・生存時刻の管理対象から除外する。
func (s *Server) removeClient(addr *net.UDPAddr) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := addr.String()
	delete(s.clients, key)
	delete(s.hp, key)
	delete(s.lastSeen, key)

	for i, v := range s.order {
		if v == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

// evictStaleClients は lastSeen が clientTimeout を超えたクライアントを削除する。
// UDPは切断イベントが飛んでこないため、一定時間パケットが来なくなったクライアントを
// 定期的に(呼び出し元のticker経由で)強制的に部屋から退出させることで、
// 通信が途切れたプレイヤーが部屋に居座り続けるのを防いでいる。
func (s *Server) evictStaleClients() {
	s.mu.Lock()
	var stale []string
	for key, t := range s.lastSeen {
		if time.Since(t) > clientTimeout {
			stale = append(stale, key)
		}
	}
	s.mu.Unlock()

	for _, key := range stale {
		log.Printf("client %s timed out, removing from room", key)
		s.mu.Lock()
		delete(s.clients, key)
		delete(s.hp, key)
		delete(s.lastSeen, key)
		for i, v := range s.order {
			if v == key {
				s.order = append(s.order[:i], s.order[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}
}

// ApplyDamage は target のHPを dmg 分だけ減らし、減算後のHPを返す。
// HPは0未満にならないようクランプする。target が未登録の場合は -1 を返す。
//
// 注意: ダメージ量やヒットの有無はクライアントからの自己申告(hit_report)を
// そのまま信用しており、サーバー側で座標をもとにした当たり判定は行っていない
// (README「既知の課題」参照)。
func (s *Server) ApplyDamage(target string, dmg int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hp[target]; !ok {
		return -1
	}
	s.hp[target] -= dmg
	if s.hp[target] < 0 {
		s.hp[target] = 0
	}
	return s.hp[target]
}

// GetHP は target の現在HPを返す。存在しない場合は ok が false になる。
func (s *Server) GetHP(target string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.hp[target]
	return v, ok
}

// clientCount は現在部屋にいるクライアント数を返す。
func (s *Server) clientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

// otherClient は sender 以外に登録されているクライアントのアドレスを返す
// (最大2人対戦なので「もう一方のプレイヤー」を意味する)。いなければ nil。
func (s *Server) otherClient(sender *net.UDPAddr) *net.UDPAddr {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, addr := range s.clients {
		if key != sender.String() {
			return addr
		}
	}
	return nil
}

// broadcastJSON は値をJSONにシリアライズし、部屋にいる全クライアントへUDPで送信する。
func (s *Server) broadcastJSON(v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		log.Printf("failed to marshal JSON: %v", err)
		return
	}

	s.mu.Lock()
	clients := make([]*net.UDPAddr, 0, len(s.clients))
	for _, addr := range s.clients {
		clients = append(clients, addr)
	}
	s.mu.Unlock()

	for _, addr := range clients {
		if _, err := s.conn.WriteToUDP(payload, addr); err != nil {
			log.Printf("failed to write to %s: %v", addr.String(), err)
		}
	}
}

// sendText は生文字列のメッセージを指定アドレスへ送信する(現状未使用のヘルパー)。
func (s *Server) sendText(addr *net.UDPAddr, message string) {
	if _, err := s.conn.WriteToUDP([]byte(message), addr); err != nil {
		log.Printf("failed to send to %s: %v", addr.String(), err)
	}
}

// main はUDPサーバーを起動し、受信したパケットの種類に応じて
// 「登録」「マッチ成立通知」「ヒット判定」「座標/攻撃の中継」「退室」を処理する。
func main() {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 9052})
	if err != nil {
		log.Fatalf("failed to start udp server: %v", err)
	}
	defer conn.Close()

	log.Printf("UDP server started on %s", listenAddr)

	server := NewServer(conn)

	// タイムアウトチェックを定期実行
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			server.evictStaleClients()
		}
	}()

	buffer := make([]byte, 2048)

	for {
		n, remoteAddr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			log.Printf("read error: %v", err)
			continue
		}

		message := strings.TrimSpace(string(buffer[:n]))
		// パケットを受け取るたびに送信元を登録/生存確認する。
		known, accepted, becameFull := server.registerClient(remoteAddr)
		if !accepted {
			// 部屋が満員で受け入れられなかった(=対戦に無関係な3人目以降)ので、
			// このパケットは中継等の処理をせずに読み捨てる。
			log.Printf("ignored packet from unregistered client %s because the room is full", remoteAddr.String())
			continue
		}

		if !known {
			// 初回登録: 現在のHPを全員に共有し、本人には自分のIDを教える(welcome)。
			if hp, ok := server.GetHP(remoteAddr.String()); ok {
				server.broadcastJSON(HPUpdate{Type: "hp_update", Target: remoteAddr.String(), HP: hp})
			}

			welcome := WelcomeMessage{Type: "welcome", ID: remoteAddr.String()}
			if payload, err := json.Marshal(welcome); err == nil {
				if _, err := conn.WriteToUDP(payload, remoteAddr); err != nil {
					log.Printf("failed to send welcome to %s: %v", remoteAddr.String(), err)
				}
			} else {
				log.Printf("failed to marshal welcome message: %v", err)
			}
		}

		if becameFull {
			// このパケットの登録処理でちょうど2人揃った瞬間に一度だけ通知する。
			server.broadcastJSON(MatchReadyMessage{
				Type:        "match_ready",
				Connected:   server.clientCount(),
				Description: "2 players connected",
			})
		}

		// メッセージ種別を判定するため、まず "type" フィールドだけ取り出す。
		var base struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(buffer[:n], &base); err == nil {
			if base.Type == "hit_report" {
				// クライアントから「自分の攻撃が相手に当たった」という自己申告を受け取り、
				// 対象(target)のHPを減らして全員に結果を通知する。
				var hr struct {
					Type    string `json:"type"`
					Target  string `json:"target"`
					Shooter string `json:"shooter"`
					Damage  int    `json:"damage"`
				}
				if err := json.Unmarshal(buffer[:n], &hr); err != nil {
					log.Printf("invalid hit_report from %s: %v", remoteAddr.String(), err)
				} else {
					target := hr.Target
					if target == "" {
						// target未指定の場合は「もう一方のプレイヤー」を対象とみなす。
						other := server.otherClient(remoteAddr)
						if other != nil {
							target = other.String()
						}
					}
					dmg := HitDamage
					if target != "" {
						newHP := server.ApplyDamage(target, dmg)
						if newHP >= 0 {
							server.broadcastJSON(HPUpdate{Type: "hp_update", Target: target, HP: newHP})
							if newHP == 0 {
								server.broadcastJSON(DeathMessage{Type: "death", Target: target})
							}
						} else {
							log.Printf("hit_report target not found: %s", target)
						}
					}
				}
				continue
			}
		}

		// 以下は高頻度に送られる軽量メッセージ(生文字列プレフィックス形式)の中継処理。
		// JSONではなくプレフィックス文字列にしているのは、毎フレーム送信される
		// 座標・弾・掴み技のデータ量とパース負荷を抑えるため。

		if strings.HasPrefix(message, "P:") {
			// プレイヤー座標をそのまま相手に転送する。
			other := server.otherClient(remoteAddr)
			if other != nil {
				if _, err := conn.WriteToUDP([]byte(message), other); err != nil {
					log.Printf("relay error from %s to %s: %v", remoteAddr.String(), other.String(), err)
				}
			}
		}

		if strings.HasPrefix(message, "S:") {
			// 弾(Shot)の座標・向きを相手に転送する。
			other := server.otherClient(remoteAddr)
			if other != nil {
				if _, err := conn.WriteToUDP([]byte(message), other); err != nil {
					log.Printf("bullet relay error from %s to %s: %v", remoteAddr.String(), other.String(), err)
				}
			}
		}

		if strings.HasPrefix(message, "G:") {
			// 掴み技(Grapple)の情報を相手に転送する。
			other := server.otherClient(remoteAddr)
			if other != nil {
				if _, err := conn.WriteToUDP([]byte(message), other); err != nil {
					log.Printf("grapple relay error from %s to %s: %v", remoteAddr.String(), other.String(), err)
				}
			}
		}

		if message == "disconnect" {
			// クライアントからの明示的な退室通知。
			server.removeClient(remoteAddr)
		}
	}
}
