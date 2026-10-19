package vpn

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"lokalka/internal/admin"
	"lokalka/internal/crypto"
	"lokalka/internal/proto"
	"lokalka/internal/store"
)

const (
	maxPeers   = 64
	vpnMTU     = 1280
	vpnPrefix  = 24
	joinWindow = time.Minute
	maxJoins   = 8
	udpFresh   = 45 * time.Second
)

var helloWait = 15 * time.Second

type Server struct {
	store     *store.Store
	identity  ed25519.PrivateKey
	public    string
	tcp       *net.TCPListener
	udp       *net.UDPConn
	mu        sync.Mutex
	byIP      map[string]*peer
	byName    map[string]*peer
	byNet     map[string]map[string]*peer
	attempts  map[string][]time.Time
	lastSweep time.Time
	panelOn   map[string]int
}

type peer struct {
	ip          net.IP
	hostname    string
	network     string
	configID    string
	configLabel string
	pub         []byte
	sessionKey  []byte
	endpoint    *net.UDPAddr
	joined      time.Time
	lastUDP     time.Time
	keepSeq     uint64
	conn        net.Conn
	out         chan []byte
	done        chan struct{}
	stopOnce    sync.Once
}

func NewServer(st *store.Store) *Server {
	return &Server{
		store:    st,
		identity: st.Identity(),
		byIP:     map[string]*peer{},
		byName:   map[string]*peer{},
		byNet:    map[string]map[string]*peer{},
		attempts: map[string][]time.Time{},
		panelOn:  map[string]int{},
	}
}

func (s *Server) Listen(addr string) error {
	tcpAddr, err := net.ResolveTCPAddr("tcp4", addr)
	if err != nil {
		return err
	}
	ln, err := net.ListenTCP("tcp4", tcpAddr)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ip := tcpAddr.IP
	if ip == nil || ip.IsUnspecified() {
		ip = net.IPv4zero
	}
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: port})
	if err != nil {
		ln.Close()
		return err
	}
	s.tcp = ln
	s.udp = uc
	return nil
}

func (s *Server) SetPublic(addr string) {
	s.public = strings.TrimSpace(addr)
}

func (s *Server) PublicEndpoint() string { return s.public }

func (s *Server) ServerKey() string {
	return FormatServerKey(s.identity.Public().(ed25519.PublicKey))
}

func (s *Server) Addr() string {
	if s.tcp == nil {
		return ""
	}
	return s.tcp.Addr().String()
}

func (s *Server) Serve(ctx context.Context) error {
	var once sync.Once
	stop := func() {
		once.Do(func() {
			if s.tcp != nil {
				_ = s.tcp.Close()
			}
			if s.udp != nil {
				_ = s.udp.Close()
			}
		})
	}
	go func() {
		<-ctx.Done()
		stop()
	}()
	go s.udpLoop()
	for {
		conn, err := s.tcp.Accept()
		if err != nil {
			if ctx.Err() != nil {
				stop()
				return nil
			}
			continue
		}
		go s.handleConn(conn)
	}
}

func (s *Server) CreateNetwork(name, password string) error {
	return s.store.Create(name, password)
}

func (s *Server) DeleteNetwork(name string) error {
	if err := s.store.Delete(name); err != nil {
		return err
	}
	s.kickNetwork(name)
	return nil
}

func (s *Server) SetPassword(name, password string) error {
	if err := s.store.SetPassword(name, password); err != nil {
		return err
	}
	if err := s.store.ClearBlocked(name); err != nil {
		log.Printf("сеть %s: не удалось очистить отключённых: %v", name, err)
	}
	s.kickNetwork(name)
	return nil
}

func (s *Server) CreateConfig(network, label, password string) error {
	_, err := s.store.AddConfig(network, label, password)
	return err
}

func (s *Server) RenameConfig(network, id, label string) error {
	return s.store.RenameConfig(network, id, label)
}

func (s *Server) DeleteConfig(network, id string) error {
	return s.store.DeleteConfig(network, id)
}

func (s *Server) NetworkPassword(network string) (string, error) {
	return s.store.NetworkPassword(network)
}

func (s *Server) ConfigFile(network, id string) (string, []byte, error) {
	label, password, err := s.store.OpenConfig(network, id)
	if err != nil {
		return "", nil, err
	}
	body, err := admin.ClientConfig(s.public, network, password, id, label, s.ServerKey())
	if err != nil {
		return "", nil, err
	}
	return label, body, nil
}

func (s *Server) DisconnectPeer(network, ip string) error {
	s.mu.Lock()
	p := s.byIP[ip]
	if p == nil || p.network != network {
		s.mu.Unlock()
		return errors.New("участник не в сети")
	}
	s.mu.Unlock()
	err := s.store.Block(network, p.hostname, p.ip.String())
	p.stop()
	return err
}

func (s *Server) AllowPeer(network, hostname string) error {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return errors.New("нужно имя компьютера")
	}
	return s.store.Unblock(network, hostname)
}

func (s *Server) DisconnectAll(network string) error {
	s.mu.Lock()
	var victims []*peer
	for _, p := range s.byNet[network] {
		victims = append(victims, p)
	}
	s.mu.Unlock()
	if len(victims) == 0 {
		return errors.New("в сети никого нет")
	}
	var firstErr error
	for _, p := range victims {
		if err := s.store.Block(network, p.hostname, p.ip.String()); err != nil && firstErr == nil {
			firstErr = err
		}
		p.stop()
	}
	return firstErr
}

func (s *Server) kickNetwork(name string) {
	s.mu.Lock()
	var victims []*peer
	for _, p := range s.byNet[name] {
		victims = append(victims, p)
	}
	s.mu.Unlock()
	for _, p := range victims {
		p.stop()
	}
}

func (s *Server) Snapshot() []admin.NetworkView {
	names := s.store.Names()
	sort.Strings(names)
	configs := s.store.AllConfigs()
	blocked := s.store.AllBlocked()
	panels := s.store.AllPanels()
	s.mu.Lock()
	defer s.mu.Unlock()
	views := make([]admin.NetworkView, 0, len(names))
	for _, name := range names {
		view := admin.NetworkView{Name: name, Peers: []admin.PeerView{}, Blocked: []admin.PeerView{}, Configs: []admin.ConfigView{}}
		for _, pn := range panels[name] {
			view.Panels = append(view.Panels, admin.PanelView{
				ID: pn.ID, Label: pn.Label,
				Created: pn.Created, LastSeen: pn.LastSeen, LastHost: pn.LastHost,
				Disabled: pn.Disabled, Online: s.panelOn[pn.ID] > 0,
			})
		}
		for _, cfg := range configs[name] {
			view.Configs = append(view.Configs, admin.ConfigView{
				ID:       cfg.ID,
				Label:    cfg.Label,
				Created:  cfg.Created,
				LastSeen: cfg.LastSeen,
				LastHost: cfg.LastHost,
			})
		}
		for _, b := range blocked[name] {
			view.Blocked = append(view.Blocked, admin.PeerView{IP: b.IP, Hostname: b.Hostname})
		}
		now := time.Now()
		for _, p := range s.byNet[name] {
			item := admin.PeerView{
				IP:       p.ip.String(),
				Hostname: p.hostname,
				Joined:   p.joined.Unix(),
				Path:     peerPath(p, now),
				Config:   p.configLabel,
			}
			if p.endpoint != nil {
				item.Endpoint = p.endpoint.String()
			}
			view.Peers = append(view.Peers, item)
		}
		sort.Slice(view.Peers, func(i, j int) bool { return view.Peers[i].IP < view.Peers[j].IP })
		view.Online = len(view.Peers)
		views = append(views, view)
	}
	return views
}

func (p *peer) mapKey() string { return p.network + "\x00" + p.hostname }

func peerPath(p *peer, now time.Time) string {
	if p == nil || p.lastUDP.IsZero() || now.Sub(p.lastUDP) > udpFresh {
		return "relay"
	}
	return "udp"
}

func (p *peer) stop() {
	p.stopOnce.Do(func() {
		close(p.done)
		_ = p.conn.Close()
	})
}

func (p *peer) writeLoop(ch *proto.Channel) {
	for {
		select {
		case <-p.done:
			return
		case msg := <-p.out:
			_ = p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ch.SendRaw(msg); err != nil {
				p.stop()
				return
			}
		}
	}
}

func (s *Server) handleAdmin(ch *proto.Channel, password string) {
	if !s.store.AdminOK(password) {
		_ = ch.Send(proto.Message{Type: "error", Error: "неверный пароль админки"})
		return
	}
	if err := ch.Send(proto.Message{Type: "admin-ok"}); err != nil {
		return
	}
	for {
		msg, err := ch.Recv()
		if err != nil {
			return
		}
		if err := ch.Send(s.adminCall(msg)); err != nil {
			return
		}
	}
}

func (s *Server) adminCall(msg proto.Message) proto.Message {
	switch msg.Type {
	case "admin-list":
		return proto.Message{Type: "admin-nets", Networks: toAdminNets(s.Snapshot())}
	case "admin-create-net":
		if err := s.CreateNetwork(strings.TrimSpace(msg.Network), msg.Password); err != nil {
			return adminErr(err)
		}
		return proto.Message{Type: "ok"}
	case "admin-delete-net":
		if err := s.DeleteNetwork(msg.Network); err != nil {
			return adminErr(err)
		}
		return proto.Message{Type: "ok"}
	case "admin-set-password":
		if err := s.SetPassword(msg.Network, msg.Password); err != nil {
			return adminErr(err)
		}
		return proto.Message{Type: "ok"}
	case "admin-kick":
		if err := s.DisconnectPeer(msg.Network, msg.IP); err != nil {
			return adminErr(err)
		}
		return proto.Message{Type: "ok"}
	case "admin-allow":
		if err := s.AllowPeer(msg.Network, msg.Hostname); err != nil {
			return adminErr(err)
		}
		return proto.Message{Type: "ok"}
	case "admin-kick-all":
		if err := s.DisconnectAll(msg.Network); err != nil {
			return adminErr(err)
		}
		return proto.Message{Type: "ok"}
	case "admin-net-password":
		pass, err := s.NetworkPassword(msg.Network)
		if err != nil {
			return adminErr(err)
		}
		return proto.Message{Type: "admin-net-password", Network: msg.Network, Password: pass}
	case "admin-create-config":
		if err := s.CreateConfig(msg.Network, msg.Label, msg.Password); err != nil {
			return adminErr(err)
		}
		return proto.Message{Type: "ok"}
	case "admin-rename-config":
		if err := s.RenameConfig(msg.Network, msg.ConfigID, msg.Label); err != nil {
			return adminErr(err)
		}
		return proto.Message{Type: "ok"}
	case "admin-delete-config":
		if err := s.DeleteConfig(msg.Network, msg.ConfigID); err != nil {
			return adminErr(err)
		}
		return proto.Message{Type: "ok"}
	case "admin-file":
		label, body, err := s.ConfigFile(msg.Network, msg.ConfigID)
		if err != nil {
			return adminErr(err)
		}
		var file struct {
			Server   string `json:"server"`
			Network  string `json:"network"`
			Password string `json:"password"`
		}
		if err := json.Unmarshal(body, &file); err != nil {
			return proto.Message{Type: "error", Error: "не удалось собрать файл подключения"}
		}
		return proto.Message{
			Type:     "admin-file",
			Label:    label,
			Network:  file.Network,
			Password: file.Password,
			Addr:     file.Server,
		}
	case "admin-create-panel":
		g, err := s.CreatePanel(msg.Network, msg.Label)
		if err != nil {
			return adminErr(err)
		}
		return g.message()
	case "admin-reset-panel":
		g, err := s.ResetPanel(msg.Network, msg.AdminID)
		if err != nil {
			return adminErr(err)
		}
		return g.message()
	case "admin-disable-panel", "admin-enable-panel":
		if err := s.SetPanelDisabled(msg.Network, msg.AdminID, msg.Type == "admin-disable-panel"); err != nil {
			return adminErr(err)
		}
		return proto.Message{Type: "ok"}
	case "admin-delete-panel":
		if err := s.DeletePanel(msg.Network, msg.AdminID); err != nil {
			return adminErr(err)
		}
		return proto.Message{Type: "ok"}
	default:
		return proto.Message{Type: "error", Error: "неизвестная команда"}
	}
}

func adminErr(err error) proto.Message {
	return proto.Message{Type: "error", Error: err.Error()}
}

func toAdminNets(views []admin.NetworkView) []proto.AdminNet {
	out := make([]proto.AdminNet, 0, len(views))
	for _, view := range views {
		item := proto.AdminNet{
			Name:    view.Name,
			Online:  view.Online,
			Peers:   make([]proto.AdminPeer, 0, len(view.Peers)),
			Blocked: make([]proto.AdminPeer, 0, len(view.Blocked)),
			Configs: make([]proto.AdminCfg, 0, len(view.Configs)),
		}
		for _, pn := range view.Panels {
			item.Panels = append(item.Panels, proto.AdminPanel(pn))
		}
		for _, peer := range view.Peers {
			item.Peers = append(item.Peers, proto.AdminPeer{
				IP:       peer.IP,
				Hostname: peer.Hostname,
				Endpoint: peer.Endpoint,
				Joined:   peer.Joined,
				Path:     peer.Path,
				Config:   peer.Config,
			})
		}
		for _, peer := range view.Blocked {
			item.Blocked = append(item.Blocked, proto.AdminPeer{IP: peer.IP, Hostname: peer.Hostname})
		}
		for _, cfg := range view.Configs {
			item.Configs = append(item.Configs, proto.AdminCfg{
				ID:       cfg.ID,
				Label:    cfg.Label,
				Created:  cfg.Created,
				LastSeen: cfg.LastSeen,
				LastHost: cfg.LastHost,
			})
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	if !s.allowJoin(host) {
		return
	}
	ch, err := serverHandshake(conn, s.identity)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(helloWait))
	msg, err := ch.Recv()
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	if msg.Type == "admin" {
		s.handleAdmin(ch, msg.Password)
		return
	}
	if msg.Type == "panel" {
		s.handlePanel(ch, host, msg)
		return
	}
	if msg.Type != "join" {
		_ = ch.Send(proto.Message{Type: "error", Error: "ожидался вход в сеть"})
		return
	}
	pub, err := base64.StdEncoding.DecodeString(msg.PubKey)
	if err != nil || len(pub) != 32 {
		_ = ch.Send(proto.Message{Type: "error", Error: "неверный ключ клиента"})
		return
	}
	if s.store.IsBlocked(msg.Network, msg.Hostname) {
		_ = ch.Send(proto.Message{Type: "error", Error: "доступ закрыт администратором"})
		return
	}
	if !s.roomFor(msg.Network, msg.Hostname) {
		_ = ch.Send(proto.Message{Type: "error", Error: "в сети уже максимум участников"})
		return
	}
	ip, err := s.store.Join(msg.Network, msg.Password, msg.Hostname)
	if err != nil {
		_ = ch.Send(proto.Message{Type: "error", Error: err.Error()})
		return
	}
	configID := strings.TrimSpace(msg.ConfigID)
	configLabel := ""
	if configID != "" {
		if label, ok := s.store.UseConfig(msg.Network, configID, msg.Hostname); ok {
			configLabel = label
		} else {
			configID = ""
		}
	}
	sessionKey, err := crypto.Random(crypto.KeySize)
	if err != nil {
		return
	}
	p := &peer{
		ip:          ip,
		hostname:    msg.Hostname,
		network:     msg.Network,
		configID:    configID,
		configLabel: configLabel,
		pub:         pub,
		sessionKey:  sessionKey,
		joined:      time.Now(),
		conn:        conn,
		out:         make(chan []byte, 32),
		done:        make(chan struct{}),
	}
	old, ok := s.addPeer(p)
	if !ok {
		_ = ch.Send(proto.Message{Type: "error", Error: "в сети уже максимум участников"})
		return
	}
	if old != nil {
		old.stop()
	}
	defer func() {
		if s.removePeer(p) {
			log.Printf("%s вышел из %s", p.hostname, p.network)
			s.notify(p.network)
		}
	}()
	go p.writeLoop(ch)
	welcome, _ := json.Marshal(proto.Message{
		Type:       "welcome",
		IP:         ip.String(),
		Mask:       vpnPrefix,
		MTU:        vpnMTU,
		SessionKey: base64.StdEncoding.EncodeToString(sessionKey),
	})
	select {
	case <-p.done:
		return
	case p.out <- welcome:
	}
	log.Printf("%s вошёл в %s как %s", p.hostname, p.network, ip)
	s.notify(p.network)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		in, err := ch.Recv()
		if err != nil {
			return
		}
		if in.Type == "ping" {
			pong, _ := json.Marshal(proto.Message{Type: "pong"})
			select {
			case <-p.done:
				return
			case p.out <- pong:
			default:
			}
		}
	}
}

func (s *Server) roomFor(network, hostname string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	peers := s.byNet[network]
	if peers == nil {
		return true
	}
	if _, exists := s.byName[network+"\x00"+hostname]; exists {
		return true
	}
	return len(peers) < maxPeers
}

func (s *Server) allowJoin(ip string) bool {
	if ip == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.lastSweep) > joinWindow {
		s.lastSweep = now
		for addr, list := range s.attempts {
			if len(list) == 0 || now.Sub(list[len(list)-1]) >= joinWindow {
				delete(s.attempts, addr)
			}
		}
	}
	recent := s.attempts[ip][:0]
	for _, ts := range s.attempts[ip] {
		if now.Sub(ts) < joinWindow {
			recent = append(recent, ts)
		}
	}
	if len(recent) >= maxJoins {
		s.attempts[ip] = recent
		return false
	}
	s.attempts[ip] = append(recent, now)
	return true
}

func (s *Server) addPeer(p *peer) (*peer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byNet[p.network] == nil {
		s.byNet[p.network] = map[string]*peer{}
	}
	old := s.byName[p.mapKey()]
	replacing := old != nil && old != p
	if !replacing && len(s.byNet[p.network]) >= maxPeers {
		return nil, false
	}
	if replacing {
		if s.byIP[old.ip.String()] == old {
			delete(s.byIP, old.ip.String())
		}
		if m := s.byNet[old.network]; m[old.ip.String()] == old {
			delete(m, old.ip.String())
		}
	}
	s.byName[p.mapKey()] = p
	s.byIP[p.ip.String()] = p
	s.byNet[p.network][p.ip.String()] = p
	if replacing {
		return old, true
	}
	return nil, true
}

func (s *Server) removePeer(p *peer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byIP[p.ip.String()] != p {
		return false
	}
	delete(s.byIP, p.ip.String())
	if s.byName[p.mapKey()] == p {
		delete(s.byName, p.mapKey())
	}
	if m := s.byNet[p.network]; m != nil {
		delete(m, p.ip.String())
	}
	p.stop()
	return true
}

func (s *Server) notify(network string) {
	s.mu.Lock()
	peers := make([]*peer, 0)
	for _, p := range s.byNet[network] {
		peers = append(peers, p)
	}
	infos := make([]proto.PeerInfo, len(peers))
	for i, p := range peers {
		infos[i] = proto.PeerInfo{
			IP:       p.ip.String(),
			Hostname: p.hostname,
			PubKey:   base64.StdEncoding.EncodeToString(p.pub),
		}
		if p.endpoint != nil {
			infos[i].Endpoint = p.endpoint.String()
		}
	}
	s.mu.Unlock()

	for _, target := range peers {
		mine := make([]proto.PeerInfo, 0)
		for _, info := range infos {
			if info.IP == target.ip.String() {
				continue
			}
			mine = append(mine, info)
		}
		msg, err := json.Marshal(proto.Message{Type: "peers", Peers: mine})
		if err != nil {
			continue
		}
		select {
		case <-target.done:
		case target.out <- msg:
		default:
		}
	}
}

func (s *Server) udpLoop() {
	buf := make([]byte, 2048)
	for {
		n, addr, err := s.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n < 12 || n > 2000 {
			continue
		}
		raw := append([]byte(nil), buf[:n]...)
		d, err := proto.ParseDatagram(raw)
		if err != nil || len(d.MAC) != 16 {
			continue
		}
		if d.Type != proto.TypeRelay && d.Type != proto.TypeKeepalive {
			continue
		}
		s.mu.Lock()
		src := s.byIP[d.Src.String()]
		if src == nil {
			s.mu.Unlock()
			continue
		}
		key := append([]byte(nil), src.sessionKey...)
		s.mu.Unlock()
		if !crypto.VerifyMAC(key, d.Body, d.MAC) {
			continue
		}

		var forward *net.UDPAddr
		var changed bool
		var network string
		s.mu.Lock()
		src = s.byIP[d.Src.String()]
		if src == nil || string(src.sessionKey) != string(key) {
			s.mu.Unlock()
			continue
		}
		src.lastUDP = time.Now()

		if seq, ok := keepaliveSeq(d); ok && seq > src.keepSeq {
			src.keepSeq = seq
			if src.endpoint == nil || !sameUDP(src.endpoint, addr) {
				src.endpoint = cloneUDP(addr)
				changed = true
				network = src.network
			}
		}
		if d.Type == proto.TypeRelay {
			dst := s.byIP[d.Dst.String()]
			if dst != nil && dst != src && dst.network == src.network && dst.endpoint != nil {
				forward = cloneUDP(dst.endpoint)
			}
		}
		s.mu.Unlock()
		if forward != nil {
			_, _ = s.udp.WriteToUDP(raw, forward)
		}
		if changed {
			s.notify(network)
		}
	}
}

func keepaliveSeq(d proto.Datagram) (uint64, bool) {
	if d.Type != proto.TypeKeepalive || len(d.Payload) != 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(d.Payload), true
}

func sameUDP(a, b *net.UDPAddr) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Port == b.Port && a.IP.Equal(b.IP)
}

func cloneUDP(a *net.UDPAddr) *net.UDPAddr {
	if a == nil {
		return nil
	}
	ip := append(net.IP(nil), a.IP...)
	return &net.UDPAddr{IP: ip, Port: a.Port, Zone: a.Zone}
}

func serverHandshake(conn net.Conn, identity ed25519.PrivateKey) (*proto.Channel, error) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, len(proto.Magic)+33)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	clientPub, err := proto.ParseClientHello(buf)
	if err != nil {
		return nil, err
	}
	priv, pub, err := crypto.GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	salt, err := crypto.Random(16)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(proto.ServerHello(identity, clientPub, pub, salt)); err != nil {
		return nil, err
	}
	toServer, toClient, err := proto.DeriveControlKeys(priv, clientPub, salt)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return proto.NewChannel(conn, toClient, toServer)
}
