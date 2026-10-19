//go:build windows

package tun

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

//go:embed wintun.dll
var embeddedWintun []byte

const adapterName = "Lokalka"

type winDevice struct {
	adapter uintptr
	session uintptr
	mu      sync.RWMutex
	closed  bool
}

func Open(ip string, prefix, mtu int) (Device, error) {
	if err := loadWintun(); err != nil {
		return nil, err
	}
	if old, err := openAdapter(adapterName); err == nil {
		closeAdapter(old)
		time.Sleep(200 * time.Millisecond)
	}
	adapter, err := createAdapter(adapterName)
	if err != nil {
		if old, openErr := openAdapter(adapterName); openErr == nil {
			closeAdapter(old)
			time.Sleep(300 * time.Millisecond)
			adapter, err = createAdapter(adapterName)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("не удалось создать адаптер %s: %w", adapterName, err)
	}
	session, err := startSession(adapter, 0x800000)
	if err != nil {
		closeAdapter(adapter)
		return nil, err
	}
	if err := configureIPv4(ip, prefix, mtu); err != nil {
		endSession(session)
		closeAdapter(adapter)
		return nil, err
	}
	return &winDevice{adapter: adapter, session: session}, nil
}

func (d *winDevice) Name() string { return adapterName }

func (d *winDevice) Read(buf []byte) (int, error) {
	for {
		d.mu.RLock()
		if d.closed {
			d.mu.RUnlock()
			return 0, ioEOF()
		}
		pkt, err := receivePacket(d.session)
		if err == nil {
			n := copy(buf, pkt)
			releaseReceivePacket(d.session, pkt)
			d.mu.RUnlock()
			return n, nil
		}
		var event syscall.Handle
		if errors.Is(err, syscall.Errno(259)) {
			event = readWaitEvent(d.session)
		}
		d.mu.RUnlock()
		if !errors.Is(err, syscall.Errno(259)) {
			return 0, err
		}

		if event != 0 {
			_, _ = syscall.WaitForSingleObject(event, 500)
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func (d *winDevice) Write(buf []byte) (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return 0, ioEOF()
	}
	out, err := allocateSendPacket(d.session, len(buf))
	if err != nil {
		return 0, err
	}
	copy(out, buf)
	sendPacket(d.session, out)
	return len(buf), nil
}

func (d *winDevice) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	endSession(d.session)
	closeAdapter(d.adapter)
	return nil
}

func ioEOF() error { return errors.New("адаптер закрыт") }

func netsh(args ...string) *exec.Cmd {
	cmd := exec.Command(filepath.Join(systemDir(), "netsh.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd
}

func configureIPv4(ip string, prefix, mtu int) error {
	mask := net.IP(net.CIDRMask(prefix, 32)).String()
	var last error
	for i := 0; i < 25; i++ {
		out, err := netsh("interface", "ipv4", "set", "address",
			"name="+adapterName, "source=static", "address="+ip, "mask="+mask, "gateway=none").CombinedOutput()
		if err == nil {
			_ = netsh("interface", "ipv4", "set", "subinterface",
				adapterName, fmt.Sprintf("mtu=%d", mtu), "store=active").Run()
			_ = netsh("interface", "ipv4", "set", "interface",
				adapterName, "metric=9000").Run()
			return nil
		}
		last = fmt.Errorf("%v: %s", err, out)
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("не удалось выдать адрес %s адаптеру %s. Запустите клиент от имени администратора: %w", ip, adapterName, last)
}

var (
	procCreate    *syscall.Proc
	procOpen      *syscall.Proc
	procClose     *syscall.Proc
	procStart     *syscall.Proc
	procEnd       *syscall.Proc
	procWait      *syscall.Proc
	procRecv      *syscall.Proc
	procRelease   *syscall.Proc
	procAllocSend *syscall.Proc
	procSend      *syscall.Proc
	loadOnce      sync.Once
	loadErr       error

	modKernel32       = syscall.NewLazyDLL("kernel32.dll")
	procSetDllDirs    = modKernel32.NewProc("SetDefaultDllDirectories")
	procLoadLibraryEx = modKernel32.NewProc("LoadLibraryExW")
	procSystemDir     = modKernel32.NewProc("GetSystemDirectoryW")
	procKnownFolder   = syscall.NewLazyDLL("shell32.dll").NewProc("SHGetKnownFolderPath")
	procTaskMemFree   = syscall.NewLazyDLL("ole32.dll").NewProc("CoTaskMemFree")
)

const (
	loadSearchDllLoadDir = 0x00000100
	loadSearchSystem32   = 0x00000800
)

func init() {
	procSetDllDirs.Call(loadSearchSystem32)
}

func loadWintun() error {
	loadOnce.Do(func() {
		path, err := extractWintun()
		if err != nil {
			loadErr = err
			return
		}
		path16, err := syscall.UTF16PtrFromString(path)
		if err != nil {
			loadErr = err
			return
		}
		h, _, callErr := procLoadLibraryEx.Call(uintptr(unsafe.Pointer(path16)), 0, loadSearchDllLoadDir|loadSearchSystem32)
		if h == 0 {
			loadErr = fmt.Errorf("не удалось загрузить драйвер из %s: %w", path, errnoErr(callErr))
			return
		}
		dll := &syscall.DLL{Name: path, Handle: syscall.Handle(h)}
		find := func(name string) *syscall.Proc {
			p, err := dll.FindProc(name)
			if err != nil && loadErr == nil {
				loadErr = fmt.Errorf("в драйвере %s нет %s: %w", path, name, err)
			}
			return p
		}
		procCreate = find("WintunCreateAdapter")
		procOpen = find("WintunOpenAdapter")
		procClose = find("WintunCloseAdapter")
		procStart = find("WintunStartSession")
		procEnd = find("WintunEndSession")
		procWait = find("WintunGetReadWaitEvent")
		procRecv = find("WintunReceivePacket")
		procRelease = find("WintunReleaseReceivePacket")
		procAllocSend = find("WintunAllocateSendPacket")
		procSend = find("WintunSendPacket")
	})
	return loadErr
}

func extractWintun() (string, error) {
	if len(embeddedWintun) == 0 {
		return "", errors.New("в программу не встроен драйвер сети")
	}
	base, err := programFiles()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "Lokalka")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("не удалось создать %s. Запустите клиент от имени администратора: %w", dir, err)
	}
	path := filepath.Join(dir, "wintun.dll")
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, embeddedWintun) {
		return path, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, embeddedWintun, 0o644); err != nil {
		return "", fmt.Errorf("не удалось записать драйвер в %s: %w", dir, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("не удалось обновить %s (его держит другая запущенная Lokalka?): %w", path, err)
	}

	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		_ = os.Remove(filepath.Join(local, "Lokalka", "wintun.dll"))
	}
	return path, nil
}

type guid struct {
	data1        uint32
	data2, data3 uint16
	data4        [8]byte
}

var folderProgramFiles = guid{0x905e63b6, 0xc1bf, 0x494e, [8]byte{0xb2, 0x9c, 0x65, 0xb7, 0x32, 0xd3, 0xd2, 0x1a}}

func programFiles() (string, error) {
	var p *uint16
	r, _, _ := procKnownFolder.Call(uintptr(unsafe.Pointer(&folderProgramFiles)), 0, 0, uintptr(unsafe.Pointer(&p)))
	if r != 0 || p == nil {
		return "", fmt.Errorf("не удалось узнать путь Program Files (код %#x)", r)
	}
	defer procTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	n := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(p), n*2)) != 0 {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n)), nil
}

func systemDir() string {
	buf := make([]uint16, 260)
	n, _, _ := procSystemDir.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || int(n) >= len(buf) {
		return `C:\Windows\System32`
	}
	return syscall.UTF16ToString(buf[:n])
}

func createAdapter(name string) (uintptr, error) {
	name16, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	type16, err := syscall.UTF16PtrFromString("Wintun")
	if err != nil {
		return 0, err
	}
	r0, _, e1 := procCreate.Call(uintptr(unsafe.Pointer(name16)), uintptr(unsafe.Pointer(type16)), 0)
	if r0 == 0 {
		return 0, errnoErr(e1)
	}
	return r0, nil
}

func openAdapter(name string) (uintptr, error) {
	name16, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	r0, _, e1 := procOpen.Call(uintptr(unsafe.Pointer(name16)))
	if r0 == 0 {
		return 0, errnoErr(e1)
	}
	return r0, nil
}

func closeAdapter(adapter uintptr) {
	_, _, _ = procClose.Call(adapter)
}

func startSession(adapter uintptr, capacity uint32) (uintptr, error) {
	r0, _, e1 := procStart.Call(adapter, uintptr(capacity))
	if r0 == 0 {
		return 0, errnoErr(e1)
	}
	return r0, nil
}

func endSession(session uintptr) {
	_, _, _ = procEnd.Call(session)
}

func readWaitEvent(session uintptr) syscall.Handle {
	r0, _, _ := procWait.Call(session)
	return syscall.Handle(r0)
}

func receivePacket(session uintptr) ([]byte, error) {
	var size uint32
	r0, _, e1 := procRecv.Call(session, uintptr(unsafe.Pointer(&size)))
	if r0 == 0 {
		return nil, errnoErr(e1)
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(r0)), size), nil
}

func releaseReceivePacket(session uintptr, packet []byte) {
	if len(packet) == 0 {
		return
	}
	_, _, _ = procRelease.Call(session, uintptr(unsafe.Pointer(&packet[0])))
}

func allocateSendPacket(session uintptr, size int) ([]byte, error) {
	r0, _, e1 := procAllocSend.Call(session, uintptr(size))
	if r0 == 0 {
		return nil, errnoErr(e1)
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(r0)), size), nil
}

func sendPacket(session uintptr, packet []byte) {
	if len(packet) == 0 {
		return
	}
	_, _, _ = procSend.Call(session, uintptr(unsafe.Pointer(&packet[0])))
}

func errnoErr(e error) error {
	if e == nil || e == syscall.Errno(0) {
		return errors.New("вызов wintun не удался")
	}
	return e
}
