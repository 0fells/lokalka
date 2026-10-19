package vpn

import (
	"context"
	"errors"
	"testing"
	"time"

	"lokalka/internal/proto"
)

func startPanelClient(t *testing.T, srv *Server, ctx context.Context, network, host string) *Client {
	t.Helper()
	c := New(Config{ServerAddr: srv.Addr(), Network: network, Password: "secret", Hostname: host, NoTUN: true})
	go c.Run(ctx)
	waitReady(t, c)
	return c
}

func panelServer(t *testing.T) (*Server, *AdminSession) {
	t.Helper()
	srv, st := testServer(t)
	if err := st.SetAdminPassword("admin-secret"); err != nil {
		t.Fatal(err)
	}
	srv.SetPublic(srv.Addr())
	root, err := DialAdmin(srv.Addr(), "admin-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return srv, root
}

func TestPanelManagesItsNetwork(t *testing.T) {
	srv, root := panelServer(t)
	pf, err := root.CreatePanel("home", "Вася")
	if err != nil {
		t.Fatal(err)
	}
	if pf.ID == "" || pf.Secret == "" || pf.Server != srv.Addr() || pf.Network != "home" || pf.Label != "Вася" {
		t.Fatalf("данные панели: %+v", pf)
	}
	if _, err := root.CreatePanel("home", "вася"); err == nil {
		t.Fatal("имя панели должно быть уникальным")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	alice := startPanelClient(t, srv, ctx, "home", "alice")
	bob := startPanelClient(t, srv, ctx, "home", "bob")
	carol := startPanelClient(t, srv, ctx, "other", "carol")

	panel := NewPanelSession(pf.Server, pf.Network, pf.ID, pf.Secret, nil)
	defer panel.Close()
	nets, err := panel.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 1 || nets[0].Name != "home" || len(nets[0].Peers) != 2 || len(nets[0].Panels) != 0 {
		t.Fatalf("панель видит не то: %+v", nets)
	}
	for _, p := range nets[0].Peers {
		if p.Endpoint != "" {
			t.Fatalf("панель видит настоящий адрес: %+v", p)
		}
	}

	var real bool
	for deadline := time.Now().Add(6 * time.Second); !real && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		for _, n := range root.mustList(t) {
			for _, p := range n.Peers {
				real = real || p.Endpoint != ""
			}
		}
	}
	if !real {
		t.Fatal("главная админка потеряла адреса участников")
	}

	if err := panel.CreateConfig("home", "для друга", ""); err != nil {
		t.Fatal(err)
	}
	nets, _ = panel.List()
	var id string
	for _, cfg := range nets[0].Configs {
		if cfg.Label == "для друга" {
			id = cfg.ID
		}
	}
	if id == "" {
		t.Fatalf("файл не появился: %+v", nets[0].Configs)
	}
	label, server, network, password, err := panel.Config("home", id)
	if err != nil || label != "для друга" || server != srv.Addr() || network != "home" || password != "secret" {
		t.Fatalf("файл: %s %s %s %s %v", label, server, network, password, err)
	}
	if err := panel.RenameConfig("home", id, "другу"); err != nil {
		t.Fatal(err)
	}
	if err := panel.DeleteConfig("home", id); err != nil {
		t.Fatal(err)
	}

	if err := panel.Disconnect("home", alice.VirtualIP()); err != nil {
		t.Fatal(err)
	}
	nets, _ = panel.List()
	if len(nets[0].Blocked) != 1 || nets[0].Blocked[0].Hostname != "alice" {
		t.Fatalf("отключённые: %+v", nets[0].Blocked)
	}
	if err := panel.Allow("home", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := panel.DisconnectAll("home"); err != nil {
		t.Fatal(err)
	}
	_ = bob

	if err := panel.Disconnect("other", carol.VirtualIP()); err == nil {
		t.Fatal("панель отключила участника чужой сети")
	}
	if err := panel.CreateConfig("other", "x", ""); err != nil {
		t.Fatal(err)
	}
	if got := root.mustFiles(t, "other"); got != 1 {
		t.Fatalf("панель создала файл в чужой сети: %d", got)
	}

	if err := panel.CreateNetwork("x", "pass1234"); err == nil {
		t.Fatal("панель создала сеть")
	}
	if err := panel.DeleteNetwork("home"); err == nil {
		t.Fatal("панель удалила сеть")
	}
	if err := panel.SetPassword("home", "newpass"); err == nil {
		t.Fatal("панель сменила пароль сети")
	}
	if _, err := panel.NetworkPassword("home"); err == nil {
		t.Fatal("панель получила пароль сети")
	}
	if _, err := panel.CreatePanel("home", "Другая"); err == nil {
		t.Fatal("панель создала панель")
	}

	all, err := root.List()
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, n := range all {
		for _, p := range n.Panels {
			if p.ID == pf.ID && p.Label == "Вася" && p.Online && p.LastSeen > 0 && !p.Disabled {
				seen = true
			}
		}
	}
	if !seen {
		t.Fatalf("главная админка не видит панель: %+v", all)
	}
}

func (a *AdminSession) mustList(t *testing.T) []proto.AdminNet {
	t.Helper()
	nets, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	return nets
}

func (a *AdminSession) mustFiles(t *testing.T, network string) int {
	t.Helper()
	nets, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nets {
		if n.Name == network {
			return len(n.Configs)
		}
	}
	return -1
}

func TestPanelAccessControl(t *testing.T) {
	srv, root := panelServer(t)
	pf, err := root.CreatePanel("home", "Вася")
	if err != nil {
		t.Fatal(err)
	}
	bad := NewPanelSession(pf.Server, pf.Network, pf.ID, "wrong-secret", nil)
	defer bad.Close()
	if _, err := bad.List(); !errors.Is(err, ErrDenied) {
		t.Fatalf("чужой ключ: %v", err)
	}
	live := NewPanelSession(pf.Server, pf.Network, pf.ID, pf.Secret, nil)
	defer live.Close()
	if _, err := live.List(); err != nil {
		t.Fatal(err)
	}

	if err := root.SetPanelDisabled("home", pf.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := live.List(); !errors.Is(err, ErrDenied) {
		t.Fatalf("приостановленная панель работает: %v", err)
	}
	fresh := NewPanelSession(pf.Server, pf.Network, pf.ID, pf.Secret, nil)
	defer fresh.Close()
	if _, err := fresh.List(); !errors.Is(err, ErrDenied) {
		t.Fatalf("вход в приостановленную панель: %v", err)
	}
	if err := root.SetPanelDisabled("home", pf.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.List(); err != nil {
		t.Fatalf("после включения: %v", err)
	}

	next, err := root.ResetPanel("home", pf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Secret == pf.Secret || next.ID != pf.ID {
		t.Fatalf("ключ не сменился: %+v", next)
	}
	if _, err := fresh.List(); !errors.Is(err, ErrDenied) {
		t.Fatalf("открытая сессия пережила замену ключа: %v", err)
	}
	old := NewPanelSession(pf.Server, pf.Network, pf.ID, pf.Secret, nil)
	defer old.Close()
	if _, err := old.List(); !errors.Is(err, ErrDenied) {
		t.Fatalf("старый ключ работает: %v", err)
	}
	renewed := NewPanelSession(next.Server, next.Network, next.ID, next.Secret, nil)
	defer renewed.Close()
	if _, err := renewed.List(); err != nil {
		t.Fatalf("новый ключ: %v", err)
	}

	other := NewPanelSession(next.Server, "other", next.ID, next.Secret, nil)
	defer other.Close()
	if _, err := other.List(); !errors.Is(err, ErrDenied) {
		t.Fatalf("панель вошла в чужую сеть: %v", err)
	}

	if err := root.DeletePanel("home", pf.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := renewed.List(); !errors.Is(err, ErrDenied) {
		t.Fatalf("удалённая панель работает: %v", err)
	}
	_ = srv
}
