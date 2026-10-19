package store

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sort"
	"strings"
	"time"
)

const maxPanels = 20

var (
	ErrPanelBad      = errors.New("панель не найдена или её ключ заменён")
	ErrPanelDisabled = errors.New("панель приостановлена администратором")
)

func newPanelSecret() (string, error) {
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func cleanPanelLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if !labelRe.MatchString(label) || strings.Trim(label, " ") == "" {
		return "", errors.New("имя панели: от 1 до 32 символов, буквы, цифры, пробел, _ . -")
	}
	return label, nil
}

func (s *Store) AddPanel(networkName, label string) (string, string, error) {
	label, err := cleanPanelLabel(label)
	if err != nil {
		return "", "", err
	}
	secret, err := newPanelSecret()
	if err != nil {
		return "", "", err
	}
	hash, err := hashPassword(secret)
	if err != nil {
		return "", "", err
	}
	id, err := newConfigID()
	if err != nil {
		return "", "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.data.Networks[networkName]
	if !ok {
		return "", "", errors.New("сеть не найдена")
	}
	if len(n.Panels) >= maxPanels {
		return "", "", errors.New("у сети уже максимум панелей")
	}
	for _, p := range n.Panels {
		if p != nil && strings.EqualFold(p.Label, label) {
			return "", "", errors.New("панель с таким именем уже есть")
		}
	}
	if n.Panels == nil {
		n.Panels = map[string]*panel{}
	}
	n.Panels[id] = &panel{ID: id, Label: label, SecretHash: hash, Created: time.Now().Unix()}
	if err := s.saveLocked(); err != nil {
		delete(n.Panels, id)
		return "", "", err
	}
	return id, secret, nil
}

func (s *Store) ResetPanel(networkName, id string) (string, error) {
	secret, err := newPanelSecret()
	if err != nil {
		return "", err
	}
	hash, err := hashPassword(secret)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.panelLocked(networkName, id)
	if err != nil {
		return "", err
	}
	prevHash, prevGen := p.SecretHash, p.Gen
	p.SecretHash = hash
	p.Gen++
	if err := s.saveLocked(); err != nil {
		p.SecretHash, p.Gen = prevHash, prevGen
		return "", err
	}
	return secret, nil
}

func (s *Store) SetPanelDisabled(networkName, id string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.panelLocked(networkName, id)
	if err != nil {
		return err
	}
	if p.Disabled == disabled {
		return nil
	}
	p.Disabled = disabled
	p.Gen++
	if err := s.saveLocked(); err != nil {
		p.Disabled = !disabled
		p.Gen--
		return err
	}
	return nil
}

func (s *Store) DeletePanel(networkName, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.panelLocked(networkName, id)
	if err != nil {
		return err
	}
	delete(s.data.Networks[networkName].Panels, id)
	if err := s.saveLocked(); err != nil {
		s.data.Networks[networkName].Panels[id] = p
		return err
	}
	return nil
}

func (s *Store) panelLocked(networkName, id string) (*panel, error) {
	n, ok := s.data.Networks[networkName]
	if !ok {
		return nil, errors.New("сеть не найдена")
	}
	p := n.Panels[id]
	if p == nil {
		return nil, errors.New("панель не найдена")
	}
	return p, nil
}

func (s *Store) PanelLogin(networkName, id, secret, host string) (PanelAccess, error) {
	s.mu.Lock()
	hash := dummyHash
	gen := 0
	if n := s.data.Networks[networkName]; n != nil {
		if p := n.Panels[id]; p != nil {
			hash, gen = p.SecretHash, p.Gen
		}
	}
	s.mu.Unlock()
	okSecret := checkPassword(hash, secret)

	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.data.Networks[networkName]
	var p *panel
	if n != nil {
		p = n.Panels[id]
	}
	if !okSecret || p == nil || p.Gen != gen || p.SecretHash != hash {
		return PanelAccess{}, ErrPanelBad
	}
	if p.Disabled {
		return PanelAccess{}, ErrPanelDisabled
	}
	prevSeen, prevHost := p.LastSeen, p.LastHost
	p.LastSeen, p.LastHost = time.Now().Unix(), host
	if err := s.saveLocked(); err != nil {
		p.LastSeen, p.LastHost = prevSeen, prevHost
	}
	return PanelAccess{ID: p.ID, Label: p.Label, Network: networkName, Gen: p.Gen}, nil
}

func (s *Store) PanelCurrent(a PanelAccess) (PanelAccess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.data.Networks[a.Network]
	var p *panel
	if n != nil {
		p = n.Panels[a.ID]
	}
	switch {
	case p == nil:
		return PanelAccess{}, ErrPanelBad
	case p.Disabled:
		return PanelAccess{}, ErrPanelDisabled
	case p.Gen != a.Gen:
		return PanelAccess{}, ErrPanelBad
	}
	return a, nil
}

func (s *Store) AllPanels() map[string][]PanelInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]PanelInfo, len(s.data.Networks))
	for name, n := range s.data.Networks {
		list := make([]PanelInfo, 0, len(n.Panels))
		for _, p := range n.Panels {
			if p == nil {
				continue
			}
			list = append(list, PanelInfo{
				ID: p.ID, Label: p.Label,
				Created: p.Created, LastSeen: p.LastSeen, LastHost: p.LastHost, Disabled: p.Disabled,
			})
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Label != list[j].Label {
				return list[i].Label < list[j].Label
			}
			return list[i].ID < list[j].ID
		})
		out[name] = list
	}
	return out
}
