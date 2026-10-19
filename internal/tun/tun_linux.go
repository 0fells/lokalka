//go:build linux

package tun

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

const (
	ifNameSize = 16
	iffTUN     = 0x0001
	iffNoPI    = 0x1000
	tunSetIFF  = 0x400454ca
)

type linuxDevice struct {
	file *os.File
	name string
}

func Open(ip string, prefix, mtu int) (Device, error) {
	fd, err := syscall.Open("/dev/net/tun", syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть /dev/net/tun (нужен root): %w", err)
	}
	var ifr [ifNameSize + 64]byte
	copy(ifr[:], "lokalka")
	*(*uint16)(unsafe.Pointer(&ifr[ifNameSize])) = iffTUN | iffNoPI
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(tunSetIFF), uintptr(unsafe.Pointer(&ifr[0])))
	if errno != 0 {
		syscall.Close(fd)
		return nil, errno
	}
	name := cString(ifr[:ifNameSize])
	dev := &linuxDevice{file: os.NewFile(uintptr(fd), name), name: name}
	if err := configure(name, ip, prefix, mtu); err != nil {
		dev.Close()
		return nil, err
	}
	return dev, nil
}

func configure(name, ip string, prefix, mtu int) error {
	commands := [][]string{
		{"ip", "link", "set", "dev", name, "mtu", fmt.Sprint(mtu)},
		{"ip", "addr", "flush", "dev", name},
		{"ip", "addr", "add", fmt.Sprintf("%s/%d", ip, prefix), "dev", name},
		{"ip", "link", "set", "dev", name, "up"},
	}
	for _, args := range commands {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %v: %s", args[0], err, out)
		}
	}
	return nil
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func (d *linuxDevice) Read(p []byte) (int, error)  { return d.file.Read(p) }
func (d *linuxDevice) Write(p []byte) (int, error) { return d.file.Write(p) }
func (d *linuxDevice) Close() error                { return d.file.Close() }
func (d *linuxDevice) Name() string                { return d.name }
