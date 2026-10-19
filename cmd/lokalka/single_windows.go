//go:build windows

package main

import (
	"errors"
	"syscall"
	"unsafe"
)

var errShownRunning = errors.New("клиент уже запущен")

var (
	modKernel32Single = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutex   = modKernel32Single.NewProc("CreateMutexW")
	procFindWindowW   = syscall.NewLazyDLL("user32.dll").NewProc("FindWindowW")
	procShowWin       = syscall.NewLazyDLL("user32.dll").NewProc("ShowWindow")
	procForeground    = syscall.NewLazyDLL("user32.dll").NewProc("SetForegroundWindow")
)

const (
	errorAlreadyExists = syscall.Errno(183)

	clientWindowClass = "LokalkaMain"
)

func lockSingleClient() (func(), error) {
	name, err := syscall.UTF16PtrFromString(`Local\Lokalka-client`)
	if err != nil {
		return func() {}, nil
	}
	h, _, callErr := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return func() {}, nil
	}
	if callErr == errorAlreadyExists {
		syscall.CloseHandle(syscall.Handle(h))
		if showRunningClient() {
			return nil, errShownRunning
		}
		return nil, errors.New("Lokalka уже запущена на этом компьютере. Закройте её окно и откройте файл снова.")
	}
	return func() { syscall.CloseHandle(syscall.Handle(h)) }, nil
}

func showRunningClient() bool {
	class, err := syscall.UTF16PtrFromString(clientWindowClass)
	if err != nil {
		return false
	}
	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(class)), 0)
	if hwnd == 0 {
		return false
	}
	procShowWin.Call(hwnd, 9)
	procForeground.Call(hwnd)
	return true
}
