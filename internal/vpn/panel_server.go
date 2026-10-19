package vpn

import (
	"errors"
	"strings"

	"lokalka/internal/admin"
	"lokalka/internal/proto"
	"lokalka/internal/store"
)

type PanelGrant struct {
	ID      string
	Secret  string
	Label   string
	Network string
	Server  string
}

func (g PanelGrant) message() proto.Message {
	return proto.Message{
		Type:     "admin-panel",
		AdminID:  g.ID,
		Password: g.Secret,
		Label:    g.Label,
		Network:  g.Network,
		Addr:     g.Server,
	}
}

func (s *Server) panelEndpoint() (string, error) {
	endpoint, err := admin.NormalizeEndpoint(s.public)
	if err != nil {
		return "", errors.New("у сервера не задан публичный адрес для панели")
	}
	return endpoint, nil
}

func (s *Server) panelLabel(network, id string) string {
	for _, pn := range s.store.AllPanels()[network] {
		if pn.ID == id {
			return pn.Label
		}
	}
	return ""
}

func (s *Server) CreatePanel(network, label string) (PanelGrant, error) {
	endpoint, err := s.panelEndpoint()
	if err != nil {
		return PanelGrant{}, err
	}
	id, secret, err := s.store.AddPanel(network, label)
	if err != nil {
		return PanelGrant{}, err
	}
	return PanelGrant{ID: id, Secret: secret, Label: s.panelLabel(network, id), Network: network, Server: endpoint}, nil
}

func (s *Server) ResetPanel(network, id string) (PanelGrant, error) {
	endpoint, err := s.panelEndpoint()
	if err != nil {
		return PanelGrant{}, err
	}
	secret, err := s.store.ResetPanel(network, id)
	if err != nil {
		return PanelGrant{}, err
	}
	return PanelGrant{ID: id, Secret: secret, Label: s.panelLabel(network, id), Network: network, Server: endpoint}, nil
}

func (s *Server) SetPanelDisabled(network, id string, disabled bool) error {
	return s.store.SetPanelDisabled(network, id, disabled)
}

func (s *Server) DeletePanel(network, id string) error {
	return s.store.DeletePanel(network, id)
}

func (s *Server) handlePanel(ch *proto.Channel, host string, msg proto.Message) {
	acc, err := s.store.PanelLogin(strings.TrimSpace(msg.Network), strings.TrimSpace(msg.AdminID), msg.Password, host)
	if err != nil {
		_ = ch.Send(proto.Message{Type: "error", Error: err.Error()})
		return
	}
	s.mu.Lock()
	s.panelOn[acc.ID]++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.panelOn[acc.ID]--; s.panelOn[acc.ID] <= 0 {
			delete(s.panelOn, acc.ID)
		}
		s.mu.Unlock()
	}()
	if err := ch.Send(proto.Message{Type: "admin-ok", Label: acc.Label}); err != nil {
		return
	}
	for {
		req, err := ch.Recv()
		if err != nil {
			return
		}
		cur, err := s.store.PanelCurrent(acc)
		if err != nil {
			_ = ch.Send(proto.Message{Type: "error", Error: err.Error()})
			return
		}
		if err := ch.Send(s.panelCall(cur, req)); err != nil {
			return
		}
	}
}

func (s *Server) panelCall(acc store.PanelAccess, msg proto.Message) proto.Message {
	switch msg.Type {
	case "admin-list":
		for _, n := range toAdminNets(s.Snapshot()) {
			if n.Name == acc.Network {
				n.Panels = nil

				for i := range n.Peers {
					n.Peers[i].Endpoint = ""
				}
				return proto.Message{Type: "admin-nets", Networks: []proto.AdminNet{n}}
			}
		}
		return proto.Message{Type: "error", Error: "сеть не найдена"}
	case "admin-kick", "admin-kick-all", "admin-allow",
		"admin-create-config", "admin-rename-config", "admin-delete-config", "admin-file":
		msg.Network = acc.Network
		msg.Password = ""
		resp := s.adminCall(msg)
		if resp.Type == "error" && strings.Contains(resp.Error, "укажите пароль сети") {
			resp.Error = "сеть не помнит свой пароль: попросите администратора создать в ней один файл"
		}
		return resp
	default:
		return proto.Message{Type: "error", Error: "для панели команда недоступна"}
	}
}
