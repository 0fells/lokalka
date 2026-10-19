package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"lokalka/internal/admin"
	"lokalka/internal/stamp"
	"lokalka/internal/vpn"
)

func ownConfig() (stamp.File, bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return stamp.File{}, false, nil
	}
	return stamp.ReadFile(exe)
}

func looksLikeConfig(arg string) bool {
	return strings.EqualFold(filepath.Ext(arg), admin.ConfigExt)
}

func loadClientFile(path string) (vpn.Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return vpn.Config{}, errors.New("не удалось прочитать конфиг")
	}
	if len(b) > 1<<16 {
		return vpn.Config{}, errors.New("конфиг слишком большой")
	}
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	var file struct {
		Version   int    `json:"lokalka"`
		Server    string `json:"server"`
		ServerKey string `json:"server_key"`
		Network   string `json:"network"`
		Password  string `json:"password"`
		ConfigID  string `json:"config_id"`
	}
	if err := json.Unmarshal(b, &file); err != nil || file.Version != 1 {
		return vpn.Config{}, errors.New("файл конфига повреждён")
	}
	file.Server = strings.TrimSpace(file.Server)
	file.Network = strings.TrimSpace(file.Network)
	if file.Server == "" || file.Network == "" || file.Password == "" {
		return vpn.Config{}, errors.New("в конфиге нет адреса сервера, имени сети или пароля")
	}
	key, err := vpn.ParseServerKey(file.ServerKey)
	if err != nil {
		return vpn.Config{}, err
	}
	host, _ := os.Hostname()
	return vpn.Config{
		ServerAddr: file.Server,
		ServerKey:  key,
		Network:    file.Network,
		Password:   file.Password,
		Hostname:   cleanHost(host),
		ConfigID:   strings.TrimSpace(file.ConfigID),
	}, nil
}

func configBeside(dir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*"+admin.ConfigExt))
	if err != nil {
		return "", nil
	}
	var files []string
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil || info.IsDir() {
			continue
		}
		files = append(files, match)
	}
	switch len(files) {
	case 0:
		return "", nil
	case 1:
		return files[0], nil
	default:
		return "", errors.New("рядом с программой несколько файлов входа: оставьте один или перетащите нужный файл на программу")
	}
}

func sidecarConfig() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil
	}
	return configBeside(filepath.Dir(exe))
}
