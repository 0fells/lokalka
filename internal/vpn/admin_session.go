package vpn

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"lokalka/internal/proto"
	"lokalka/internal/store"
)

var errNoLink = errors.New("нет связи с сервером")

var ErrDenied = errors.New("сервер не пустил")

type deniedError struct {
	msg   string
	cause error
}

func (e *deniedError) Error() string { return e.msg }
func (e *deniedError) Is(target error) bool {
	return target == ErrDenied || (e.cause != nil && target == e.cause)
}

type AdminSession struct {
	mu       sync.Mutex
	conn     net.Conn
	ch       *proto.Channel
	addr     string
	password string

	pin []byte

	panelNet string
	panelID  string
}

func DialAdmin(addr, password string, pin []byte) (*AdminSession, error) {
	a := &AdminSession{addr: addr, password: password, pin: pin}
	if err := a.redial(); err != nil {
		return nil, err
	}
	return a, nil
}

func NewPanelSession(addr, network, id, secret string, pin []byte) *AdminSession {
	return &AdminSession{addr: addr, password: secret, pin: pin, panelNet: network, panelID: id}
}

const DefaultPort = "51820"

func ServerAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", errors.New("введите адрес сервера")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = strings.Trim(addr, "[]"), DefaultPort
	}
	if host == "" || strings.ContainsAny(host, " /\\") {
		return "", errors.New("адрес сервера: укажите IP или имя, например 203.0.113.10:51820")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", errors.New("адрес сервера: неверный порт")
	}
	return net.JoinHostPort(host, port), nil
}

func ProbeServerKey(addr string) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, errors.New("сервер недоступен")
	}
	defer conn.Close()
	_, key, err := clientHandshake(conn, nil)
	if err != nil {
		return nil, errors.New("сервер не ответил")
	}
	return key, nil
}

func (a *AdminSession) ServerKey() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]byte(nil), a.pin...)
}

func dialAdminConn(addr, password, panelNet, panelID string, pin []byte) (net.Conn, *proto.Channel, []byte, error) {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, nil, nil, errors.New("сервер недоступен")
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	ch, serverKey, err := clientHandshake(conn, pin)
	if err != nil {
		conn.Close()
		if errors.Is(err, ErrServerKey) {
			return nil, nil, nil, &deniedError{msg: ErrServerKey.Error(), cause: ErrServerKey}
		}
		return nil, nil, nil, errors.New("сервер не ответил")
	}
	_ = conn.SetDeadline(time.Now().Add(12 * time.Second))
	hello := proto.Message{Type: "admin", Password: password}
	if panelID != "" {
		hello = proto.Message{Type: "panel", Network: panelNet, AdminID: panelID, Password: password}
	}
	if err := ch.Send(hello); err != nil {
		conn.Close()
		return nil, nil, nil, errNoLink
	}
	resp, err := ch.Recv()
	_ = conn.SetDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return nil, nil, nil, errNoLink
	}
	if resp.Type != "admin-ok" {
		conn.Close()
		if resp.Error != "" {
			return nil, nil, nil, &deniedError{msg: resp.Error}
		}
		return nil, nil, nil, &deniedError{msg: "сервер не пустил в админку"}
	}
	return conn, ch, serverKey, nil
}

func (a *AdminSession) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conn == nil {
		return nil
	}
	err := a.conn.Close()
	a.conn = nil
	return err
}

func (a *AdminSession) List() ([]proto.AdminNet, error) {
	resp, err := a.call(proto.Message{Type: "admin-list"})
	if err != nil {
		return nil, err
	}
	if resp.Networks == nil {
		return []proto.AdminNet{}, nil
	}
	return resp.Networks, nil
}

func (a *AdminSession) CreateNetwork(name, password string) error {
	_, err := a.call(proto.Message{Type: "admin-create-net", Network: name, Password: password})
	return err
}

func (a *AdminSession) DeleteNetwork(name string) error {
	_, err := a.call(proto.Message{Type: "admin-delete-net", Network: name})
	return err
}

func (a *AdminSession) SetPassword(name, password string) error {
	_, err := a.call(proto.Message{Type: "admin-set-password", Network: name, Password: password})
	return err
}

func (a *AdminSession) Disconnect(network, ip string) error {
	_, err := a.call(proto.Message{Type: "admin-kick", Network: network, IP: ip})
	return err
}

func (a *AdminSession) Allow(network, hostname string) error {
	_, err := a.call(proto.Message{Type: "admin-allow", Network: network, Hostname: hostname})
	return err
}

func (a *AdminSession) DisconnectAll(network string) error {
	_, err := a.call(proto.Message{Type: "admin-kick-all", Network: network})
	return err
}

func (a *AdminSession) NetworkPassword(network string) (string, error) {
	resp, err := a.call(proto.Message{Type: "admin-net-password", Network: network})
	if err != nil {
		return "", err
	}
	if resp.Password == "" {
		return "", errors.New("сервер не прислал пароль сети")
	}
	return resp.Password, nil
}

func (a *AdminSession) CreateConfig(network, label, password string) error {
	_, err := a.call(proto.Message{Type: "admin-create-config", Network: network, Label: label, Password: password})
	return err
}

func (a *AdminSession) RenameConfig(network, id, label string) error {
	_, err := a.call(proto.Message{Type: "admin-rename-config", Network: network, ConfigID: id, Label: label})
	return err
}

func (a *AdminSession) DeleteConfig(network, id string) error {
	_, err := a.call(proto.Message{Type: "admin-delete-config", Network: network, ConfigID: id})
	return err
}

func (a *AdminSession) Config(network, id string) (label, server, netName, password string, err error) {
	resp, err := a.call(proto.Message{Type: "admin-file", Network: network, ConfigID: id})
	if err != nil {
		return "", "", "", "", err
	}
	if resp.Addr == "" || resp.Network == "" || resp.Password == "" {
		return "", "", "", "", errors.New("сервер прислал пустой файл подключения")
	}
	return resp.Label, resp.Addr, resp.Network, resp.Password, nil
}

type PanelFile struct {
	ID      string
	Secret  string
	Label   string
	Network string
	Server  string
}

func panelFile(resp proto.Message) (PanelFile, error) {
	f := PanelFile{ID: resp.AdminID, Secret: resp.Password, Label: resp.Label, Network: resp.Network, Server: resp.Addr}
	if f.ID == "" || f.Secret == "" || f.Network == "" || f.Server == "" {
		return PanelFile{}, errors.New("сервер прислал неполные данные панели")
	}
	return f, nil
}

func (a *AdminSession) CreatePanel(network, label string) (PanelFile, error) {
	resp, err := a.call(proto.Message{Type: "admin-create-panel", Network: network, Label: label})
	if err != nil {
		return PanelFile{}, err
	}
	return panelFile(resp)
}

func (a *AdminSession) ResetPanel(network, id string) (PanelFile, error) {
	resp, err := a.call(proto.Message{Type: "admin-reset-panel", Network: network, AdminID: id})
	if err != nil {
		return PanelFile{}, err
	}
	return panelFile(resp)
}

func (a *AdminSession) SetPanelDisabled(network, id string, disabled bool) error {
	typ := "admin-enable-panel"
	if disabled {
		typ = "admin-disable-panel"
	}
	_, err := a.call(proto.Message{Type: typ, Network: network, AdminID: id})
	return err
}

func (a *AdminSession) DeletePanel(network, id string) error {
	_, err := a.call(proto.Message{Type: "admin-delete-panel", Network: network, AdminID: id})
	return err
}

func (a *AdminSession) call(req proto.Message) (proto.Message, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	resp, err := a.round(req)
	if err == nil || !errors.Is(err, errNoLink) || a.addr == "" {
		return resp, err
	}
	if rerr := a.redial(); rerr != nil {
		if errors.Is(rerr, ErrDenied) {
			return proto.Message{}, rerr
		}
		return proto.Message{}, err
	}
	if req.Type == "admin-list" {
		return a.round(req)
	}
	return proto.Message{}, errors.New("связь оборвалась, повторите")
}

func (a *AdminSession) round(req proto.Message) (proto.Message, error) {
	if a.conn == nil {
		return proto.Message{}, errNoLink
	}
	_ = a.conn.SetDeadline(time.Now().Add(12 * time.Second))
	defer a.conn.SetDeadline(time.Time{})
	if err := a.ch.Send(req); err != nil {
		return proto.Message{}, errNoLink
	}
	resp, err := a.ch.Recv()
	if err != nil {
		return proto.Message{}, errNoLink
	}
	if resp.Type == "error" {
		if resp.Error == "" {
			return resp, errors.New("ошибка админки")
		}
		if a.panelID != "" && (resp.Error == store.ErrPanelBad.Error() || resp.Error == store.ErrPanelDisabled.Error()) {
			return resp, &deniedError{msg: resp.Error}
		}
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

func (a *AdminSession) redial() error {
	if a.conn != nil {
		_ = a.conn.Close()
		a.conn = nil
	}
	conn, ch, serverKey, err := dialAdminConn(a.addr, a.password, a.panelNet, a.panelID, a.pin)
	if err != nil {
		return err
	}
	a.conn = conn
	a.ch = ch
	a.pin = serverKey
	return nil
}
