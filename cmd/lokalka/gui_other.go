//go:build !windows

package main

import (
	"errors"
	"log"

	"lokalka/internal/stamp"
	"lokalka/internal/vpn"
)

func guiSupported() bool { return false }

var errShownRunning = errors.New("клиент уже запущен")

func lockSingleClient() (func(), error) { return func() {}, nil }

func runGUI(vpn.Config) error {
	return errors.New("окно клиента доступно в Windows")
}

func runAdminGUI() error {
	return errors.New("окно админки доступно в Windows")
}

func runPanelGUI(stamp.File) error {
	return errors.New("панель управления доступна в Windows")
}

func showError(err error) {
	if err == nil {
		return
	}
	log.Println(err)
}
