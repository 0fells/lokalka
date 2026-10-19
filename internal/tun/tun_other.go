//go:build !windows && !linux

package tun

import "errors"

func Open(string, int, int) (Device, error) {
	return nil, errors.New("виртуальный адаптер поддерживается на Windows и Linux")
}
