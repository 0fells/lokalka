package vpn

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"

	"lokalka/internal/crypto"
	"lokalka/internal/proto"
)

func TestPinnedServerKey(t *testing.T) {
	srv, _ := testServer(t)
	good, err := ParseServerKey(srv.ServerKey())
	if err != nil || len(good) != ed25519.PublicKeySize {
		t.Fatalf("ключ сервера %q: %v", srv.ServerKey(), err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ok := New(Config{ServerAddr: srv.Addr(), ServerKey: good, Network: "home", Password: "secret", Hostname: "alice", NoTUN: true})
	go ok.Run(ctx)
	waitReady(t, ok)

	other, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	bad := New(Config{ServerAddr: srv.Addr(), ServerKey: other, Network: "home", Password: "secret", Hostname: "bob", NoTUN: true})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := bad.Run(ctx2); !errors.Is(err, ErrServerKey) {
		t.Fatalf("чужой ключ сервера: ждали ErrServerKey, получили %v", err)
	}

	if _, err := DialAdmin(srv.Addr(), "x", other); !errors.Is(err, ErrServerKey) || !errors.Is(err, ErrDenied) {
		t.Fatalf("админка с чужим ключом: %v", err)
	}
}

func TestSilentConnectionDropped(t *testing.T) {
	prev := helloWait
	helloWait = 300 * time.Millisecond
	defer func() { helloWait = prev }()
	srv, _ := testServer(t)
	conn, err := net.Dial("tcp4", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, _, err := clientHandshake(conn, nil); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var one [1]byte
	if _, err := conn.Read(one[:]); err == nil {
		t.Fatal("сервер прислал данные молчащему соединению")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("сервер не закрыл молчащее соединение")
	}
}

func TestInnerAddrOK(t *testing.T) {
	me := net.ParseIP("10.66.1.3").To4()
	netIP := net.ParseIP("10.66.1.0").To4()
	src := net.ParseIP("10.66.1.2").To4()
	cases := []struct {
		name     string
		from, to string
		want     bool
	}{
		{"обычный", "10.66.1.2", "10.66.1.3", true},
		{"широковещание", "10.66.1.2", "10.66.1.255", true},
		{"чужой отправитель", "10.66.1.9", "10.66.1.3", false},
		{"не мне", "10.66.1.2", "10.66.1.7", false},
	}
	for _, tc := range cases {
		pkt := udpPacket(tc.from, tc.to, "x")
		if got := innerAddrOK(pkt, src, me, netIP); got != tc.want {
			t.Errorf("%s: %v, ждали %v", tc.name, got, tc.want)
		}
	}
	if innerAddrOK([]byte{0x45, 0}, src, me, netIP) {
		t.Error("короткий пакет принят")
	}
}

func TestReplayDoesNotMoveEndpoint(t *testing.T) {
	srv, _ := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := New(Config{ServerAddr: srv.Addr(), Network: "home", Password: "secret", Hostname: "alice", NoTUN: true})
	go a.Run(ctx)
	waitReady(t, a)

	var key, ip []byte
	var before *net.UDPAddr
	var seq uint64
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && before == nil {
		srv.mu.Lock()
		if p := srv.byIP[a.VirtualIP()]; p != nil && p.endpoint != nil {
			key, ip, before, seq = append([]byte(nil), p.sessionKey...), append([]byte(nil), p.ip...), cloneUDP(p.endpoint), p.keepSeq
		}
		srv.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	if before == nil {
		t.Fatal("сервер не узнал UDP-адрес участника")
	}
	serverUDP, _ := net.ResolveUDPAddr("udp4", srv.Addr())
	attacker, err := net.DialUDP("udp4", nil, serverUDP)
	if err != nil {
		t.Fatal(err)
	}
	defer attacker.Close()
	send := func(d proto.Datagram) {
		body, _ := d.Marshal()
		d.MAC = crypto.MAC(key, body)
		raw, _ := d.Marshal()
		attacker.Write(raw)
		time.Sleep(150 * time.Millisecond)
	}
	endpoint := func() *net.UDPAddr {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return cloneUDP(srv.byIP[a.VirtualIP()].endpoint)
	}
	other := net.ParseIP("10.66.1.200").To4()
	send(proto.Datagram{Type: proto.TypeRelay, Src: ip, Dst: other, Payload: make([]byte, 40)})
	if !sameUDP(endpoint(), before) {
		t.Fatal("релейная датаграмма с чужого адреса увела адрес участника")
	}
	send(proto.Datagram{Type: proto.TypeKeepalive, Src: ip, Dst: net.IPv4zero, Payload: binary.BigEndian.AppendUint64(nil, seq)})
	if !sameUDP(endpoint(), before) {
		t.Fatal("повторённый keepalive увёл адрес участника")
	}
	send(proto.Datagram{Type: proto.TypeKeepalive, Src: ip, Dst: net.IPv4zero, Payload: binary.BigEndian.AppendUint64(nil, seq+1_000_000_000_000)})
	if sameUDP(endpoint(), before) {
		t.Fatal("свежий keepalive с нового адреса не обновил адрес: переезд в другую сеть сломан")
	}
}

func TestStalePunchIgnored(t *testing.T) {
	c := New(Config{})
	c.links["10.66.1.3"] = &link{ip: net.ParseIP("10.66.1.3").To4()}
	good := &net.UDPAddr{IP: net.ParseIP("198.51.100.24"), Port: 40000}
	evil := &net.UDPAddr{IP: net.ParseIP("198.51.100.77"), Port: 40000}
	if !c.acceptPunch("10.66.1.3", 100, good) {
		t.Fatal("свежая пробивка отброшена")
	}
	if c.acceptPunch("10.66.1.3", 100, evil) || c.acceptPunch("10.66.1.3", 99, evil) {
		t.Fatal("повторённая пробивка с чужого адреса принята")
	}
	if !sameUDP(c.links["10.66.1.3"].endpoint, good) {
		t.Fatal("адрес пары уведён")
	}
	c.refreshDirect("10.66.1.3", evil)
	before := c.links["10.66.1.3"].lastDirect
	time.Sleep(5 * time.Millisecond)
	c.refreshDirect("10.66.1.3", evil)
	if c.links["10.66.1.3"].lastDirect != before {
		t.Fatal("пакет с чужого адреса продлил прямую связь")
	}
}

func TestServerAddr(t *testing.T) {
	cases := map[string]string{
		"203.0.113.10":         "203.0.113.10:51820",
		" 203.0.113.10:40000 ": "203.0.113.10:40000",
		"vpn.example.com":      "vpn.example.com:51820",
		"[2001:db8::1]:51820":  "[2001:db8::1]:51820",
	}
	for in, want := range cases {
		if got, err := ServerAddr(in); err != nil || got != want {
			t.Errorf("%q: %q %v, ждали %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "203.0.113.10:99999", "http://203.0.113.10", "203.0.113.10:abc"} {
		if got, err := ServerAddr(bad); err == nil {
			t.Errorf("%q принят как %q", bad, got)
		}
	}
}
