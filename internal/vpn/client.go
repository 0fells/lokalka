package vpn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lokalka/internal/crypto"
	"lokalka/internal/proto"
	"lokalka/internal/tun"
)

type permanent struct{ err error }

func (e *permanent) Error() string { return e.err.Error() }
func (e *permanent) Unwrap() error { return e.err }

type Config struct {
	ServerAddr string

	ServerKey []byte
	Network   string
	Password  string
	Hostname  string
	ConfigID  string
	Device    tun.Device
	NoTUN     bool
}

var ErrServerKey = errors.New("сервер не подтвердил, что он настоящий: ключ не совпадает с файлом входа")

func FormatServerKey(key []byte) string {
	return base64.RawURLEncoding.EncodeToString(key)
}

func ParseServerKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	key, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("ключ сервера в файле входа повреждён")
	}
	return key, nil
}

type Client struct {
	cfg       Config
	ready     chan struct{}
	readyOnce sync.Once
	seq       uint64

	mu          sync.Mutex
	priv        []byte
	ip          net.IP
	networkIP   net.IP
	network     string
	sessionKey  []byte
	serverUDP   *net.UDPAddr
	udp         *net.UDPConn
	links       map[string]*link
	lastPeerLog string
	state       string
	detail      string
}

type link struct {
	ip         net.IP
	hostname   string
	key        []byte
	endpoint   *net.UDPAddr
	lastDirect time.Time
	punchSeq   uint64
}

func New(cfg Config) *Client {
	return &Client{
		cfg:   cfg,
		ready: make(chan struct{}),
		links: map[string]*link{},
		seq:   uint64(time.Now().UnixNano()),
	}
}

func (c *Client) Ready() <-chan struct{} { return c.ready }

func (c *Client) VirtualIP() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ip == nil {
		return ""
	}
	return c.ip.String()
}

type PeerStatus struct {
	IP       string
	Hostname string
	Direct   bool
}

type ClientStatus struct {
	State     string
	Detail    string
	Network   string
	Hostname  string
	VirtualIP string
	Peers     []PeerStatus
}

func (c *Client) Status() ClientStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := ClientStatus{
		State:   c.state,
		Detail:  c.detail,
		Network: c.network,
		Peers:   []PeerStatus{},
	}
	if st.State == "" {
		st.State = "Подключение..."
	}
	if c.ip != nil {
		st.VirtualIP = c.ip.String()
	}
	st.Hostname = c.cfg.Hostname
	if st.Network == "" {
		st.Network = c.cfg.Network
	}
	for _, l := range c.links {
		st.Peers = append(st.Peers, PeerStatus{
			IP:       l.ip.String(),
			Hostname: l.hostname,
			Direct:   !l.lastDirect.IsZero() && time.Since(l.lastDirect) < 6*time.Second,
		})
	}
	sort.Slice(st.Peers, func(i, j int) bool { return st.Peers[i].IP < st.Peers[j].IP })
	return st
}

func (c *Client) setState(state, detail string) {
	c.mu.Lock()
	c.state = state
	c.detail = detail
	c.mu.Unlock()
}

func (c *Client) HasPeer(ip string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.links[ip]
	return ok
}

func (c *Client) Run(ctx context.Context) error {
	delay := time.Duration(0)
	for {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		started := time.Now()
		err := c.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var denied *permanent
		if errors.As(err, &denied) {
			c.setState("Ошибка", err.Error())
			return denied
		}
		if err == nil {
			return nil
		}
		c.mu.Lock()
		c.ip = nil
		c.links = map[string]*link{}
		c.state = "Нет связи"
		c.detail = err.Error()
		c.mu.Unlock()
		log.Printf("соединение потеряно: %v", err)

		if time.Since(started) > time.Minute {
			delay = 0
		}
		if delay < 15*time.Second {
			delay += 2 * time.Second
		}
	}
}

func (c *Client) markReady() {
	c.readyOnce.Do(func() { close(c.ready) })
}

func (c *Client) session(ctx context.Context) error {
	c.setState("Подключение...", "")
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp4", c.cfg.ServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}

	ch, _, err := clientHandshake(conn, c.cfg.ServerKey)
	if errors.Is(err, ErrServerKey) {
		return &permanent{err: err}
	}
	if err != nil {
		return err
	}
	priv, pub, err := crypto.GenerateKeyPair()
	if err != nil {
		return err
	}
	if err := ch.Send(proto.Message{
		Type:     "join",
		Network:  c.cfg.Network,
		Password: c.cfg.Password,
		Hostname: c.cfg.Hostname,
		ConfigID: c.cfg.ConfigID,
		PubKey:   base64.StdEncoding.EncodeToString(pub),
	}); err != nil {
		return err
	}
	welcome, err := ch.Recv()
	if err != nil {
		return err
	}
	if welcome.Type == "error" {
		return &permanent{err: errors.New(welcome.Error)}
	}
	if welcome.Type != "welcome" {
		return &permanent{err: errors.New("сервер не подтвердил вход")}
	}
	ip := net.ParseIP(welcome.IP).To4()
	sessionKey, err := base64.StdEncoding.DecodeString(welcome.SessionKey)
	if ip == nil || err != nil || len(sessionKey) != crypto.KeySize {
		return &permanent{err: errors.New("сервер прислал некорректный адрес")}
	}
	ra := conn.RemoteAddr().(*net.TCPAddr)
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return err
	}
	defer udp.Close()

	dev := c.cfg.Device
	owned := false
	if dev == nil {
		if c.cfg.NoTUN {
			dev = tun.NewBlackhole()
		} else {
			opened, openErr := tun.Open(ip.String(), vpnPrefix, vpnMTU)
			if openErr != nil {
				select {
				case <-c.ready:
					return openErr
				default:
					return &permanent{err: openErr}
				}
			}
			dev = opened
		}
		owned = true
	}
	if owned {
		defer dev.Close()
	}

	networkIP := append(net.IP(nil), ip...)
	networkIP[3] = 0
	c.mu.Lock()
	c.priv = priv
	c.ip = append(net.IP(nil), ip...)
	c.networkIP = networkIP
	c.network = c.cfg.Network
	c.sessionKey = sessionKey
	c.serverUDP = &net.UDPAddr{IP: append(net.IP(nil), ra.IP...), Port: ra.Port}
	c.udp = udp
	c.links = map[string]*link{}
	c.mu.Unlock()

	log.Printf("сеть %s, адрес %s/24, интерфейс %s", c.cfg.Network, ip, dev.Name())
	c.setState("Подключено", "")
	if c.cfg.NoTUN {
		log.Printf("адаптер не поднимался: проверка входа без виртуальной сети")
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-sctx.Done()
		_ = conn.Close()
		_ = udp.Close()
		if owned {
			_ = dev.Close()
		}
	}()

	var wg sync.WaitGroup
	start := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}
	start(func() { c.udpLoop(sctx, dev) })
	start(func() { c.tunLoop(sctx, dev) })
	start(func() { c.keepalive(sctx) })
	start(func() { c.punchLoop(sctx) })
	start(func() { c.pingLoop(sctx, conn, ch) })
	c.markReady()

	err = c.controlLoop(conn, ch)
	cancel()
	wg.Wait()
	return err
}

func (c *Client) controlLoop(conn net.Conn, ch *proto.Channel) error {
	for {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		msg, err := ch.Recv()
		if err != nil {
			return err
		}
		switch msg.Type {
		case "peers":
			c.applyPeers(msg.Peers)
		case "error":
			return &permanent{err: errors.New(msg.Error)}
		}
	}
}

func (c *Client) applyPeers(peers []proto.PeerInfo) {
	c.mu.Lock()
	next := make(map[string]*link, len(peers))
	for _, info := range peers {
		ip := net.ParseIP(info.IP).To4()
		pub, err := base64.StdEncoding.DecodeString(info.PubKey)
		if ip == nil || err != nil || len(pub) != 32 || c.priv == nil {
			continue
		}
		key, err := crypto.DeriveKey(c.priv, pub, nil, proto.PairInfo(c.network, c.ip.String(), ip.String()))
		if err != nil {
			continue
		}
		l := &link{ip: append(net.IP(nil), ip...), hostname: info.Hostname, key: key}
		if info.Endpoint != "" {
			if addr, err := net.ResolveUDPAddr("udp4", info.Endpoint); err == nil {
				l.endpoint = addr
			}
		}
		if old := c.links[ip.String()]; old != nil {
			l.lastDirect = old.lastDirect
			if bytes.Equal(old.key, key) {
				l.punchSeq = old.punchSeq
			}

			if old.endpoint != nil && !old.lastDirect.IsZero() && time.Since(old.lastDirect) < 6*time.Second {
				l.endpoint = old.endpoint
			}
			if l.endpoint == nil {
				l.endpoint = old.endpoint
			}
		}
		next[ip.String()] = l
	}
	c.links = next
	var text string
	if len(next) == 0 {
		text = fmt.Sprintf("в сети %s пока только вы", c.network)
	} else {
		ips := make([]string, 0, len(next))
		for ip := range next {
			ips = append(ips, ip)
		}
		sort.Strings(ips)
		var b strings.Builder
		fmt.Fprintf(&b, "участники сети %s:\n", c.network)
		for _, ip := range ips {
			l := next[ip]
			mode := "через сервер"
			if !l.lastDirect.IsZero() && time.Since(l.lastDirect) < 6*time.Second {
				mode = "напрямую"
			}
			fmt.Fprintf(&b, "  %s  %s  %s\n", l.ip, l.hostname, mode)
		}
		text = strings.TrimRight(b.String(), "\n")
	}
	if text == c.lastPeerLog {
		c.mu.Unlock()
		return
	}
	c.lastPeerLog = text
	c.mu.Unlock()
	log.Print(text)
}

func (c *Client) tunLoop(ctx context.Context, dev tun.Device) {
	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := dev.Read(buf)
		if err != nil {
			return
		}
		c.handleOutbound(buf[:n])
	}
}

func (c *Client) handleOutbound(pkt []byte) {
	c.mu.Lock()
	if c.ip == nil {
		c.mu.Unlock()
		return
	}
	myIP := append(net.IP(nil), c.ip...)
	networkIP := append(net.IP(nil), c.networkIP...)
	c.mu.Unlock()
	normalized, err := proto.NormalizeIPv4(pkt, myIP)
	if err != nil {
		return
	}
	dst := net.IP(normalized[16:20])
	if dst.Equal(myIP) {
		return
	}
	if proto.SameSubnetBroadcast(dst, networkIP, vpnPrefix) {
		for _, l := range c.snapshotLinks() {
			c.sendTo(l, normalized)
		}
		return
	}
	c.mu.Lock()
	l := c.links[dst.String()]
	c.mu.Unlock()
	if l == nil {
		return
	}
	c.sendTo(l, normalized)
}

func (c *Client) snapshotLinks() []*link {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*link, 0, len(c.links))
	for _, l := range c.links {
		out = append(out, l)
	}
	return out
}

func (c *Client) sendTo(l *link, pkt []byte) {
	c.mu.Lock()
	direct := l.endpoint != nil && !l.lastDirect.IsZero() && time.Since(l.lastDirect) < 6*time.Second
	endpoint := l.endpoint
	key := l.key
	session := append([]byte(nil), c.sessionKey...)
	myIP := append(net.IP(nil), c.ip...)
	dst := append(net.IP(nil), l.ip...)
	server := c.serverUDP
	udp := c.udp
	c.mu.Unlock()
	if udp == nil || key == nil {
		return
	}
	sealed, err := crypto.Seal(key, pkt)
	if err != nil {
		return
	}
	d := proto.Datagram{Src: myIP, Dst: dst, Payload: sealed}
	if direct {
		d.Type = proto.TypeDirect
		raw, err := d.Marshal()
		if err != nil {
			return
		}
		_, _ = udp.WriteToUDP(raw, endpoint)
		return
	}
	d.Type = proto.TypeRelay
	body, err := d.Marshal()
	if err != nil {
		return
	}
	d.MAC = crypto.MAC(session, body)
	raw, err := d.Marshal()
	if err != nil {
		return
	}
	_, _ = udp.WriteToUDP(raw, server)
}

func (c *Client) udpLoop(ctx context.Context, dev tun.Device) {
	buf := make([]byte, 2048)
	for {
		_ = c.udp.SetReadDeadline(time.Now().Add(time.Second))
		n, addr, err := c.udp.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}
			return
		}
		d, err := proto.ParseDatagram(buf[:n])
		if err != nil {
			continue
		}
		c.mu.Lock()
		myIP := c.ip
		networkIP := c.networkIP
		l := c.links[d.Src.String()]
		var key []byte
		if l != nil {
			key = l.key
		}
		c.mu.Unlock()
		if myIP == nil || !d.Dst.Equal(myIP) || key == nil {
			continue
		}
		plain, err := crypto.Open(key, d.Payload)
		if err != nil {
			continue
		}
		if (d.Type == proto.TypeDirect || d.Type == proto.TypeRelay) && !innerAddrOK(plain, d.Src, myIP, networkIP) {
			continue
		}
		switch d.Type {
		case proto.TypePunch, proto.TypePunchAck:
			if len(plain) != 8 || !c.acceptPunch(d.Src.String(), binary.BigEndian.Uint64(plain), addr) {
				continue
			}
			if d.Type == proto.TypePunch {
				c.replyPunch(d.Src.String())
			}
		case proto.TypeDirect, proto.TypeRelay:
			if d.Type == proto.TypeDirect {
				c.refreshDirect(d.Src.String(), addr)
			}
			_, _ = dev.Write(plain)
		}
	}
}

func innerAddrOK(pkt []byte, src, myIP, networkIP net.IP) bool {
	if len(pkt) < 20 || pkt[0]>>4 != 4 || !net.IP(pkt[12:16]).Equal(src) {
		return false
	}
	dst := net.IP(pkt[16:20])
	return dst.Equal(myIP) || proto.SameSubnetBroadcast(dst, networkIP, vpnPrefix)
}

func (c *Client) acceptPunch(ip string, seq uint64, addr *net.UDPAddr) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.links[ip]
	if l == nil || seq <= l.punchSeq || addr == nil {
		return false
	}
	l.punchSeq = seq
	l.lastDirect = time.Now()
	l.endpoint = cloneUDP(addr)
	return true
}

func (c *Client) refreshDirect(ip string, addr *net.UDPAddr) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l := c.links[ip]; l != nil && sameUDP(l.endpoint, addr) {
		l.lastDirect = time.Now()
	}
}

func (c *Client) nextSeq() []byte {
	return binary.BigEndian.AppendUint64(nil, atomic.AddUint64(&c.seq, 1))
}

func (c *Client) replyPunch(ip string) {
	c.mu.Lock()
	l := c.links[ip]
	if l == nil || l.endpoint == nil || c.udp == nil {
		c.mu.Unlock()
		return
	}
	key := l.key
	dst := append(net.IP(nil), l.ip...)
	endpoint := l.endpoint
	myIP := append(net.IP(nil), c.ip...)
	udp := c.udp
	c.mu.Unlock()
	sealed, err := crypto.Seal(key, c.nextSeq())
	if err != nil {
		return
	}
	raw, err := (proto.Datagram{Type: proto.TypePunchAck, Src: myIP, Dst: dst, Payload: sealed}).Marshal()
	if err != nil {
		return
	}
	_, _ = udp.WriteToUDP(raw, endpoint)
}

func (c *Client) keepalive(ctx context.Context) {
	send := func() {
		c.mu.Lock()
		if c.ip == nil || c.udp == nil || c.serverUDP == nil {
			c.mu.Unlock()
			return
		}
		d := proto.Datagram{Type: proto.TypeKeepalive, Src: append(net.IP(nil), c.ip...), Dst: net.IPv4(0, 0, 0, 0), Payload: c.nextSeq()}
		body, err := d.Marshal()
		if err != nil {
			c.mu.Unlock()
			return
		}
		d.MAC = crypto.MAC(c.sessionKey, body)
		raw, err := d.Marshal()
		udp := c.udp
		dst := c.serverUDP
		c.mu.Unlock()
		if err != nil {
			return
		}
		_, _ = udp.WriteToUDP(raw, dst)
	}

	send()
	for _, wait := range []time.Duration{time.Second, 2 * time.Second} {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
			send()
		}
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

func (c *Client) punchLoop(ctx context.Context) {
	send := func() {
		c.mu.Lock()
		type target struct {
			ip  net.IP
			key []byte
			ep  *net.UDPAddr
		}
		var targets []target
		myIP := append(net.IP(nil), c.ip...)
		udp := c.udp
		for _, l := range c.links {
			if l.endpoint == nil {
				continue
			}
			targets = append(targets, target{ip: append(net.IP(nil), l.ip...), key: l.key, ep: l.endpoint})
		}
		c.mu.Unlock()
		if udp == nil {
			return
		}
		for _, t := range targets {
			sealed, err := crypto.Seal(t.key, c.nextSeq())
			if err != nil {
				continue
			}
			raw, err := (proto.Datagram{Type: proto.TypePunch, Src: myIP, Dst: t.ip, Payload: sealed}).Marshal()
			if err != nil {
				continue
			}
			_, _ = udp.WriteToUDP(raw, t.ep)
		}
	}
	send()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

func (c *Client) pingLoop(ctx context.Context, conn net.Conn, ch *proto.Channel) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := ch.Send(proto.Message{Type: "ping"}); err != nil {
				_ = conn.Close()
				return
			}
		}
	}
}

func clientHandshake(conn net.Conn, pin []byte) (ch *proto.Channel, serverKey []byte, err error) {
	priv, pub, err := crypto.GenerateKeyPair()
	if err != nil {
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(proto.ClientHello(pub)); err != nil {
		return nil, nil, err
	}
	buf := make([]byte, proto.ServerHelloSize)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, nil, err
	}
	peerPub, salt, serverKey, err := proto.ParseServerHello(buf, pub)
	if err != nil {
		return nil, nil, err
	}
	if pin != nil && !bytes.Equal(pin, serverKey) {
		return nil, nil, ErrServerKey
	}
	toServer, toClient, err := proto.DeriveControlKeys(priv, peerPub, salt)
	if err != nil {
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	ch, err = proto.NewChannel(conn, toServer, toClient)
	return ch, serverKey, err
}
