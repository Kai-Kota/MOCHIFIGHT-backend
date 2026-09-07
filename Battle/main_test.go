package main

import (
	"net"
	"testing"
	"time"
)

// addr はテスト用にポート番号だけ変えたUDPアドレスを作るための小道具
// registerClientとかは文字列化したアドレスをキーにしてるので、
// ポート変えるだけで別人として扱われるはず。
func addr(port int) *net.UDPAddr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
}

// 初めて登録したときにknown=false, accepted=true, becameFull=falseになって、
// HPもInitialHPで初期化されるかを確認するテスト
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

// 2人目が入ってきたタイミングでbecameFullがtrueになるか確認するテスト
// ここがちゃんと動かないとmatch_readyが飛ばなくなる
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

// 2人埋まってるところに3人目が来たらちゃんと弾かれるか
// 人数が2のまま変わらないことを確認するテスト
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

// 一回登録した人からまたパケットが来たときはknown=trueになるか
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

// removeClientで退室させたら人数とHPの情報が消えて
// 空いた枠にまた新しい人が入れるようになるかを確認するテスト
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
	// 枠が空いたので新しい人が入れるはず
	_, accepted, _ := s.registerClient(addr(3))
	if !accepted {
		t.Error("expected slot to be reusable after removal")
	}
}

// otherClientがもう一方のプレイヤーを返すか確認するテスト
// 2人対戦なので、自分じゃないアドレスを渡せば絶対相手が返ってくるはず
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

// ApplyDamageでHitDamage分HPが減るか確認するテスト
func TestApplyDamage(t *testing.T) {
	s := NewServer(nil)
	s.registerClient(addr(1))

	newHP := s.ApplyDamage(addr(1).String(), HitDamage)

	if newHP != InitialHP-HitDamage {
		t.Errorf("expected HP %d after damage, got %d", InitialHP-HitDamage, newHP)
	}
}

// 何回もダメージ与え続けてもHPがマイナスにならず0で止まることを確認するテスト
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

// いない人にダメージ与えようとしたら-1が返ってくることを確認するテスト
func TestApplyDamage_UnknownTargetReturnsNegativeOne(t *testing.T) {
	s := NewServer(nil)

	if got := s.ApplyDamage("unknown", HitDamage); got != -1 {
		t.Errorf("expected -1 for unknown target, got %d", got)
	}
}

// evictStaleClientsがタイムアウトした人だけをちゃんと退室させて、
// まだ生きてる人には影響しないことを確認するテスト
func TestEvictStaleClients_RemovesOnlyExpiredEntries(t *testing.T) {
	s := NewServer(nil)
	s.registerClient(addr(1))
	s.registerClient(addr(2))

	// client 1 のlastSeenを無理やり過去にずらしてタイムアウトを再現する
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
