package admin

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"unicode"
)

func NormalizeEndpoint(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		return "", errors.New("адрес сервера должен быть вида host:port")
	}
	if host == "0.0.0.0" || host == "::" || host == "[::]" {
		return "", errors.New("в конфиг нельзя записывать адрес 0.0.0.0")
	}
	return net.JoinHostPort(host, port), nil
}

const ConfigExt = ".lokalka"

func ClientConfig(endpoint, network, password, configID, label, serverKey string) ([]byte, error) {
	endpoint, err := NormalizeEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if network == "" || password == "" {
		return nil, errors.New("в конфиге нет сети или пароля")
	}
	body, err := json.MarshalIndent(struct {
		Version   int    `json:"lokalka"`
		Server    string `json:"server"`
		ServerKey string `json:"server_key,omitempty"`
		Network   string `json:"network"`
		Password  string `json:"password"`
		ConfigID  string `json:"config_id,omitempty"`
		Label     string `json:"label,omitempty"`
	}{
		Version:   1,
		Server:    endpoint,
		ServerKey: serverKey,
		Network:   network,
		Password:  password,
		ConfigID:  configID,
		Label:     label,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func ContentDisposition(label string) string {
	ascii := asciiFilename(label)
	utf := url.PathEscape(label + ConfigExt)
	return `attachment; filename="` + ascii + ConfigExt + `"; filename*=UTF-8''` + utf
}

func asciiFilename(label string) string {
	var b strings.Builder
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), ".-")
	if name == "" {
		name = "lokalka"
	}
	if len(name) > 32 {
		name = name[:32]
	}
	return name
}
