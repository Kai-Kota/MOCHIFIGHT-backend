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
	listenAddr     = ":9052"
	maxPlayers     = 2
	clientTimeout  = 5 * time.Second
)

type Server struct {
	mu       sync.Mutex
	clients  map[string]*net.UDPAddr
	order    []string
	conn     *net.UDPConn
	hp       map[string]int
	lastSeen map[string]time.Time
}

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

func (s *Server) registerClient(addr *net.UDPAddr) (known bool, accepted bool, becameFull bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := addr.String()
	if _, exists := s.clients[key]; exists {
		s.lastSeen[key] = time.Now()
		return true, true, false
	}

	if len(s.clients) >= maxPlayers {
		return false, false, false
	}

	s.clients[key] = addr
	s.order = append(s.order, key)
	s.lastSeen[key] = time.Now()
	if _, ok := s.hp[key]; !ok {
		s.hp[key] = 100
	}
	return false, true, len(s.clients) == maxPlayers
}

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

// evictStaleClients は lastSeen が clientTimeout を超えたクライアントを削除する
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

func (s *Server) GetHP(target string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.hp[target]
	return v, ok
}

func (s *Server) clientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

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

func (s *Server) sendText(addr *net.UDPAddr, message string) {
	if _, err := s.conn.WriteToUDP([]byte(message), addr); err != nil {
		log.Printf("failed to send to %s: %v", addr.String(), err)
	}
}

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
		known, accepted, becameFull := server.registerClient(remoteAddr)
		if !accepted {
			log.Printf("ignored packet from unregistered client %s because the room is full", remoteAddr.String())
			continue
		}

		if !known {
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
			server.broadcastJSON(MatchReadyMessage{
				Type:        "match_ready",
				Connected:   server.clientCount(),
				Description: "2 players connected",
			})
		}

		var base struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(buffer[:n], &base); err == nil {
			if base.Type == "hit_report" {
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
			other := server.otherClient(remoteAddr)
			if other != nil {
				if _, err := conn.WriteToUDP([]byte(message), other); err != nil {
					log.Printf("relay error from %s to %s: %v", remoteAddr.String(), other.String(), err)
				}
			}
		}

		if strings.HasPrefix(message, "S:") {
			other := server.otherClient(remoteAddr)
			if other != nil {
				if _, err := conn.WriteToUDP([]byte(message), other); err != nil {
					log.Printf("bullet relay error from %s to %s: %v", remoteAddr.String(), other.String(), err)
				}
			}
		}

		if strings.HasPrefix(message, "G:") {
			other := server.otherClient(remoteAddr)
			if other != nil {
				if _, err := conn.WriteToUDP([]byte(message), other); err != nil {
					log.Printf("grapple relay error from %s to %s: %v", remoteAddr.String(), other.String(), err)
				}
			}
		}

		if message == "disconnect" {
			server.removeClient(remoteAddr)
		}
	}
}
