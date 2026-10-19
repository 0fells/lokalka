package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadClientFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ноутбук.lokalka")
	body := []byte("{\n  \"lokalka\": 1,\n  \"server\": \"203.0.113.10:51820\",\n  \"network\": \"coop\",\n  \"password\": \"secret\"\n}\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadClientFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerAddr != "203.0.113.10:51820" || cfg.Network != "coop" || cfg.Password != "secret" {
		t.Fatalf("%+v", cfg)
	}
	if _, err := loadClientFile(filepath.Join(dir, "missing.lokalka")); err == nil {
		t.Fatal("отсутствующий файл должен быть ошибкой")
	}
}

func TestConfigBeside(t *testing.T) {
	dir := t.TempDir()
	path, err := configBeside(dir)
	if err != nil || path != "" {
		t.Fatalf("пусто: %q %v", path, err)
	}
	file := filepath.Join(dir, "home.lokalka")
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err = configBeside(dir)
	if err != nil || path != file {
		t.Fatalf("один файл: %q %v", path, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.lokalka"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := configBeside(dir); err == nil {
		t.Fatal("два конфига должны быть ошибкой")
	}
}

func TestCleanHost(t *testing.T) {
	keep := []string{"DESKTOP-AB12CD", "gaming.pc", "pc_1"}
	for _, h := range keep {
		if got := cleanHost(h); got != h {
			t.Errorf("латинское имя %q изменилось: %q", h, got)
		}
	}
	a, b := cleanHost("ВАДИМ-ПК"), cleanHost("МАКС-ПК")
	if a == b || !strings.HasPrefix(a, "VADIM-PK-") || !strings.HasPrefix(b, "MAKS-PK-") {
		t.Errorf("кириллица: %q и %q", a, b)
	}
	if cleanHost("ВАДИМ-ПК") != a {
		t.Error("имя должно быть стабильным: по нему закреплён адрес")
	}
	x, y := cleanHost("电脑一"), cleanHost("电脑二")
	if x == y || len(x) == 0 {
		t.Errorf("непереводимые имена слились: %q %q", x, y)
	}
	long := cleanHost(strings.Repeat("Я", 40))
	if len(long) > 32 || !hostOK(long) {
		t.Errorf("длинное имя: %q", long)
	}
	for _, h := range []string{a, b, x, y} {
		if !hostOK(h) {
			t.Errorf("сервер не примет %q", h)
		}
	}
}

func hostOK(h string) bool {
	if len(h) == 0 || len(h) > 32 {
		return false
	}
	for _, r := range h {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
