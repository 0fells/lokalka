//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

var procUILanguage = k32.NewProc("GetUserDefaultUILanguage")

type uiSettings struct {
	Lang string `json:"lang,omitempty"`
}

func settingsPath() string {
	return filepath.Join(localAppData(), "Lokalka", "settings.json")
}

func loadLang() string {
	if b, err := os.ReadFile(settingsPath()); err == nil {
		var s uiSettings
		if json.Unmarshal(b, &s) == nil && (s.Lang == langRU || s.Lang == langEN) {
			return s.Lang
		}
	}
	return systemLang()
}

func systemLang() string {
	id, _, _ := procUILanguage.Call()
	switch id & 0x3ff {
	case 0x19, 0x22, 0x23, 0x3f:
		return langRU
	}
	return langEN
}

func saveLang(lang string) error {
	path := settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(uiSettings{Lang: lang}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}
