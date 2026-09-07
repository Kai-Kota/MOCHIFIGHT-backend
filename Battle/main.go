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
	clientTimeout = 5 * time.Second // これだけパケットが来なかったら切断扱いにする
)

// Server は対戦中のUDPのやりとりをまとめて管理する構造体
// 今部屋に誰がいるか、HPは何か、最後にいつパケット来たか、を全部ここで持ってる
// 座標、攻撃のメッセージもここを経由して相手に転送される
type Server struct {
	mu       sync.Mutex
	clients  map[string]*net.UDPAddr // UDPAddrを文字列にしたものをidとして使ってる
	order    []string                // 登録した順番
	conn     *net.UDPConn
	hp       map[string]int
	lastSeen map[string]time.Time // 最後にパケット来た時刻、タイムアウト判定用
}

// MatchReadyMessage は2人揃ったときに送るマッチ成立した確認のJSON
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

// パケット送ってきたアドレスを部屋に登録する処理

//	known      : 前から知ってるアドレスかどうか
//	accepted   : 今回の分を処理していいか
//	becameFull : このタイミングでちょうど2人揃ったかどうか
func (s *Server) registerClient(addr *net.UDPAddr) (known bool, accepted bool, becameFull bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := addr.String()
	if _, exists := s.clients[key]; exists {
		// 前から知ってる人だったらlastSeenだけ更新して終わり
		s.lastSeen[key] = time.Now()
		return true, true, false
	}

	if len(s.clients) >= maxPlayers {
		// もう2人埋まってたら3人目以降は入れない
		return false, false, false
	}

	s.clients[key] = addr
	s.order = append(s.order, key)
	s.lastSeen[key] = time.Now()
	if _, ok := s.hp[key]; !ok {
		// 初めて来た人だけHPを満タンにする
		s.hp[key] = 100
	}
	return false, true, len(s.clients) == maxPlayers
}

// removeClient は指定した人を部屋・HP・生存時刻の管理から全部消す
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

// 一定時間(clientTimeout)パケットが来てない人を強制退室させる
// 定期的にチェックしてフリーズしたプレイヤーがずっと部屋に居座るのを防ぐ処理
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

// target のHPを dmg だけ減らして、減った後のHPを返す関数
// マイナスにはならないように0で止める、targetが見つからなかったら-1を返す
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

// 今のHPを返す、登録されてなかったらokがfalseになる
func (s *Server) GetHP(target string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.hp[target]
	return v, ok
}

// 今部屋にいる人数を返す
func (s *Server) clientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

// 自分(sender)以外の登録済みアドレスを返す関数。
// 2人対戦しかないので、相手プレイヤーを探してる、いなければnil
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

// JSONにして部屋にいる全員にUDPで送る
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

// main関数
// UDPサーバー立ち上げて、パケットが来たら中身によって
// 登録・マッチ成立通知・ヒット判定・座標や攻撃の中継・退室のように振り分けてる
func main() {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 9052})
	if err != nil {
		log.Fatalf("failed to start udp server: %v", err)
	}
	defer conn.Close()

	log.Printf("UDP server started on %s", listenAddr)

	server := NewServer(conn)

	// 2秒おきにタイムアウトチェックを回しておく
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
		// パケット来るたびに毎回登録/生存確認をする
		known, accepted, becameFull := server.registerClient(remoteAddr)
		if !accepted {
			// 部屋が満員で入れなかったので、このパケットは何もせず捨てる
			log.Printf("ignored packet from unregistered client %s because the room is full", remoteAddr.String())
			continue
		}

		if !known {
			// 初めて来た人には、今のHPをみんなに共有しつつ、本人には自分のID(welcome)を教える
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
			// ちょうど2人揃った瞬間だけ通知が飛ぶ
			server.broadcastJSON(MatchReadyMessage{
				Type:        "match_ready",
				Connected:   server.clientCount(),
				Description: "2 players connected",
			})
		}

		// とりあえず"type"だけ取り出してどんなメッセージか判定する
		var base struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(buffer[:n], &base); err == nil {
			if base.Type == "hit_report" {
				// 攻撃の当たった報告がクライアントから来たら、
				// 対象のHPを減らしてみんなに結果を知らせる
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
						// targetが空だったらとりあえず相手を対象にしとく
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

		if strings.HasPrefix(message, "P:") {
			// プレイヤーの座標をそのまま相手に投げる
			other := server.otherClient(remoteAddr)
			if other != nil {
				if _, err := conn.WriteToUDP([]byte(message), other); err != nil {
					log.Printf("relay error from %s to %s: %v", remoteAddr.String(), other.String(), err)
				}
			}
		}

		if strings.HasPrefix(message, "S:") {
			// 弾の座標をそのまま相手に投げる
			other := server.otherClient(remoteAddr)
			if other != nil {
				if _, err := conn.WriteToUDP([]byte(message), other); err != nil {
					log.Printf("bullet relay error from %s to %s: %v", remoteAddr.String(), other.String(), err)
				}
			}
		}

		if strings.HasPrefix(message, "G:") {
			// グラップルの座標をそのまま相手に投げる
			other := server.otherClient(remoteAddr)
			if other != nil {
				if _, err := conn.WriteToUDP([]byte(message), other); err != nil {
					log.Printf("grapple relay error from %s to %s: %v", remoteAddr.String(), other.String(), err)
				}
			}
		}

		if message == "disconnect" {
			// 自分から退室する場合
			server.removeClient(remoteAddr)
		}
	}
}
