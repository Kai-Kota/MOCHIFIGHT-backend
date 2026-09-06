package main

import (
	"net"
	"testing"
	"time"
)

// addr はテスト用にポート番号だけ変えたUDPアドレスを作るヘルパー。
// registerClient等はアドレスの文字列表現をキーにするため、ポートを変えれば
// 別クライアントとして扱われる。
func addr(port int) *net.UDPAddr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
}

// 初回登録では known=false・accepted=true・becameFull=false となり、
// HPが InitialHP で初期化されることを確認する。
func TestRegisterClient_FirstTimeIsUnknownAndAccepted(t *testing.T) {
	s := NewServer(nil)

	known, accepted, becameFull := s.registerClient(addr(1))

	if known {
		t.Error("first registration should not be known")
	}
	if !accepted {
		t.Error("first registration should be accepted")
	}
	if becameFull {
		t.Error("room should not be full with only one client")
	}
	if hp, ok := s.GetHP(addr(1).String()); !ok || hp != InitialHP {
		t.Errorf("expected initial HP %d, got %d (ok=%v)", InitialHP, hp, ok)
	}
}

// 2人目が登録されたタイミングで becameFull が true になり、
// match_ready 通知を出すべきタイミングをサーバーが正しく検知できることを確認する。
func TestRegisterClient_SecondClientFillsRoom(t *testing.T) {
	s := NewServer(nil)
	s.registerClient(addr(1))

	known, accepted, becameFull := s.registerClient(addr(2))

	if known {
		t.Error("second client should not be known yet")
	}
	if !accepted {
		t.Error("second client should be accepted")
	}
	if !becameFull {
		t.Error("room should become full with two clients")
	}
}

// 既に2人が対戦中の部屋に3人目が来た場合、accepted=false で弾かれ、
// 部屋の人数(=2)が変化しないことを確認する。
func TestRegisterClient_ThirdClientIsRejected(t *testing.T) {
	s := NewServer(nil)
	s.registerClient(addr(1))
	s.registerClient(addr(2))

	known, accepted, becameFull := s.registerClient(addr(3))

	if known {
		t.Error("rejected client should not be reported as known")
	}
	if accepted {
		t.Error("third client should be rejected once the room is full")
	}
	if becameFull {
		t.Error("becameFull should be false when the client was rejected")
	}
	if s.clientCount() != 2 {
		t.Errorf("expected room to stay at 2 clients, got %d", s.clientCount())
	}
}

// 既に登録済みのアドレスから再度パケットが来た場合は known=true として扱われ、
// (新規参加ではなく生存確認としての)再登録であることを確認する。
func TestRegisterClient_ExistingClientIsKnown(t *testing.T) {
	s := NewServer(nil)
	s.registerClient(addr(1))

	known, accepted, becameFull := s.registerClient(addr(1))

	if !known {
		t.Error("re-registering the same address should be known")
	}
	if !accepted {
		t.Error("re-registering the same address should be accepted")
	}
	if becameFull {
		t.Error("becameFull should be false on a repeat registration")
	}
}

// removeClient で退室させると、クライアント数とHP情報が両方消え、
// 空いた枠に新しいクライアントが入れるようになることを確認する。
func TestRemoveClient(t *testing.T) {
	s := NewServer(nil)
	s.registerClient(addr(1))
	s.registerClient(addr(2))

	s.removeClient(addr(1))

	if s.clientCount() != 1 {
		t.Errorf("expected 1 client remaining, got %d", s.clientCount())
	}
	if _, ok := s.GetHP(addr(1).String()); ok {
		t.Error("HP entry for removed client should be gone")
	}
	// 退室によって空いた枠が再利用できることを確認する。
	_, accepted, _ := s.registerClient(addr(3))
	if !accepted {
		t.Error("expected slot to be reusable after removal")
	}
}

// otherClient が「自分以外のもう一方のプレイヤー」を正しく返すことを確認する。
// 対戦は常に2人なので、自分ではないアドレスを渡せば必ず相手が返るはず。
func TestOtherClient(t *testing.T) {
	s := NewServer(nil)
	s.registerClient(addr(1))
	s.registerClient(addr(2))

	other := s.otherClient(addr(1))
	if other == nil || other.String() != addr(2).String() {
		t.Errorf("expected other client to be %s, got %v", addr(2), other)
	}

	if got := s.otherClient(addr(99)); got == nil {
		t.Error("with two registered clients, any address not matching one of them should return the other client")
	}
}

// ApplyDamage が HitDamage 分だけ正しくHPを減算することを確認する。
func TestApplyDamage(t *testing.T) {
	s := NewServer(nil)
	s.registerClient(addr(1))

	newHP := s.ApplyDamage(addr(1).String(), HitDamage)

	if newHP != InitialHP-HitDamage {
		t.Errorf("expected HP %d after damage, got %d", InitialHP-HitDamage, newHP)
	}
}

// 何度もダメージを与えてもHPが負の値にならず、0でクランプされることを確認する。
func TestApplyDamage_ClampsAtZero(t *testing.T) {
	s := NewServer(nil)
	s.registerClient(addr(1))

	for i := 0; i < InitialHP/HitDamage+2; i++ {
		s.ApplyDamage(addr(1).String(), HitDamage)
	}

	if hp, _ := s.GetHP(addr(1).String()); hp != 0 {
		t.Errorf("expected HP to clamp at 0, got %d", hp)
	}
}

// 登録されていない(存在しない)ターゲットにダメージを与えようとした場合、
// -1 が返ってサーバー側で「対象なし」と判定できることを確認する。
func TestApplyDamage_UnknownTargetReturnsNegativeOne(t *testing.T) {
	s := NewServer(nil)

	if got := s.ApplyDamage("unknown", HitDamage); got != -1 {
		t.Errorf("expected -1 for unknown target, got %d", got)
	}
}

// evictStaleClients が clientTimeout を超えて音信不通のクライアントだけを
// 退室させ、まだ生存している(lastSeenが新しい)クライアントには影響しないことを確認する。
func TestEvictStaleClients_RemovesOnlyExpiredEntries(t *testing.T) {
	s := NewServer(nil)
	s.registerClient(addr(1))
	s.registerClient(addr(2))

	// client 1 の最終受信時刻を強制的に過去にずらし、タイムアウト状態を再現する。
	s.mu.Lock()
	s.lastSeen[addr(1).String()] = time.Now().Add(-2 * clientTimeout)
	s.mu.Unlock()

	s.evictStaleClients()

	if s.clientCount() != 1 {
		t.Fatalf("expected 1 client left after eviction, got %d", s.clientCount())
	}
	if _, ok := s.GetHP(addr(2).String()); !ok {
		t.Error("fresh client should not have been evicted")
	}
	if _, ok := s.GetHP(addr(1).String()); ok {
		t.Error("stale client should have been evicted")
	}
}
