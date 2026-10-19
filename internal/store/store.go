package store

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	nameRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	hostRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
	labelRe = regexp.MustCompile(`^[\p{L}\p{N} _.-]{1,32}$`)

	errBadLogin = errors.New("неверное имя сети или пароль")
)

const maxConfigs = 20

const pbkdf2Iters = 100_000

var dummyHash string

func init() {
	var err error
	dummyHash, err = hashPassword("lokalka-not-a-real-password")
	if err != nil {
		panic(err)
	}
}

type Store struct {
	mu   sync.Mutex
	path string
	data fileData
}

type fileData struct {
	NextOctet int    `json:"next_octet"`
	AdminHash string `json:"admin_hash,omitempty"`

	ServerKey string              `json:"server_key,omitempty"`
	Networks  map[string]*network `json:"networks"`
}

type network struct {
	Name         string `json:"name"`
	PasswordHash string `json:"password_hash"`

	Password string                   `json:"password,omitempty"`
	Octet    int                      `json:"octet"`
	Leases   map[string]string        `json:"leases"`
	Configs  map[string]*clientConfig `json:"configs,omitempty"`
	Blocked  map[string]string        `json:"blocked,omitempty"`
	Panels   map[string]*panel        `json:"panels,omitempty"`
}

type panel struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	SecretHash string `json:"secret_hash"`
	Created    int64  `json:"created"`
	LastSeen   int64  `json:"last_seen,omitempty"`
	LastHost   string `json:"last_host,omitempty"`
	Disabled   bool   `json:"disabled,omitempty"`
	Gen        int    `json:"gen,omitempty"`
}

type PanelInfo struct {
	ID       string
	Label    string
	Files    int
	Created  int64
	LastSeen int64
	LastHost string
	Disabled bool
}

type PanelAccess struct {
	ID      string
	Label   string
	Network string
	Gen     int
}

type clientConfig struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Password string `json:"password"`
	Created  int64  `json:"created"`
	LastSeen int64  `json:"last_seen,omitempty"`
	LastHost string `json:"last_host,omitempty"`
}

type ConfigInfo struct {
	ID       string
	Label    string
	Created  int64
	LastSeen int64
	LastHost string
}

type BlockedInfo struct {
	Hostname string
	IP       string
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && filepath.Dir(path) != "." {
		return nil, err
	}
	s := &Store{
		path: path,
		data: fileData{NextOctet: 1, Networks: map[string]*network{}},
	}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("повреждён файл сетей: %w", err)
		}
	}
	if s.data.Networks == nil {
		s.data.Networks = map[string]*network{}
	}
	if s.data.NextOctet < 1 {
		s.data.NextOctet = 1
	}

	if s.data.ServerKey != "" {
		if _, err := s.identityLocked(); err != nil {
			return nil, fmt.Errorf("в %s повреждён server_key: восстановите файл из копии", path)
		}
	} else {
		seed, err := randomBytes(ed25519.SeedSize)
		if err != nil {
			return nil, err
		}
		s.data.ServerKey = base64.StdEncoding.EncodeToString(seed)
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func ReadPublicKey(path string) (ed25519.PublicKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("повреждён файл сетей: %w", err)
	}
	key, err := s.identityLocked()
	if err != nil {
		return nil, errors.New("в файле ещё нет ключа сервера: запустите сервер хотя бы раз")
	}
	return key.Public().(ed25519.PublicKey), nil
}

func (s *Store) Identity() ed25519.PrivateKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, _ := s.identityLocked()
	return key
}

func (s *Store) identityLocked() (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(s.data.ServerKey)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("нет ключа сервера")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

func (s *Store) Create(name, password string) error {
	if !nameRe.MatchString(name) {
		return errors.New("имя сети: от 1 до 32 символов, латиница, цифры, _ и -")
	}
	if len(password) < 4 {
		return errors.New("пароль должен быть не короче 4 символов")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Networks[name]; ok {
		return errors.New("сеть уже существует")
	}
	octet, err := s.allocOctetLocked()
	if err != nil {
		return err
	}
	id, err := newConfigID()
	if err != nil {
		return err
	}
	s.data.Networks[name] = &network{
		Name:         name,
		PasswordHash: hash,
		Password:     password,
		Octet:        octet,
		Leases:       map[string]string{},
		Configs: map[string]*clientConfig{
			id: {ID: id, Label: name, Password: password, Created: time.Now().Unix()},
		},
	}
	return s.saveLocked()
}

func (s *Store) SetAdminPassword(password string) error {
	if len(password) < 8 {
		return errors.New("пароль админки должен быть не короче 8 символов")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.AdminHash = hash
	return s.saveLocked()
}

func (s *Store) AdminOK(password string) bool {
	s.mu.Lock()
	hash := s.data.AdminHash
	s.mu.Unlock()
	if hash == "" {
		hash = dummyHash
	}
	if !checkPassword(hash, password) {
		return false
	}
	return s.adminConfigured(hash)
}

func (s *Store) adminConfigured(hash string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.AdminHash != "" && s.data.AdminHash == hash
}

func (s *Store) SetPassword(name, password string) error {
	if len(password) < 4 {
		return errors.New("пароль должен быть не короче 4 символов")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.data.Networks[name]
	if !ok {
		return errors.New("сеть не найдена")
	}
	n.PasswordHash = hash
	n.Password = password
	for _, cfg := range n.Configs {
		if cfg != nil {
			cfg.Password = password
		}
	}
	return s.saveLocked()
}

func (s *Store) AddConfig(networkName, label, password string) (string, error) {
	return s.addConfig(networkName, label, password)
}

func (s *Store) addConfig(networkName, label, password string) (string, error) {
	label = strings.TrimSpace(label)
	if !labelRe.MatchString(label) || strings.Trim(label, " ") == "" {
		return "", errors.New("имя конфига: от 1 до 32 символов, буквы, цифры, пробел, _ . -")
	}
	if password != "" && len(password) < 4 {
		return "", errors.New("пароль должен быть не короче 4 символов")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.data.Networks[networkName]
	if !ok {
		return "", errors.New("сеть не найдена")
	}
	if password == "" {
		password = n.savedPassword()
	}
	if password == "" {
		return "", errors.New("укажите пароль сети")
	}
	if !checkPassword(n.PasswordHash, password) {
		return "", errors.New("неверный пароль сети")
	}
	n.Password = password
	if n.Configs == nil {
		n.Configs = map[string]*clientConfig{}
	}
	if len(n.Configs) >= maxConfigs {
		return "", errors.New("у сети уже максимум конфигов")
	}
	for _, cfg := range n.Configs {
		if cfg != nil && strings.EqualFold(cfg.Label, label) {
			return "", errors.New("конфиг с таким именем уже есть")
		}
	}
	id, err := newConfigID()
	if err != nil {
		return "", err
	}
	n.Configs[id] = &clientConfig{ID: id, Label: label, Password: password, Created: time.Now().Unix()}
	if err := s.saveLocked(); err != nil {
		delete(n.Configs, id)
		return "", err
	}
	return id, nil
}

func (s *Store) RenameConfig(networkName, id, label string) error {
	label = strings.TrimSpace(label)
	if !labelRe.MatchString(label) || strings.Trim(label, " ") == "" {
		return errors.New("имя конфига: от 1 до 32 символов, буквы, цифры, пробел, _ . -")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.data.Networks[networkName]
	if !ok {
		return errors.New("сеть не найдена")
	}
	cfg, ok := n.Configs[id]
	if !ok || cfg == nil {
		return errors.New("конфиг не найден")
	}
	for _, other := range n.Configs {
		if other != nil && other.ID != id && strings.EqualFold(other.Label, label) {
			return errors.New("конфиг с таким именем уже есть")
		}
	}
	prev := cfg.Label
	cfg.Label = label
	if err := s.saveLocked(); err != nil {
		cfg.Label = prev
		return err
	}
	return nil
}

func (s *Store) DeleteConfig(networkName, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.data.Networks[networkName]
	if !ok {
		return errors.New("сеть не найдена")
	}
	cfg, ok := n.Configs[id]
	if !ok || cfg == nil {
		return errors.New("конфиг не найден")
	}
	delete(n.Configs, id)
	if err := s.saveLocked(); err != nil {
		n.Configs[id] = cfg
		return err
	}
	return nil
}

func (s *Store) NetworkPassword(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.data.Networks[name]
	if !ok {
		return "", errors.New("сеть не найдена")
	}
	pass := n.savedPassword()
	if pass == "" {
		return "", errors.New("пароль сети недоступен: создайте файл для входа")
	}
	return pass, nil
}

func (s *Store) OpenConfig(networkName, id string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.data.Networks[networkName]
	if !ok {
		return "", "", errors.New("сеть не найдена")
	}
	cfg, ok := n.Configs[id]
	if !ok || cfg == nil || cfg.Password == "" {
		return "", "", errors.New("конфиг не найден")
	}
	return cfg.Label, cfg.Password, nil
}

func (s *Store) UseConfig(networkName, id, hostname string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.data.Networks[networkName]
	if !ok || id == "" {
		return "", false
	}
	cfg, ok := n.Configs[id]
	if !ok || cfg == nil || cfg.Label == "" {
		return "", false
	}
	prevSeen, prevHost := cfg.LastSeen, cfg.LastHost
	cfg.LastSeen = time.Now().Unix()
	cfg.LastHost = hostname
	if err := s.saveLocked(); err != nil {
		cfg.LastSeen, cfg.LastHost = prevSeen, prevHost
	}
	return cfg.Label, true
}

func (s *Store) Block(networkName, hostname, ip string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.data.Networks[networkName]
	if !ok {
		return errors.New("сеть не найдена")
	}
	if n.Blocked == nil {
		n.Blocked = map[string]string{}
	}
	prev, had := n.Blocked[hostname]
	n.Blocked[hostname] = ip
	if err := s.saveLocked(); err != nil {
		if had {
			n.Blocked[hostname] = prev
		} else {
			delete(n.Blocked, hostname)
		}
		return err
	}
	return nil
}

func (s *Store) Unblock(networkName, hostname string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.data.Networks[networkName]
	if !ok {
		return errors.New("сеть не найдена")
	}
	ip, ok := n.Blocked[hostname]
	if !ok {
		return errors.New("участник не в списке отключённых")
	}
	delete(n.Blocked, hostname)
	if err := s.saveLocked(); err != nil {
		n.Blocked[hostname] = ip
		return err
	}
	return nil
}
func (s *Store) IsBlocked(networkName, hostname string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.data.Networks[networkName]
	if !ok {
		return false
	}
	_, blocked := n.Blocked[hostname]
	return blocked
}

func (s *Store) ClearBlocked(networkName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.data.Networks[networkName]
	if !ok || len(n.Blocked) == 0 {
		return nil
	}
	prev := n.Blocked
	n.Blocked = nil
	if err := s.saveLocked(); err != nil {
		n.Blocked = prev
		return err
	}
	return nil
}

func (s *Store) AllBlocked() map[string][]BlockedInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]BlockedInfo, len(s.data.Networks))
	for name, n := range s.data.Networks {
		if len(n.Blocked) == 0 {
			continue
		}
		list := make([]BlockedInfo, 0, len(n.Blocked))
		for host, ip := range n.Blocked {
			list = append(list, BlockedInfo{Hostname: host, IP: ip})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Hostname < list[j].Hostname })
		out[name] = list
	}
	return out
}

func (s *Store) AllConfigs() map[string][]ConfigInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]ConfigInfo, len(s.data.Networks))
	for name, n := range s.data.Networks {
		list := make([]ConfigInfo, 0, len(n.Configs))
		for _, cfg := range n.Configs {
			if cfg == nil {
				continue
			}
			list = append(list, ConfigInfo{ID: cfg.ID, Label: cfg.Label, Created: cfg.Created, LastSeen: cfg.LastSeen, LastHost: cfg.LastHost})
		}
		sortConfigs(list)
		out[name] = list
	}
	return out
}

func (n *network) savedPassword() string {
	if n.Password != "" && checkPassword(n.PasswordHash, n.Password) {
		return n.Password
	}
	for _, cfg := range n.Configs {
		if cfg != nil && cfg.Password != "" && checkPassword(n.PasswordHash, cfg.Password) {
			return cfg.Password
		}
	}
	return ""
}

func sortConfigs(list []ConfigInfo) {
	for i := 1; i < len(list); i++ {
		item := list[i]
		j := i
		for j > 0 && (list[j-1].Label > item.Label || (list[j-1].Label == item.Label && list[j-1].ID > item.ID)) {
			list[j] = list[j-1]
			j--
		}
		list[j] = item
	}
}

func newConfigID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Networks[name]; !ok {
		return errors.New("сеть не найдена")
	}
	delete(s.data.Networks, name)
	return s.saveLocked()
}

func (s *Store) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.data.Networks))
	for name := range s.data.Networks {
		names = append(names, name)
	}
	return names
}

func (s *Store) Join(name, password, hostname string) (net.IP, error) {
	if !hostRe.MatchString(hostname) {
		return nil, errors.New("имя компьютера: от 1 до 32 символов, латиница, цифры, точка, _ и -")
	}

	s.mu.Lock()
	hash := dummyHash
	if n, ok := s.data.Networks[name]; ok {
		hash = n.PasswordHash
	}
	s.mu.Unlock()
	okPass := checkPassword(hash, password)

	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.data.Networks[name]
	if !okPass || !ok || n.PasswordHash != hash {
		return nil, errBadLogin
	}
	if n.Leases == nil {
		n.Leases = map[string]string{}
	}
	if ip, exists := n.Leases[hostname]; exists {
		return net.ParseIP(ip).To4(), nil
	}
	used := map[string]bool{}
	for _, ip := range n.Leases {
		used[ip] = true
	}
	for host := 2; host <= 254; host++ {
		ip := fmt.Sprintf("10.66.%d.%d", n.Octet, host)
		if used[ip] {
			continue
		}
		n.Leases[hostname] = ip
		if err := s.saveLocked(); err != nil {
			delete(n.Leases, hostname)
			return nil, err
		}
		return net.ParseIP(ip).To4(), nil
	}
	return nil, errors.New("в сети нет свободных адресов")
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iters, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2$%d$%s$%s", pbkdf2Iters, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(sum)), nil
}

func checkPassword(stored, password string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 || iter > 5_000_000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) < 8 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return hmac.Equal(got, want)
}

func (s *Store) allocOctetLocked() (int, error) {
	used := map[int]bool{}
	for _, n := range s.data.Networks {
		used[n.Octet] = true
	}
	for i := 0; i < 254; i++ {
		octet := s.data.NextOctet
		if octet < 1 || octet > 254 {
			octet = 1
		}
		s.data.NextOctet = octet + 1
		if s.data.NextOctet > 254 {
			s.data.NextOctet = 1
		}
		if !used[octet] {
			return octet, nil
		}
	}
	return 0, errors.New("достигнут предел числа сетей")
}

var errSave = errors.New("сервер не смог сохранить изменения")

func (s *Store) saveLocked() error {
	if err := s.writeLocked(); err != nil {
		log.Printf("не удалось сохранить %s: %v", s.path, err)
		return errSave
	}
	return nil
}

func (s *Store) writeLocked() error {
	if s.data.Networks == nil {
		s.data.Networks = map[string]*network{}
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
