package main

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"sync"
)

type Server struct {
	mu      sync.Mutex
	clients map[net.Conn]struct{}
}

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

func (s *Server) removeClient(conn net.Conn) {
	s.mu.Lock()
	delete(s.clients, conn)
	s.mu.Unlock()
}

func (s *Server) broadcastJSON(v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		log.Printf("failed to marshal message: %v", err)
		return
	}

	payload = append(payload, '\n')

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

		go func(c net.Conn) {
			log.Printf("client connected: %s", c.RemoteAddr().String())
			server.addClient(c)

			// Block until the client disconnects.
			_, _ = io.Copy(io.Discard, c)

			server.removeClient(c)
			_ = c.Close()
			log.Printf("client disconnected: %s", c.RemoteAddr().String())
		}(conn)
	}
}