//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"lokalka/internal/stamp"
)

func ensureAdmin() {
	if !needsAdmin() || isAdmin() {
		return
	}
	if err := relaunchElevated(); err != nil {
		fmt.Fprintf(os.Stderr, "Нужны права администратора. Нажмите \"Да\" в окне Windows.\n%v\n", err)
		return
	}
	os.Exit(0)
}

func needsAdmin() bool {
	if file, ok, err := ownConfig(); err == nil && ok {
		if file.Role == stamp.RolePanel {
			return false
		}
		return !tunNone(os.Args)
	}
	if len(os.Args) < 2 && defaultMode == "admin" {
		return false
	}
	if len(os.Args) < 2 || looksLikeConfig(os.Args[1]) {
		return !tunNone(os.Args[1:])
	}
	if os.Args[1] != "join" {
		return false
	}
	return !tunNone(os.Args[1:])
}

func tunNone(args []string) bool {
	for i, arg := range args {
		if arg == "--tun=none" {
			return true
		}
		if arg == "--tun" && i+1 < len(args) && args[i+1] == "none" {
			return true
		}
	}
	return false
}

func isAdmin() bool {
	shell32 := syscall.NewLazyDLL("shell32.dll")
	proc := shell32.NewProc("IsUserAnAdmin")
	r, _, _ := proc.Call()
	return r != 0
}

func relaunchElevated() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, err := syscall.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	params, err := syscall.UTF16PtrFromString(quoteArgs(os.Args[1:]))
	if err != nil {
		return err
	}
	shell32 := syscall.NewLazyDLL("shell32.dll")
	proc := shell32.NewProc("ShellExecuteW")
	r, _, callErr := proc.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		0,
		1,
	)
	if r <= 32 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return fmt.Errorf("код %d", r)
	}
	return nil
}

func quoteArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = syscall.EscapeArg(arg)
	}
	return strings.Join(quoted, " ")
}
