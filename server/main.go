package main

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

// ---- 共通定数 ----

const (
	makeRoomPort  = ":8080"
	battlePort    = ":9052"
	maxPlayers    = 2
	clientTimeout = 5 * time.Second
	initialHP     = 100
	hitDamage     = 20
)

// ---- MakeRoom (TCP) ----

type makeRoomServer struct {
	mu      sync.Mutex
	clients map[net.Conn]struct{}
}

type matchReadyMsg struct {
	Type        string `json:"type"`
	Connected   int    `json:"connected"`
	Description string `json:"description"`
}

func newMakeRoomServer() *makeRoomServer {
	return &makeRoomServer{clients: make(map[net.Conn]struct{})}
}

func (s *makeRoomServer) add(conn net.Conn) {
	s.mu.Lock()
	before := len(s.clients)
	s.clients[conn] = struct{}{}
	after := len(s.clients)
	s.mu.Unlock()

	if before != 2 && after == 2 {
		s.broadcast(matchReadyMsg{Type: "match_ready", Connected: after, Description: "2 players connected"})
	}
}

func (s *makeRoomServer) remove(conn net.Conn) {
	s.mu.Lock()
	delete(s.clients, conn)
	s.mu.Unlock()
}

func (s *makeRoomServer) broadcast(v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		return
	}
	payload = append(payload, '\n')
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.clients))
	for c := range s.clients {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.Write(payload)
	}
}

func runMakeRoom() {
	ln, err := net.Listen("tcp", makeRoomPort)
	if err != nil {
		log.Fatalf("MakeRoom: failed to listen: %v", err)
	}
	log.Printf("MakeRoom TCP server started on %s", makeRoomPort)
	s := newMakeRoomServer()
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("MakeRoom: accept error: %v", err)
			continue
		}
		go func(c net.Conn) {
			log.Printf("MakeRoom: client connected: %s", c.RemoteAddr())
			s.add(c)
			_, _ = io.Copy(io.Discard, c)
			s.remove(c)
			c.Close()
			log.Printf("MakeRoom: client disconnected: %s", c.RemoteAddr())
		}(conn)
	}
}

// ---- Battle (UDP) ----

type battleServer struct {
	mu       sync.Mutex
	clients  map[string]*net.UDPAddr
	order    []string
	conn     *net.UDPConn
	hp       map[string]int
	lastSeen map[string]time.Time
}

type hpUpdate struct {
	Type   string `json:"type"`
	Target string `json:"target"`
	HP     int    `json:"hp"`
}

type deathMsg struct {
	Type   string `json:"type"`
	Target string `json:"target"`
}

type welcomeMsg struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func newBattleServer(conn *net.UDPConn) *battleServer {
	return &battleServer{
		clients:  make(map[string]*net.UDPAddr),
		order:    make([]string, 0, maxPlayers),
		conn:     conn,
		hp:       make(map[string]int),
		lastSeen: make(map[string]time.Time),
	}
}

func (s *battleServer) register(addr *net.UDPAddr) (known, accepted, becameFull bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := addr.String()
	if _, ok := s.clients[key]; ok {
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
		s.hp[key] = initialHP
	}
	return false, true, len(s.clients) == maxPlayers
}

func (s *battleServer) remove(addr *net.UDPAddr) {
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

func (s *battleServer) evictStale() {
	s.mu.Lock()
	var stale []string
	for key, t := range s.lastSeen {
		if time.Since(t) > clientTimeout {
			stale = append(stale, key)
		}
	}
	s.mu.Unlock()
	for _, key := range stale {
		log.Printf("Battle: client %s timed out", key)
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

func (s *battleServer) other(sender *net.UDPAddr) *net.UDPAddr {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, addr := range s.clients {
		if key != sender.String() {
			return addr
		}
	}
	return nil
}

func (s *battleServer) applyDamage(target string, dmg int) int {
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

func (s *battleServer) getHP(target string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.hp[target]
	return v, ok
}

func (s *battleServer) clientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

func (s *battleServer) broadcastJSON(v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.mu.Lock()
	addrs := make([]*net.UDPAddr, 0, len(s.clients))
	for _, a := range s.clients {
		addrs = append(addrs, a)
	}
	s.mu.Unlock()
	for _, a := range addrs {
		s.conn.WriteToUDP(payload, a)
	}
}

func runBattle() {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 9052})
	if err != nil {
		log.Fatalf("Battle: failed to listen: %v", err)
	}
	log.Printf("Battle UDP server started on %s", battlePort)
	s := newBattleServer(conn)

	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for range t.C {
			s.evictStale()
		}
	}()

	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("Battle: read error: %v", err)
			continue
		}
		msg := strings.TrimSpace(string(buf[:n]))
		known, accepted, becameFull := s.register(addr)
		if !accepted {
			log.Printf("Battle: ignored packet from %s (room full)", addr)
			continue
		}
		if !known {
			if hp, ok := s.getHP(addr.String()); ok {
				s.broadcastJSON(hpUpdate{Type: "hp_update", Target: addr.String(), HP: hp})
			}
			if payload, err := json.Marshal(welcomeMsg{Type: "welcome", ID: addr.String()}); err == nil {
				conn.WriteToUDP(payload, addr)
			}
		}
		if becameFull {
			s.broadcastJSON(matchReadyMsg{Type: "match_ready", Connected: s.clientCount(), Description: "2 players connected"})
		}

		var base struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(buf[:n], &base); err == nil && base.Type == "hit_report" {
			var hr struct {
				Target string `json:"target"`
				Damage int    `json:"damage"`
			}
			if err := json.Unmarshal(buf[:n], &hr); err == nil {
				target := hr.Target
				if target == "" {
					if o := s.other(addr); o != nil {
						target = o.String()
					}
				}
				if target != "" {
					newHP := s.applyDamage(target, hitDamage)
					if newHP >= 0 {
						s.broadcastJSON(hpUpdate{Type: "hp_update", Target: target, HP: newHP})
						if newHP == 0 {
							s.broadcastJSON(deathMsg{Type: "death", Target: target})
						}
					}
				}
			}
			continue
		}

		if strings.HasPrefix(msg, "P:") || strings.HasPrefix(msg, "S:") {
			if o := s.other(addr); o != nil {
				conn.WriteToUDP(buf[:n], o)
			}
		}
		if msg == "disconnect" {
			s.remove(addr)
		}
	}
}

func main() {
	go runMakeRoom()
	runBattle()
}
