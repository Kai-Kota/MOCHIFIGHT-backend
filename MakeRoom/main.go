package main

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"sync"
)

// Server はマッチング(部屋待機)用のTCP接続を管理する。
// 対戦相手を待つ2人のクライアントを受け付け、揃ったら通知する役割を持つ。
type Server struct {
	mu      sync.Mutex
	clients map[net.Conn]struct{} // 現在部屋に入っているTCPコネクションの集合
}

// MatchReadyMessage は2人揃ったときに全クライアントへ送るJSONメッセージ。
type MatchReadyMessage struct {
	Type        string `json:"type"`
	Connected   int    `json:"connected"`
	Description string `json:"description"`
}

func NewServer() *Server {
	return &Server{
		clients: make(map[net.Conn]struct{}),
	}
}

// addClient は新しく接続してきたクライアントを部屋に追加する。
// 追加した結果ちょうど2人になった瞬間だけ match_ready を通知する
// (すでに2人いる状態からの再入室などで重複通知しないようにするため)。
func (s *Server) addClient(conn net.Conn) {
	s.mu.Lock()
	before := len(s.clients)
	s.clients[conn] = struct{}{}
	after := len(s.clients)
	shouldNotify := before != 2 && after == 2
	s.mu.Unlock()

	if shouldNotify {
		msg := MatchReadyMessage{
			Type:        "match_ready",
			Connected:   after,
			Description: "2 players connected",
		}
		s.broadcastJSON(msg)
	}
}

// removeClient は切断されたクライアントを部屋から取り除く。
func (s *Server) removeClient(conn net.Conn) {
	s.mu.Lock()
	delete(s.clients, conn)
	s.mu.Unlock()
}

// broadcastJSON は値をJSONにシリアライズし、末尾に改行を付けて
// 現在部屋にいる全クライアントへ送信する。
func (s *Server) broadcastJSON(v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		log.Printf("failed to marshal message: %v", err)
		return
	}

	payload = append(payload, '\n')

	// ロックを取っている間に接続の一覧だけコピーし、
	// 実際の書き込み(ネットワークI/O)はロックの外で行う。
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.clients))
	for conn := range s.clients {
		conns = append(conns, conn)
	}
	s.mu.Unlock()

	for _, conn := range conns {
		if _, err := conn.Write(payload); err != nil {
			log.Printf("failed to write to %s: %v", conn.RemoteAddr().String(), err)
		}
	}
}

// main はTCPサーバーを起動し、接続してきたクライアントを部屋に登録する。
// マッチング自体は「TCP接続を受け入れる」だけで完了し、
// 座標や攻撃などの実際の対戦通信はBattleサーバー(UDP)側が担当する。
func main() {
	listener, err := net.Listen("tcp", ":8080")
	if err != nil {
		log.Fatalf("failed to start tcp server: %v", err)
	}
	defer listener.Close()

	log.Println("TCP server started on :8080")

	server := NewServer()

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("accept error: %v", err)
			continue
		}

		// クライアントごとに専用goroutineを立て、接続が切れるまでブロックさせる。
		go func(c net.Conn) {
			log.Printf("client connected: %s", c.RemoteAddr().String())
			server.addClient(c)

			// クライアントからの受信データは使わないので読み捨てる。
			// io.Copyがエラー(切断)を返すまでここでブロックし続けることで、
			// 「接続が生きている間だけ部屋に居続ける」を表現している。
			_, _ = io.Copy(io.Discard, c)

			server.removeClient(c)
			_ = c.Close()
			log.Printf("client disconnected: %s", c.RemoteAddr().String())
		}(conn)
	}
}
