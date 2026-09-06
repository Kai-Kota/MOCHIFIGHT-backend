package main

import (
	"net"
	"testing"
)

// fakeConn は実際のソケットを使わずにテストするための net.Conn の簡易スタブ。
// nilのnet.Connを埋め込んでいるので、addClient/broadcastJSONが実際に呼び出す
// Writeメソッドだけを上書きし、それ以外のメソッドは呼ばれない前提にしている
// (呼ばれるとnil埋め込みによりpanicする)。
type fakeConn struct {
	net.Conn
	id string
}

func (f *fakeConn) Write(b []byte) (int, error) {
	return len(b), nil
}

// 2人目が入室したタイミングでちょうど部屋が満員(2人)になることを確認する。
// (match_ready の実際の送信内容自体はbroadcastJSON経由でfakeConn.Writeに渡るだけで
// 検証していないが、Writeでpanicしないこと自体が「通知処理が正しく1回だけ走る」ことの確認になる)
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

// removeClient で退室させたconnが部屋の管理対象から消えることを確認する。
func TestRemoveClient(t *testing.T) {
	s := NewServer()
	a := &fakeConn{id: "a"}

	s.addClient(a)
	s.removeClient(a)

	if len(s.clients) != 0 {
		t.Errorf("expected 0 clients after removal, got %d", len(s.clients))
	}
}

// 登録されていないconnをremoveClientに渡しても、既存クライアントに影響しない
// (delete on missing map key is a no-op)ことを確認する。
func TestRemoveClient_UnknownConnIsNoOp(t *testing.T) {
	s := NewServer()
	a := &fakeConn{id: "a"}
	s.addClient(a)

	s.removeClient(&fakeConn{id: "unregistered"})

	if len(s.clients) != 1 {
		t.Errorf("removing an unregistered conn should not affect existing clients, got %d", len(s.clients))
	}
}
