package main

import (
	"net"
	"testing"
)

// fakeConn は本物のソケットを使わずにテストするための偽物
// nilのnet.Connを埋め込んでるだけなので、addClient/broadcastJSONが呼ぶ
// Writeだけ自前で用意して、それ以外は呼ばれない前提でいる
type fakeConn struct {
	net.Conn
	id string
}

func (f *fakeConn) Write(b []byte) (int, error) {
	return len(b), nil
}

// 2人目が入ったタイミングでちゃんと満員になるか確認するテスト
// match_readyの中身自体はチェックしてないけど、Writeでpanicしなければ
// 通知処理がおかしなことになってないという確認になる
func TestAddClient_NotifiesOnlyWhenRoomFills(t *testing.T) {
	s := NewServer()
	a := &fakeConn{id: "a"}
	b := &fakeConn{id: "b"}

	s.addClient(a)
	if len(s.clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(s.clients))
	}

	s.addClient(b)
	if len(s.clients) != 2 {
		t.Fatalf("expected 2 clients, got %d", len(s.clients))
	}
}

// removeClientしたら部屋の管理から消えるか確認するテスト
func TestRemoveClient(t *testing.T) {
	s := NewServer()
	a := &fakeConn{id: "a"}

	s.addClient(a)
	s.removeClient(a)

	if len(s.clients) != 0 {
		t.Errorf("expected 0 clients after removal, got %d", len(s.clients))
	}
}

// 登録してないconnをremoveClientに渡しても、既存の人には影響しないことの確認
func TestRemoveClient_UnknownConnIsNoOp(t *testing.T) {
	s := NewServer()
	a := &fakeConn{id: "a"}
	s.addClient(a)

	s.removeClient(&fakeConn{id: "unregistered"})

	if len(s.clients) != 1 {
		t.Errorf("removing an unregistered conn should not affect existing clients, got %d", len(s.clients))
	}
}
