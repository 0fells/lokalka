package vpn

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"lokalka/internal/crypto"
	"lokalka/internal/proto"
	"lokalka/internal/store"
)

type memTUN struct {
	name   string
	in     chan []byte
	out    chan []byte
	once   sync.Once
	closed chan struct{}
}

func newMemTUN(name string) *memTUN {
	return &memTUN{
		name:   name,
		in:     make(chan []byte, 8),
		out:    make(chan []byte, 8),
		closed: make(chan struct{}),
	}
}

func (m *memTUN) Read(b []byte) (int, error) {
	select {
	case <-m.closed:
		return 0, io.EOF
	case pkt := <-m.in:
		return copy(b, pkt), nil
	}
}

func (m *memTUN) Write(p []byte) (int, error) {
	pkt := append([]byte(nil), p...)
	select {
	case <-m.closed:
		return 0, io.ErrClosedPipe
	case m.out <- pkt:
		return len(p), nil
	}
}

func (m *memTUN) Close() error {
	m.once.Do(func() { close(m.closed) })
	return nil
}

func (m *memTUN) Name() string { return m.name }

func testServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Create("home", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := st.Create("other", "secret"); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(st)
	if err := srv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		if srv.tcp != nil {
			_ = srv.tcp.Close()
		}
		if srv.udp != nil {
			_ = srv.udp.Close()
		}
	})
	go srv.Serve(ctx)
	return srv, st
}

func waitReady(t *testing.T, c *Client) {
	t.Helper()
	select {
	case <-c.Ready():
	case <-time.After(8 * time.Second):
		t.Fatal("клиент не подключился")
	}
}

func waitPeer(t *testing.T, c *Client, ip string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.HasPeer(ip) {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("участник %s не появился", ip)
}

func udpPacket(src, dst, payload string) []byte {
	body := []byte(payload)
	pkt := make([]byte, 28+len(body))
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
	pkt[8] = 64
	pkt[9] = 17
	copy(pkt[12:16], net.ParseIP(src).To4())
	copy(pkt[16:20], net.ParseIP(dst).To4())
	binary.BigEndian.PutUint16(pkt[10:12], protoChecksum(pkt[:20]))
	binary.BigEndian.PutUint16(pkt[20:22], 10000)
	binary.BigEndian.PutUint16(pkt[22:24], 20000)
	binary.BigEndian.PutUint16(pkt[24:26], uint16(8+len(body)))
	copy(pkt[28:], body)
	return pkt
}

func protoChecksum(h []byte) uint16 {
	sum := uint32(0)
	for i := 0; i+1 < len(h); i += 2 {
		if i == 10 {
			continue
		}
		sum += uint32(binary.BigEndian.Uint16(h[i:]))
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}

func payloadOf(pkt []byte) string {
	if len(pkt) < 28 {
		return ""
	}
	return string(pkt[28:])
}

func TestClientsExchange(t *testing.T) {
	srv, _ := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	aDev, bDev := newMemTUN("a"), newMemTUN("b")
	a := New(Config{ServerAddr: srv.Addr(), Network: "home", Password: "secret", Hostname: "alice", Device: aDev})
	b := New(Config{ServerAddr: srv.Addr(), Network: "home", Password: "secret", Hostname: "bob", Device: bDev})
	go a.Run(ctx)
	go b.Run(ctx)
	waitReady(t, a)
	waitReady(t, b)
	waitPeer(t, a, b.VirtualIP())
	waitPeer(t, b, a.VirtualIP())

	pkt := udpPacket(a.VirtualIP(), b.VirtualIP(), "hello-lokalka")
	deadline := time.Now().Add(5 * time.Second)
	for {
		aDev.in <- pkt
		select {
		case got := <-bDev.out:
			if payloadOf(got) != "hello-lokalka" {
				t.Fatalf("payload %q", payloadOf(got))
			}
			goto broadcast
		case <-time.After(200 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("пакет не дошёл")
			}
		}
	}

broadcast:
	parts := net.ParseIP(a.VirtualIP()).To4()
	bcast := net.IPv4(parts[0], parts[1], parts[2], 255).String()
	bpkt := udpPacket(a.VirtualIP(), bcast, "bcast")
	deadline = time.Now().Add(5 * time.Second)
	for {
		aDev.in <- bpkt
		select {
		case got := <-bDev.out:
			if payloadOf(got) != "bcast" {
				t.Fatalf("broadcast payload %q", payloadOf(got))
			}
			return
		case <-time.After(200 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("broadcast не дошёл")
			}
		}
	}
}

func TestWrongPassword(t *testing.T) {
	srv, _ := testServer(t)
	c := New(Config{ServerAddr: srv.Addr(), Network: "home", Password: "nope", Hostname: "eve", NoTUN: true})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := c.Run(ctx)
	if err == nil || err == context.DeadlineExceeded {
		t.Fatalf("ожидался отказ, получено %v", err)
	}
}

func TestDisconnectBlocksReconnect(t *testing.T) {
	srv, _ := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(Config{ServerAddr: srv.Addr(), Network: "home", Password: "secret", Hostname: "alice", NoTUN: true})
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	waitReady(t, c)
	ip := c.VirtualIP()
	if err := srv.DisconnectPeer("home", ip); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("клиент вышел без ошибки")
		}
		if err.Error() != "доступ закрыт администратором" {
			t.Fatalf("ошибка %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("клиент не остановился после отключения")
	}
	again := New(Config{ServerAddr: srv.Addr(), Network: "home", Password: "secret", Hostname: "alice", NoTUN: true})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	err := again.Run(ctx2)
	if err == nil || err == context.DeadlineExceeded {
		t.Fatalf("повторный вход должен быть закрыт, получено %v", err)
	}
	if err.Error() != "доступ закрыт администратором" {
		t.Fatalf("ошибка %v", err)
	}
	views := srv.Snapshot()
	if len(views) == 0 || len(views[0].Blocked) != 1 || views[0].Blocked[0].Hostname != "alice" {
		t.Fatalf("список отключённых: %+v", views)
	}
	if err := srv.AllowPeer("home", "alice"); err != nil {
		t.Fatal(err)
	}
	allowed := New(Config{ServerAddr: srv.Addr(), Network: "home", Password: "secret", Hostname: "alice", NoTUN: true})
	ctx3, cancel3 := context.WithCancel(context.Background())
	defer cancel3()
	go allowed.Run(ctx3)
	waitReady(t, allowed)
}

func TestNetworksAreIsolated(t *testing.T) {
	srv, _ := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	aDev, bDev := newMemTUN("a"), newMemTUN("b")
	a := New(Config{ServerAddr: srv.Addr(), Network: "home", Password: "secret", Hostname: "alice", Device: aDev})
	b := New(Config{ServerAddr: srv.Addr(), Network: "other", Password: "secret", Hostname: "bob", Device: bDev})
	go a.Run(ctx)
	go b.Run(ctx)
	waitReady(t, a)
	waitReady(t, b)

	deadline := time.Now().Add(3 * time.Second)
	var aKey, bIP, aIP []byte
	for time.Now().Before(deadline) {
		srv.mu.Lock()
		pa := srv.byIP[a.VirtualIP()]
		pb := srv.byIP[b.VirtualIP()]
		if pa != nil && pb != nil && pa.endpoint != nil && pb.endpoint != nil && pa.sessionKey != nil {
			aKey = append([]byte(nil), pa.sessionKey...)
			aIP = append([]byte(nil), pa.ip...)
			bIP = append([]byte(nil), pb.ip...)
			srv.mu.Unlock()
			break
		}
		srv.mu.Unlock()
		time.Sleep(30 * time.Millisecond)
	}
	if aKey == nil {
		t.Fatal("пиры не зарегистрировали UDP")
	}
	sealed, err := crypto.Seal(aKey, []byte("cross"))
	if err != nil {
		t.Fatal(err)
	}
	d := proto.Datagram{Type: proto.TypeRelay, Src: aIP, Dst: bIP, Payload: sealed}
	body, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	d.MAC = crypto.MAC(aKey, body)
	raw, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	addr, err := net.ResolveUDPAddr("udp4", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write(raw); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-bDev.out:
		t.Fatalf("чужая сеть получила пакет %q", payloadOf(got))
	case <-time.After(400 * time.Millisecond):
	}
}

func TestAdminSession(t *testing.T) {
	srv, st := testServer(t)
	if err := st.SetAdminPassword("admin-secret"); err != nil {
		t.Fatal(err)
	}
	srv.SetPublic(srv.Addr())
	if _, err := DialAdmin(srv.Addr(), "wrong-pass", nil); err == nil {
		t.Fatal("чужой пароль пустил в админку")
	}
	sess, err := DialAdmin(srv.Addr(), "admin-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.CreateConfig("home", "ноутбук", ""); err != nil {
		t.Fatal(err)
	}
	if err := sess.CreateConfig("home", "пк-2", ""); err != nil {
		t.Fatal(err)
	}
	nets, err := sess.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) == 0 || len(nets[0].Configs) < 2 {
		t.Fatalf("ожидались 2+ файла: %+v", nets)
	}
	var id string
	for _, cfg := range nets[0].Configs {
		if cfg.Label == "ноутбук" {
			id = cfg.ID
		}
	}
	if id == "" {
		t.Fatalf("конфиг не появился: %+v", nets)
	}
	label, server, network, password, err := sess.Config("home", id)
	if err != nil {
		t.Fatal(err)
	}
	if label != "ноутбук" || server != srv.Addr() || network != "home" || password != "secret" {
		t.Fatalf("файл: %s %s %s %s", label, server, network, password)
	}
	found := false
	for _, net := range nets {
		for _, cfg := range net.Configs {
			if cfg.Label == "ноутбук" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("список без конфига")
	}
	_ = sess.conn.Close()
	again, err := sess.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(again) == 0 || again[0].Name == "" {
		t.Fatal("после обрыва список пуст")
	}
}

func TestPeerReportsConfig(t *testing.T) {
	srv, st := testServer(t)
	list := st.AllConfigs()["home"]
	if len(list) == 0 {
		t.Fatal("нет конфига сети")
	}
	id := list[0].ID
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(Config{
		ServerAddr: srv.Addr(),
		Network:    "home",
		Password:   "secret",
		Hostname:   "alice",
		ConfigID:   id,
		NoTUN:      true,
	})
	go c.Run(ctx)
	waitReady(t, c)
	views := srv.Snapshot()
	if len(views) == 0 || len(views[0].Peers) != 1 {
		t.Fatalf("пиры: %+v", views)
	}
	if views[0].Peers[0].Config != "home" {
		t.Fatalf("файл входа: %+v", views[0].Peers[0])
	}
}
