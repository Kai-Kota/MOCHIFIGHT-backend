package main

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"sync"
)

// マッチング用のTCPコネクションをまとめて管理する構造体
// 今つながってるクライアントを覚えておくだけ
type Server struct {
	mu      sync.Mutex
	clients map[net.Conn]struct{} // 今つながってる人たちのコネクション一覧
}

// 2人揃ったときに送るマッチ成立のJSON
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

// 新しく接続してきた人を部屋に追加する処理
// ちょうど2人になった瞬間だけmatch_readyを送る
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

// 切断された人を部屋から消す
func (s *Server) removeClient(conn net.Conn) {
	s.mu.Lock()
	delete(s.clients, conn)
	s.mu.Unlock()
}

// JSONに変換して改行つけて、部屋にいる全員に送る
func (s *Server) broadcastJSON(v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		log.Printf("failed to marshal message: %v", err)
		return
	}

	payload = append(payload, '\n')

	// ロック中にコネクション一覧だけコピーしておいて
	// 実際に送信する処理はロックの外で行う
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

// TCPサーバーを立てて、つながってきた人を部屋に入れる処理
// マッチング側はここで完結してて、対戦中の座標とか攻撃のやりとりは
// Battleサーバーで行う
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

		// 1人ずつgoroutineを立てて、切断されるまでそのまま待たせておく
		go func(c net.Conn) {
			log.Printf("client connected: %s", c.RemoteAddr().String())
			server.addClient(c)

			// クライアントから何か送られてきても中身は使わないので捨てる
			// io.Copyがエラー返す=切断された、ということなので
			// それまでずっとここで止まってる
			_, _ = io.Copy(io.Discard, c)

			server.removeClient(c)
			_ = c.Close()
			log.Printf("client disconnected: %s", c.RemoteAddr().String())
		}(conn)
	}
}
