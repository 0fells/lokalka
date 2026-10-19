package proto

import (
	"crypto/cipher"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"

	"lokalka/internal/crypto"
)

const (
	Magic   = "Lokalka!"
	Version = 1

	ServerHelloSize = 48 + ed25519.PublicKeySize + ed25519.SignatureSize

	MaxFrame = 1 << 20

	TypeRelay     = 1
	TypeKeepalive = 2
	TypeDirect    = 3
	TypePunch     = 4
	TypePunchAck  = 5

	controlInfo = "lokalka-control"
	helloInfo   = "lokalka-hello"
)

type Message struct {
	Type       string     `json:"type"`
	Network    string     `json:"network,omitempty"`
	Password   string     `json:"password,omitempty"`
	Hostname   string     `json:"hostname,omitempty"`
	PubKey     string     `json:"pubkey,omitempty"`
	IP         string     `json:"ip,omitempty"`
	Mask       int        `json:"mask,omitempty"`
	MTU        int        `json:"mtu,omitempty"`
	SessionKey string     `json:"session_key,omitempty"`
	Error      string     `json:"error,omitempty"`
	Peers      []PeerInfo `json:"peers,omitempty"`
	Label      string     `json:"label,omitempty"`
	ConfigID   string     `json:"config_id,omitempty"`
	Addr       string     `json:"addr,omitempty"`
	Networks   []AdminNet `json:"networks,omitempty"`
	AdminID    string     `json:"admin_id,omitempty"`
}

type AdminPanel struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Created  int64  `json:"created"`
	LastSeen int64  `json:"last_seen,omitempty"`
	LastHost string `json:"last_host,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Online   bool   `json:"online,omitempty"`
}

type AdminNet struct {
	Name    string       `json:"name"`
	Online  int          `json:"online"`
	Peers   []AdminPeer  `json:"peers"`
	Blocked []AdminPeer  `json:"blocked,omitempty"`
	Configs []AdminCfg   `json:"configs"`
	Panels  []AdminPanel `json:"panels,omitempty"`
}

type AdminPeer struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	Endpoint string `json:"endpoint,omitempty"`
	Joined   int64  `json:"joined,omitempty"`
	Path     string `json:"path,omitempty"`
	Config   string `json:"config,omitempty"`
}

type AdminCfg struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Created  int64  `json:"created"`
	LastSeen int64  `json:"last_seen,omitempty"`
	LastHost string `json:"last_host,omitempty"`
}

type PeerInfo struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	Endpoint string `json:"endpoint,omitempty"`
	PubKey   string `json:"pubkey"`
}

func ClientHello(pub []byte) []byte {
	b := make([]byte, 0, len(Magic)+33)
	b = append(b, Magic...)
	b = append(b, Version)
	b = append(b, pub...)
	return b
}

func ParseClientHello(b []byte) ([]byte, error) {
	if len(b) != len(Magic)+33 || string(b[:len(Magic)]) != Magic || b[len(Magic)] != Version {
		return nil, errors.New("неверное приветствие клиента")
	}
	pub := make([]byte, 32)
	copy(pub, b[len(Magic)+1:])
	return pub, nil
}

func helloTranscript(clientPub, serverPub, salt []byte) []byte {
	b := make([]byte, 0, len(helloInfo)+len(clientPub)+len(serverPub)+len(salt))
	b = append(b, helloInfo...)
	b = append(b, clientPub...)
	b = append(b, serverPub...)
	return append(b, salt...)
}

func ServerHello(id ed25519.PrivateKey, clientPub, pub, salt []byte) []byte {
	b := make([]byte, 0, ServerHelloSize)
	b = append(b, pub...)
	b = append(b, salt...)
	b = append(b, id.Public().(ed25519.PublicKey)...)
	return append(b, ed25519.Sign(id, helloTranscript(clientPub, pub, salt))...)
}

func ParseServerHello(b, clientPub []byte) (pub, salt, serverKey []byte, err error) {
	if len(b) != ServerHelloSize {
		return nil, nil, nil, errors.New("неверное приветствие сервера")
	}
	pub = append([]byte(nil), b[:32]...)
	salt = append([]byte(nil), b[32:48]...)
	serverKey = append([]byte(nil), b[48:48+ed25519.PublicKeySize]...)
	sig := b[48+ed25519.PublicKeySize:]
	if !ed25519.Verify(serverKey, helloTranscript(clientPub, pub, salt), sig) {
		return nil, nil, nil, errors.New("сервер не подтвердил рукопожатие подписью")
	}
	return pub, salt, serverKey, nil
}

func DeriveControlKeys(priv, peerPub, salt []byte) (toServer, toClient []byte, err error) {
	toServer, err = crypto.DeriveKey(priv, peerPub, salt, []byte(controlInfo+"|to-server"))
	if err != nil {
		return nil, nil, err
	}
	toClient, err = crypto.DeriveKey(priv, peerPub, salt, []byte(controlInfo+"|to-client"))
	if err != nil {
		return nil, nil, err
	}
	return toServer, toClient, nil
}

type Channel struct {
	rw    io.ReadWriter
	send  cipher.AEAD
	recv  cipher.AEAD
	wmu   sync.Mutex
	sendN uint64
	recvN uint64
}

func NewChannel(rw io.ReadWriter, sendKey, recvKey []byte) (*Channel, error) {
	send, err := crypto.NewAEAD(sendKey)
	if err != nil {
		return nil, err
	}
	recv, err := crypto.NewAEAD(recvKey)
	if err != nil {
		return nil, err
	}
	return &Channel{rw: rw, send: send, recv: recv}, nil
}

func counterNonce(n uint64) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], n)
	return nonce
}

func (c *Channel) SendRaw(plain []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if len(plain)+c.send.Overhead() > MaxFrame {
		return errors.New("слишком большой кадр")
	}
	frame := make([]byte, 4, 4+len(plain)+c.send.Overhead())
	frame = c.send.Seal(frame, counterNonce(c.sendN), plain, nil)
	binary.BigEndian.PutUint32(frame, uint32(len(frame)-4))
	c.sendN++
	_, err := c.rw.Write(frame)
	return err
}

func (c *Channel) Send(msg Message) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.SendRaw(b)
}

func (c *Channel) Recv() (Message, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(c.rw, hdr[:]); err != nil {
		return Message{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if int(n) < c.recv.Overhead() || n > MaxFrame {
		return Message{}, errors.New("неверная длина кадра")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.rw, buf); err != nil {
		return Message{}, err
	}
	plain, err := c.recv.Open(buf[:0], counterNonce(c.recvN), buf, nil)
	if err != nil {
		return Message{}, errors.New("кадр не прошёл проверку")
	}
	c.recvN++
	var msg Message
	if err := json.Unmarshal(plain, &msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}

type Datagram struct {
	Type    byte
	Src     net.IP
	Dst     net.IP
	Payload []byte
	MAC     []byte
	Body    []byte
}

func (d Datagram) Marshal() ([]byte, error) {
	src, dst := d.Src.To4(), d.Dst.To4()
	if src == nil || dst == nil {
		return nil, errors.New("нужен IPv4")
	}
	if len(d.Payload) > 1600 {
		return nil, errors.New("слишком большой пакет")
	}
	body := make([]byte, 12+len(d.Payload))
	body[0] = Version
	body[1] = d.Type
	copy(body[2:6], src)
	copy(body[6:10], dst)
	binary.BigEndian.PutUint16(body[10:12], uint16(len(d.Payload)))
	copy(body[12:], d.Payload)
	if len(d.MAC) == 0 {
		return body, nil
	}
	if len(d.MAC) != 16 {
		return nil, errors.New("неверная длина MAC")
	}
	return append(body, d.MAC...), nil
}

func ParseDatagram(b []byte) (Datagram, error) {
	if len(b) < 12 || b[0] != Version {
		return Datagram{}, errors.New("неверный датаграмм")
	}
	n := int(binary.BigEndian.Uint16(b[10:12]))
	if n > 1600 || len(b) < 12+n {
		return Datagram{}, errors.New("битая длина пакета")
	}
	d := Datagram{
		Type:    b[1],
		Src:     net.IP(append([]byte(nil), b[2:6]...)),
		Dst:     net.IP(append([]byte(nil), b[6:10]...)),
		Payload: append([]byte(nil), b[12:12+n]...),
		Body:    append([]byte(nil), b[:12+n]...),
	}
	rest := b[12+n:]
	if len(rest) == 16 {
		d.MAC = append([]byte(nil), rest...)
	} else if len(rest) != 0 {
		return Datagram{}, errors.New("лишние байты в пакете")
	}
	return d, nil
}

func PairInfo(network, ipA, ipB string) []byte {
	if ipA > ipB {
		ipA, ipB = ipB, ipA
	}
	return []byte("lokalka-pair|" + network + "|" + ipA + "|" + ipB)
}

func SameSubnetBroadcast(dst net.IP, networkIP net.IP, prefix int) bool {
	dst = dst.To4()
	networkIP = networkIP.To4()
	if dst == nil || networkIP == nil {
		return false
	}
	if dst.Equal(net.IPv4bcast) {
		return true
	}
	if dst[0] >= 224 && dst[0] <= 239 {
		return true
	}
	mask := net.CIDRMask(prefix, 32)
	bcast := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		bcast[i] = networkIP[i] | ^mask[i]
	}
	return dst.Equal(bcast)
}

func NormalizeIPv4(pkt []byte, src net.IP) ([]byte, error) {
	src = src.To4()
	if src == nil || len(pkt) < 20 || pkt[0]>>4 != 4 {
		return nil, errors.New("это не IPv4")
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < 20 || len(pkt) < ihl || len(pkt) > 1280 {
		return nil, errors.New("неверная длина IPv4")
	}
	out := append([]byte(nil), pkt...)
	if net.IP(out[12:16]).Equal(src) {
		return out, nil
	}
	copy(out[12:16], src)
	fixIPv4Checksum(out[:ihl])
	fixTransportChecksum(out, ihl)
	return out, nil
}

func fixIPv4Checksum(h []byte) {
	h[10], h[11] = 0, 0
	binary.BigEndian.PutUint16(h[10:12], onesComplement(h))
}

func fixTransportChecksum(pkt []byte, ihl int) {
	proto := pkt[9]
	var off int
	switch proto {
	case 6:
		if len(pkt) < ihl+20 {
			return
		}
		off = ihl + 16
	case 17:
		if len(pkt) < ihl+8 {
			return
		}
		off = ihl + 6
		if binary.BigEndian.Uint16(pkt[off:]) == 0 {
			return
		}
	default:
		return
	}
	binary.BigEndian.PutUint16(pkt[off:], 0)
	sum := uint32(binary.BigEndian.Uint16(pkt[12:14]))
	sum += uint32(binary.BigEndian.Uint16(pkt[14:16]))
	sum += uint32(binary.BigEndian.Uint16(pkt[16:18]))
	sum += uint32(binary.BigEndian.Uint16(pkt[18:20]))
	sum += uint32(proto)
	sum += uint32(len(pkt) - ihl)
	payload := pkt[ihl:]
	for i := 0; i+1 < len(payload); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(payload[i:]))
	}
	if len(payload)%2 == 1 {
		sum += uint32(payload[len(payload)-1]) << 8
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	csum := ^uint16(sum)
	if proto == 17 && csum == 0 {
		csum = 0xffff
	}
	binary.BigEndian.PutUint16(pkt[off:], csum)
}

func onesComplement(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}
